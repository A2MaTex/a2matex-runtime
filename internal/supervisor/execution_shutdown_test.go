package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/discovery"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const shutdownGroup = "00000000-0000-4000-8000-000000000001"
const shutdownDevice = "00000000-0000-4000-8000-000000000002"

type liveExecution struct {
	store       *sqlitestore.Store
	repo        *repositories.ExecutionRepository
	checkpoints *services.CheckpointService
	lifetime    *executionlifecycle.Service
	worker      *runner.Runner
	request     runner.Request
	admission   execution.Admission
}

// Real G1, G9, G10 and SQLite; only the first handler is controlled.
func shutdownFixture(t *testing.T, handler registry.Handler, change ...func(*ir.Document)) *liveExecution {
	t.Helper()
	store := sqlitestore.New()
	repo := repositories.NewExecutionRepository(store)
	checkpoints := services.NewCheckpointService(repo, nil)
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := registry.New(catalog, []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: handler},
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: delay.Invoke},
		{Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke},
	})
	if err != nil {
		t.Fatal(err)
	}
	admissionValidator, err := runtimeir.New(catalog, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../execution/runner/testdata/workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(change) > 0 {
		var doc ir.Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, apply := range change {
			apply(&doc)
		}
		raw, err = json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
	}
	checked, err := admissionValidator.Validate(t.Context(), raw, []json.RawMessage{json.RawMessage(`{"duration":0}`)})
	if err != nil || checked.Validation.Status != validation.StatusValid {
		t.Fatalf("G1: %+v %v", checked.Validation, err)
	}
	resolver, err := bindings.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := outputs.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	invoker, err := invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := runner.New(t.Context(), checked.Document, runner.Dependencies{Bindings: resolver, Outputs: validator, Invoker: invoker, Checkpoints: checkpoints})
	if err != nil {
		t.Fatal(err)
	}
	return &liveExecution{store: store, repo: repo, checkpoints: checkpoints, lifetime: executionlifecycle.New(checkpoints), worker: worker,
		request:   runner.Request{RunGroupID: shutdownGroup, DeviceExecutionID: shutdownDevice, ADBSerial: "controlled-device", WorkflowInputs: ir.Object(checked.DeviceInputs[0])},
		admission: execution.Admission{RequestID: "00000000-0000-4000-8000-000000000003", RunGroupID: shutdownGroup, Now: time.Now().Add(-time.Second), Devices: []execution.AdmissionDevice{{DeviceExecutionID: shutdownDevice, ADBSerial: "controlled-device", EntryNodeID: checked.Document.EntryNodeID}}},
	}
}
func (f *liveExecution) admit(t *testing.T) {
	t.Helper()
	release, err := f.lifetime.BeginAdmission(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if result, err := f.repo.Admit(t.Context(), f.admission); err != nil || result.Disposition != constants.AdmissionCreated {
		t.Fatalf("admit: %+v %v", result, err)
	}
}
func localExecution(t *testing.T, ctx context.Context, handler registry.Handler, change ...func(*ir.Document)) *liveExecution {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	f := shutdownFixture(t, handler, change...)
	if err := f.store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.lifetime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.checkpoints.Shutdown(context.Background()); _ = f.store.Shutdown(context.Background()) })
	return f
}
func boundedStop(t *testing.T, f *liveExecution, reason constants.ExecutionStopReason) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	f.lifetime.PrepareStop(reason, deadline)
	return f.lifetime.Shutdown(ctx)
}
func TestExecutionLifetimeActiveStopAndDuplicateRegistration(t *testing.T) {
	entered := make(chan struct{})
	f := localExecution(t, t.Context(), func(ctx context.Context, _ registry.Invocation) (map[string]any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	f.admit(t)
	done := make(chan error, 1)
	go func() { _, err := f.lifetime.Run(t.Context(), f.worker, f.request); done <- err }()
	<-entered
	if _, err := f.lifetime.Run(t.Context(), f.worker, f.request); !errors.Is(err, xerror.ErrExecutionStaleState) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := boundedStop(t, f, constants.ExecutionStopIntentional); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("accepted active cancellation: %v", err)
	}
	if err := f.lifetime.Wait(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	rows, rowsErr := f.repo.Attempts(t.Context(), shutdownDevice, 0, 0)
	if err != nil || rowsErr != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || len(rows.Attempts) != 1 || rows.Attempts[0].Status != constants.ExecutionCancelled {
		t.Fatalf("active stop: %+v %+v %v %v", snapshot, rows, err, rowsErr)
	}
	if _, err := f.lifetime.BeginAdmission(t.Context()); !errors.Is(err, xerror.ErrExecutionStopping) {
		t.Fatalf("closed gate: %v", err)
	}
	if _, err := f.lifetime.Run(t.Context(), f.worker, f.request); !errors.Is(err, xerror.ErrExecutionStopping) {
		t.Fatalf("late worker: %v", err)
	}
	if err := boundedStop(t, f, constants.ExecutionStopIntentional); err != nil {
		t.Fatal(err)
	}
	after, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if !reflect.DeepEqual(snapshot, after) {
		t.Fatal("repeated shutdown changed history")
	}
}
func TestExecutionLifetimeRootStopBeforeAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	f := localExecution(t, ctx, func(ctx context.Context, _ registry.Invocation) (map[string]any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	f.admit(t)
	done := make(chan error, 1)
	go func() { _, err := f.lifetime.Run(ctx, f.worker, f.request); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("handoff: %v", err)
	}
	waiting := make(chan error, 1)
	go func() { waiting <- f.lifetime.Wait() }()
	select {
	case err := <-waiting:
		t.Fatalf("Wait exited before shutdown: %v", err)
	default:
	}
	if err := boundedStop(t, f, constants.ExecutionStopIntentional); err != nil {
		t.Fatal(err)
	}
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
	snapshot, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if snapshot.RunGroup.Status != constants.ExecutionCancelled {
		t.Fatalf("handoff not settled: %+v", snapshot)
	}
}
func TestExecutionLifetimeAdmissionFenceAndDeadline(t *testing.T) {
	f := localExecution(t, t.Context(), start.Invoke)
	release, err := f.lifetime.BeginAdmission(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
	done := make(chan error, 1)
	go func() { done <- f.lifetime.Shutdown(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("did not join lease: %v", err)
	default:
	}
	if _, err := f.repo.Admit(t.Context(), f.admission); err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snapshot, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	rows, _ := f.repo.Attempts(t.Context(), shutdownDevice, 0, 0)
	if snapshot.RunGroup.Status != constants.ExecutionCancelled || len(rows.Attempts) != 0 {
		t.Fatalf("lease race: %+v %+v", snapshot, rows)
	}
}
func TestExecutionLifetimeUncooperativeWorkerDeadline(t *testing.T) {
	entered, unblock := make(chan struct{}), make(chan struct{})
	f := localExecution(t, t.Context(), func(ctx context.Context, _ registry.Invocation) (map[string]any, error) {
		close(entered)
		<-unblock
		return nil, ctx.Err()
	})
	f.admit(t)
	done := make(chan error, 1)
	go func() { _, err := f.lifetime.Run(t.Context(), f.worker, f.request); done <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
	if err := f.lifetime.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("join timeout: %v", err)
	}
	snapshot, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if snapshot.RunGroup.Status != constants.ExecutionRunning {
		t.Fatal("settled before join")
	}
	_ = f.checkpoints.Shutdown(t.Context())
	close(unblock)
	<-done
	snapshot, _ = f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if snapshot.RunGroup.Status != constants.ExecutionRunning {
		t.Fatal("late worker changed closed writer")
	}
}
func TestExecutionLifetimePreservesTerminalResult(t *testing.T) {
	f := localExecution(t, t.Context(), start.Invoke)
	f.admit(t)
	result, err := f.lifetime.Run(t.Context(), f.worker, f.request)
	if err != nil || result.DeviceStatus != constants.ExecutionSucceeded {
		t.Fatalf("run: %+v %v", result, err)
	}
	before, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if err := boundedStop(t, f, constants.ExecutionStopIntentional); err != nil {
		t.Fatal(err)
	}
	after, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stop overwrote success")
	}
}

// Starts one controlled workflow before the normal B5 readiness publisher.
// It adds no production endpoint or launch flag.
type executionStarter struct {
	fixture *liveExecution
	t       *testing.T
	entered <-chan struct{}
	done    chan struct{}
}

func (s *executionStarter) Start(ctx context.Context) error {
	s.fixture.admit(s.t)
	go func() { defer close(s.done); _, _ = s.fixture.lifetime.Run(ctx, s.fixture.worker, s.fixture.request) }()
	select {
	case <-s.entered:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *executionStarter) Wait() error { <-s.done; return nil }
func (s *executionStarter) Shutdown(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestManagedExecutionShutdownChild(t *testing.T) {
	if os.Getenv("A2MATEX_G11_CHILD") != "1" {
		return
	}
	owner, err := discovery.OpenOwner(3, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	go owner.Watch(cancel)
	entered := make(chan struct{})
	var once sync.Once
	f := shutdownFixture(t, func(ctx context.Context, _ registry.Invocation) (map[string]any, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	starter := &executionStarter{fixture: f, t: t, entered: entered, done: make(chan struct{})}
	session, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{HTTP: config.HTTPConfig{ListenAddress: "127.0.0.1:41389"}}
	publisher := discovery.NewOwnerPublisher(cfg, session, owner)
	httpComponent := newFakeLifecycle("local_http_server", nil)
	// Assert preparation happened before HTTP draining; shutdown shares its deadline.
	httpComponent.shutdownFunc = func(ctx context.Context) error {
		if _, err := f.lifetime.BeginAdmission(ctx); !errors.Is(err, xerror.ErrExecutionStopping) {
			return errors.New("execution gate still open during HTTP drain")
		}
		return nil
	}
	app := &App{execution: f.lifetime, managed: true, shutdownTimeout: time.Second, logger: logger,
		components: []component{{name: "execution_store", lifecycle: f.store}, {name: "execution_recovery", lifecycle: services.NewExecutionRecovery(f.repo, logger)},
			{name: "execution_checkpoint", lifecycle: f.checkpoints}, {name: "execution_lifecycle", lifecycle: f.lifetime},
			{name: "fixture_starter", lifecycle: starter}, {name: "local_http_server", lifecycle: httpComponent}, {name: "managed_readiness_publisher", lifecycle: publisher}}}
	if err := app.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestManagedProcessCancellationAndOwnerLoss(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("inherited-descriptor fixture; native Windows acceptance is separate")
	}
	for _, frame := range []string{"shutdown\n", "", "invalid\n"} {
		t.Run(frame, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_STATE_HOME", root)
			t.Setenv("LOCALAPPDATA", root)
			commandRead, commandWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer commandRead.Close()
			defer commandWrite.Close()
			readyRead, readyWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer readyRead.Close()
			defer readyWrite.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestManagedExecutionShutdownChild$", "-test.timeout=8s")
			cmd.Env = append(os.Environ(), "A2MATEX_G11_CHILD=1")
			cmd.ExtraFiles = []*os.File{commandRead, readyWrite}
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			_ = commandRead.Close()
			_ = readyWrite.Close()
			readiness := make(chan error, 1)
			go func() { _, err := bufio.NewReader(readyRead).ReadBytes('\n'); readiness <- err }()
			select {
			case err := <-readiness:
				if err != nil {
					t.Fatalf("readiness: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("child never ready")
			}
			if _, err := commandWrite.WriteString(frame); err != nil {
				t.Fatal(err)
			}
			_ = commandWrite.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("child exit: %v\n%s", err, output.String())
			}
			store := sqlitestore.New()
			if err := store.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer store.Shutdown(context.Background())
			repo := repositories.NewExecutionRepository(store)
			before, err := repo.SnapshotByID(t.Context(), shutdownGroup)
			if err != nil {
				t.Fatal(err)
			}
			if frame == "shutdown\n" && before.RunGroup.Status != constants.ExecutionCancelled {
				t.Fatalf("intentional stop not durable: %+v", before)
			}
			if frame != "shutdown\n" && before.RunGroup.Status != constants.ExecutionRunning {
				t.Fatalf("owner loss claimed cancellation: %+v", before)
			}
			recovery := services.NewExecutionRecovery(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := recovery.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, err := repo.SnapshotByID(t.Context(), shutdownGroup)
			rows, rowsErr := repo.Attempts(t.Context(), shutdownDevice, 0, 0)
			want := constants.ExecutionInterrupted
			if frame == "shutdown\n" {
				want = constants.ExecutionCancelled
				if !reflect.DeepEqual(before, after) {
					t.Fatal("recovery changed clean stop")
				}
			}
			if err != nil || rowsErr != nil || after.RunGroup.Status != want || len(rows.Attempts) != 1 || rows.Attempts[0].Status != want {
				t.Fatalf("restart: %+v %+v %v %v", after, rows, err, rowsErr)
			}
		})
	}
}

func TestExecutionStopReasonPolicy(t *testing.T) {
	for _, tc := range []struct {
		name            string
		managed, failed bool
		cause           error
		want            constants.ExecutionStopReason
	}{
		{"owner stop", true, false, xerror.ErrOwnerShutdownRequested, constants.ExecutionStopIntentional},
		{"owner loss", true, false, xerror.ErrOwnerDisconnected, constants.ExecutionStopOwnerLost},
		{"protocol", true, false, xerror.ErrOwnerProtocol, constants.ExecutionStopFailure},
		{"managed signal", true, false, context.Canceled, constants.ExecutionStopFailure},
		{"standalone signal", false, false, context.Canceled, constants.ExecutionStopIntentional},
		{"deadline", false, false, context.DeadlineExceeded, constants.ExecutionStopFailure},
		{"component failure wins", true, true, xerror.ErrOwnerShutdownRequested, constants.ExecutionStopFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(tc.cause)
			if got := (&App{managed: tc.managed}).stopReason(ctx, tc.failed); got != tc.want {
				t.Fatalf("reason: %s", got)
			}
		})
	}
}
func TestExecutionLifetimeOwnerLossCannotBeUpgraded(t *testing.T) {
	f := localExecution(t, t.Context(), start.Invoke)
	f.admit(t)
	deadline := time.Now().Add(time.Second)
	f.lifetime.PrepareStop(constants.ExecutionStopOwnerLost, deadline)
	f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline.Add(time.Second))
	if err := f.lifetime.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if snapshot.RunGroup.Status != constants.ExecutionRunning {
		t.Fatal("owner loss upgraded to cancellation")
	}
}
func TestExecutionLifetimeInvalidStopCannotWrite(t *testing.T) {
	for _, reason := range []constants.ExecutionStopReason{"unknown", constants.ExecutionStopIntentional} {
		t.Run(string(reason), func(t *testing.T) {
			f := localExecution(t, t.Context(), start.Invoke)
			f.admit(t)
			deadline := time.Now().Add(time.Second)
			if reason == constants.ExecutionStopIntentional {
				deadline = time.Time{}
			}
			f.lifetime.PrepareStop(reason, deadline)
			if err := f.lifetime.Shutdown(t.Context()); !errors.Is(err, xerror.ErrExecutionLifecycleInvalid) {
				t.Fatalf("invalid stop: %v", err)
			}
			snapshot, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
			if snapshot.RunGroup.Status != constants.ExecutionRunning {
				t.Fatal("invalid stop wrote cancellation")
			}
		})
	}
	unstarted := executionlifecycle.New(nil)
	unstarted.PrepareStop(constants.ExecutionStopIntentional, time.Now().Add(time.Second))
	if err := unstarted.Shutdown(t.Context()); !errors.Is(err, xerror.ErrExecutionLifecycleInvalid) {
		t.Fatalf("unstarted shutdown: %v", err)
	}
}

func TestExecutionLifetimeStopsDuringRetryGap(t *testing.T) {
	var dispatches atomic.Int32
	f := localExecution(t, t.Context(), func(context.Context, registry.Invocation) (map[string]any, error) {
		dispatches.Add(1)
		return nil, xerror.CommandError{Code: constants.CommandStartFailed, Message: "controlled pre-dispatch failure", Retryable: true}
	}, func(doc *ir.Document) { doc.Nodes[0].RetryPolicy = ir.RetryPolicy{MaxAttempts: 2, DelayMS: 5000} })
	f.admit(t)
	done := make(chan error, 1)
	go func() { _, err := f.lifetime.Run(t.Context(), f.worker, f.request); done <- err }()
	// Observe the real committed retry event, rather than sleeping until an assumed boundary.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		page, err := f.repo.ReplayEvents(t.Context(), 0, constants.MaxEventBatchSize)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range page.Events {
			if event.Type == constants.EventRetryScheduled {
				found = true
				break
			}
		}
		if found {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("retry event was not committed")
		}
	}
	if err := boundedStop(t, f, constants.ExecutionStopIntentional); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("retry handoff: %v", err)
	}
	rows, err := f.repo.Attempts(t.Context(), shutdownDevice, 0, 0)
	snapshot, snapshotErr := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if err != nil || snapshotErr != nil || dispatches.Load() != 1 || len(rows.Attempts) != 1 || rows.Attempts[0].Status != constants.ExecutionFailed || snapshot.RunGroup.Status != constants.ExecutionCancelled {
		t.Fatalf("retry stop: %+v %+v dispatches=%d %v %v", rows, snapshot, dispatches.Load(), err, snapshotErr)
	}
}
func TestExecutionLifetimeWorkerFailureIsObservable(t *testing.T) {
	f := localExecution(t, t.Context(), start.Invoke)
	if _, err := f.lifetime.Run(t.Context(), &runner.Runner{}, f.request); err == nil {
		t.Fatal("unexpected worker guard hidden")
	}
	if err := f.lifetime.Wait(); err == nil {
		t.Fatal("worker failure not supervised")
	}
	if err := boundedStop(t, f, constants.ExecutionStopIntentional); err == nil {
		t.Fatal("stop masked worker failure")
	}
}
func TestExecutionLifetimeDurableFailureIsNotInfrastructureFailure(t *testing.T) {
	f := localExecution(t, t.Context(), func(context.Context, registry.Invocation) (map[string]any, error) {
		return nil, xerror.CommandError{Code: constants.CommandFailed, Message: "controlled node failure"}
	})
	f.admit(t)
	result, err := f.lifetime.Run(t.Context(), f.worker, f.request)
	if err != nil || result.DeviceStatus != constants.ExecutionFailed {
		t.Fatalf("durable node failure: %+v %v", result, err)
	}
	before, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if err := boundedStop(t, f, constants.ExecutionStopIntentional); err != nil {
		t.Fatal(err)
	}
	after, _ := f.repo.SnapshotByID(t.Context(), shutdownGroup)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stop overwrote terminal failure")
	}
}
