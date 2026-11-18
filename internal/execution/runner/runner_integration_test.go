package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type integration struct {
	runner   *Runner
	repo     *repositories.ExecutionRepository
	store    *sqlitestore.Store
	requests []Request
}

func integrationFixture(t *testing.T, doc *ir.Document, inputs []string, handler registry.Handler, wrap func(*services.CheckpointService) attempts.Checkpoints) *integration {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return initializeIntegration(t, doc, inputs, handler, wrap, store)
}
func initializeIntegration(t *testing.T, doc *ir.Document, inputs []string, handler registry.Handler, wrap func(*services.CheckpointService) attempts.Checkpoints, store *sqlitestore.Store) *integration {
	t.Helper()
	f := &integration{store: store, repo: repositories.NewExecutionRepository(store)}
	catalog, _, deps := components(t, nil)
	groupID := uuid.NewString()
	rawInputs := make([]json.RawMessage, len(inputs))
	devices := make([]execution.AdmissionDevice, len(inputs))
	bySerial := map[string]string{}
	for i, value := range inputs {
		serial := fmt.Sprintf("emulator-%d", 5554+2*i)
		id := uuid.NewString()
		f.requests = append(f.requests, Request{RunGroupID: groupID, DeviceExecutionID: id, ADBSerial: serial})
		devices[i] = execution.AdmissionDevice{DeviceExecutionID: id, ADBSerial: serial, EntryNodeID: doc.EntryNodeID}
		bySerial[serial] = id
		rawInputs[i] = json.RawMessage(`{"duration":` + value + `}`)
	}
	if handler == nil {
		handler = delay.Invoke
	}
	observe := func(run registry.Handler) registry.Handler {
		return func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
			page, err := f.repo.Attempts(t.Context(), bySerial[call.ADBSerial], 0, constants.MaxAttemptPageSize)
			if err != nil || len(page.Attempts) == 0 || page.Attempts[len(page.Attempts)-1].Status != constants.ExecutionRunning {
				t.Fatalf("invocation before durable RUNNING: %+v %v", page, err)
			}
			return run(ctx, call)
		}
	}
	handlers, err := registry.New(catalog, []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: observe(start.Invoke)},
		{Type: "flow.end", RuntimeHandler: "flow.end", Run: observe(end.Invoke)},
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: observe(handler)},
	})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := runtimeir.New(catalog, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := validator.Validate(t.Context(), raw, rawInputs)
	if err != nil || checked.Validation.Status != validation.StatusValid {
		t.Fatalf("G1 rejected integration workflow: %+v %v", checked.Validation, err)
	}
	for i := range f.requests {
		f.requests[i].WorkflowInputs = ir.Object(checked.DeviceInputs[i])
	}
	deps.Invoker, err = invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := services.NewCheckpointService(f.repo, nil)
	t.Cleanup(func() {
		if err := checkpoints.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	deps.Checkpoints = checkpoints
	if wrap != nil {
		deps.Checkpoints = wrap(checkpoints)
	}
	f.runner, err = New(t.Context(), checked.Document, deps)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := f.repo.Admit(t.Context(), execution.Admission{RequestID: uuid.NewString(), RunGroupID: groupID, Now: time.Now().Add(-time.Second), Devices: devices})
	if err != nil || admitted.Disposition != constants.AdmissionCreated {
		t.Fatalf("D2 admission: %+v %v", admitted, err)
	}
	return f
}
func rows(t *testing.T, f *integration, index int) []execution.NodeAttempt {
	t.Helper()
	page, err := f.repo.Attempts(t.Context(), f.requests[index].DeviceExecutionID, 0, constants.MaxAttemptPageSize)
	if err != nil {
		t.Fatal(err)
	}
	return page.Attempts
}
func assertTerminal(t *testing.T, f *integration, index int, result Result) {
	t.Helper()
	snapshot, err := f.repo.SnapshotByID(t.Context(), f.requests[index].RunGroupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range snapshot.DeviceExecutions {
		if device.ID == f.requests[index].DeviceExecutionID {
			if device.Status != result.DeviceStatus || device.NextNodeID != nil {
				t.Fatalf("device/result disagreement: %+v %+v", device, result)
			}
		}
	}
	history := rows(t, f, index)
	seen := map[string]bool{}
	for i, attempt := range history {
		if attempt.Sequence != int64(i+1) || seen[attempt.ID] || attempt.Status == constants.ExecutionRunning {
			t.Fatalf("history: %+v", history)
		}
		seen[attempt.ID] = true
	}
	last := history[len(history)-1]
	if last.ID != result.LastAttempt.Ref.AttemptID || last.Sequence != result.LastAttempt.Sequence || !result.LastAttempt.Finished {
		t.Fatalf("terminal evidence: %+v %+v", last, result)
	}
}

func TestSQLiteCompleteWorkflowAndIsolation(t *testing.T) {
	f := integrationFixture(t, document(t), []string{"7", "11"}, nil, nil)
	type reply struct {
		index  int
		result Result
		err    error
	}
	done := make(chan reply, 2)
	for i := range f.requests {
		go func() { result, err := f.runner.Run(t.Context(), f.requests[i]); done <- reply{i, result, err} }()
	}
	for range f.requests {
		item := <-done
		if item.err != nil || item.result.DeviceStatus != constants.ExecutionSucceeded || item.result.TerminalError != nil {
			t.Fatalf("workflow: %+v %v", item.result, item.err)
		}
		assertTerminal(t, f, item.index, item.result)
		history := rows(t, f, item.index)
		if len(history) != 4 || history[0].NodeID != "start" || history[1].NodeID != "producer" || history[2].NodeID != "consumer" || history[3].NodeID != "end" {
			t.Fatalf("order: %+v", history)
		}
		want := `{"durationMs":` + string(f.requests[item.index].WorkflowInputs["duration"]) + `}`
		if string(history[1].Output) != want || string(history[2].Output) != want {
			t.Fatalf("cross-device input/output leak: %+v", history)
		}
	}
	snapshot, err := f.repo.SnapshotByID(t.Context(), f.requests[0].RunGroupID)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionSucceeded || snapshot.RunGroup.Summary.Succeeded != 2 {
		t.Fatalf("group: %+v %v", snapshot, err)
	}
}

func TestSQLiteFailureRoutesRetriesAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, code, want            string
		branch                      bool
		attempts, failures, history int
		timeout                     bool
	}{
		{"handled error", constants.CommandFailed, constants.ExecutionSucceeded, true, 1, 1, 3, false},
		{"unhandled error", constants.CommandFailed, constants.ExecutionFailed, false, 1, 1, 2, false},
		{"safe retry succeeds", constants.CommandStartFailed, constants.ExecutionSucceeded, false, 2, 1, 5, false},
		{"retry exhausted then error branch", constants.CommandStartFailed, constants.ExecutionSucceeded, true, 2, 2, 4, false},
		{"timeout stops", constants.NodeTimedOut, constants.ExecutionFailed, false, 2, 1, 2, true},
		{"timeout error branch", constants.NodeTimedOut, constants.ExecutionSucceeded, true, 2, 1, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := document(t)
			doc.Nodes[1].RetryPolicy.MaxAttempts = int64(tc.attempts)
			if tc.branch {
				doc.Nodes[1].ErrorPolicy = constants.ErrorPolicyErrorBranch
				doc.Edges = append(doc.Edges, ir.Edge{ID: "e_error", SourceNodeID: "producer", SourcePort: "error", TargetNodeID: "end"})
			}
			if tc.timeout {
				doc.Nodes[1].TimeoutMS = 1
			}
			calls := 0
			f := integrationFixture(t, doc, []string{"0"}, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				calls++
				if calls <= tc.failures {
					if tc.timeout {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return nil, xerror.CommandError{Code: tc.code, Message: "Controlled dispatch failure", Retryable: tc.code == constants.CommandStartFailed}
				}
				return delay.Invoke(ctx, call)
			}, nil)
			result, err := f.runner.Run(t.Context(), f.requests[0])
			if err != nil || result.DeviceStatus != tc.want {
				t.Fatalf("workflow: %+v %v", result, err)
			}
			assertTerminal(t, f, 0, result)
			history := rows(t, f, 0)
			if len(history) != tc.history {
				t.Fatalf("unexpected dispatch count: %+v", history)
			}
			if tc.want == constants.ExecutionSucceeded && result.TerminalError != nil {
				t.Fatal("handled error tainted successful result")
			}
			if tc.want == constants.ExecutionFailed && (result.TerminalError == nil || result.TerminalError.Code != tc.code) {
				t.Fatalf("terminal error: %+v", result.TerminalError)
			}
			if history[1].AttemptNumber != 1 {
				t.Fatal("node attempt number did not reset")
			}
			if tc.failures == 2 || tc.name == "safe retry succeeds" {
				if history[2].AttemptNumber != 2 {
					t.Fatalf("retry numbering: %+v", history)
				}
			}
		})
	}
}

type afterFinish struct {
	*services.CheckpointService
	hook func(execution.FinishAttemptRequest)
}

func (s afterFinish) FinishAttempt(ctx context.Context, r execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	result, err := s.CheckpointService.FinishAttempt(ctx, r)
	if err == nil && result.Disposition == constants.CheckpointCommitted {
		s.hook(r)
	}
	return result, err
}
func TestSQLiteCancellationHandoffAndTerminalRace(t *testing.T) {
	for _, when := range []string{"queued", "after start", "during handler", "retry gap", "after end"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var accepted atomic.Bool
			doc := document(t)
			if when == "retry gap" {
				doc.Nodes[1].RetryPolicy = ir.RetryPolicy{MaxAttempts: 2, DelayMS: 1000}
			}
			f := integrationFixture(t, doc, []string{"0"}, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				if when == "during handler" {
					accepted.Store(true)
					cancel()
				}
				if when == "retry gap" {
					return nil, xerror.CommandError{Code: constants.CommandStartFailed, Message: "Controlled pre-dispatch failure", Retryable: true}
				}
				return delay.Invoke(ctx, call)
			}, func(s *services.CheckpointService) attempts.Checkpoints {
				return afterFinish{s, func(r execution.FinishAttemptRequest) {
					if (when == "after start" && r.NodeID == "start") || (when == "after end" && r.NodeID == "end") || (when == "retry gap" && r.Retry != nil) {
						accepted.Store(true)
						cancel()
					}
				}}
			})
			req := f.requests[0]
			req.Control = func() attempts.Control {
				return attempts.Control{GroupCancellation: accepted.Load(), ShutdownDeadline: time.Now().Add(time.Second)}
			}
			if when == "queued" {
				accepted.Store(true)
			}
			result, err := f.runner.Run(ctx, req)
			switch when {
			case "after end":
				if err != nil || result.DeviceStatus != constants.ExecutionSucceeded {
					t.Fatalf("terminal race: %+v %v", result, err)
				}
				assertTerminal(t, f, 0, result)
			case "during handler":
				if err != nil || result.DeviceStatus != constants.ExecutionCancelled {
					t.Fatalf("active cancellation: %+v %v", result, err)
				}
				assertTerminal(t, f, 0, result)
			default:
				checkHandoff(t, result, err)
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				history := rows(t, f, 0)
				want := map[string]int{"queued": 0, "after start": 1, "retry gap": 2}[when]
				if len(history) != want {
					t.Fatalf("stop dispatched later work: %+v", history)
				}
				for _, attempt := range history {
					if attempt.Status == constants.ExecutionRunning || attempt.Status == constants.ExecutionCancelled {
						t.Fatalf("boundary changed attempt history: %+v", history)
					}
				}
				snapshot, err := f.repo.SnapshotByID(t.Context(), req.RunGroupID)
				if err != nil || snapshot.RunGroup.Status != constants.ExecutionRunning {
					t.Fatalf("G10 falsely settled G11 work: %+v %v", snapshot, err)
				}
			}
		})
	}
}

func TestSQLiteFinishFaultReturnsUnconfirmedEvidence(t *testing.T) {
	f := integrationFixture(t, document(t), []string{"0"}, nil, nil)
	if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER fail_producer_finish BEFORE UPDATE ON node_attempts WHEN NEW.node_id='producer' BEGIN SELECT RAISE(ABORT, 'test finish fault'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := f.runner.Run(t.Context(), f.requests[0])
	checkHandoff(t, result, err)
	if !errors.Is(err, xerror.ErrCheckpointFatal) || result.LastAttempt.Ref.NodeID != "producer" || !result.LastAttempt.Began || result.LastAttempt.Finished || result.NextNodeID != "producer" {
		t.Fatalf("finish failure: %+v %v", result, err)
	}
	history := rows(t, f, 0)
	if len(history) != 2 || history[0].Status != constants.ExecutionSucceeded || history[1].Status != constants.ExecutionRunning || history[1].Output != nil {
		t.Fatalf("unsafe history: %+v", history)
	}
}

func TestRunnerRestartInterruption(t *testing.T) {
	if marker := os.Getenv("A2MATEX_G10_CRASH_MARKER"); marker != "" {
		store := sqlitestore.New()
		if err := store.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		f := initializeIntegration(t, document(t), []string{"0"}, func(context.Context, registry.Invocation) (map[string]any, error) {
			file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("dispatched\n"); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			os.Exit(77)
			return nil, nil
		}, nil, store)
		if _, err := f.runner.Run(t.Context(), f.requests[0]); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash fixture did not exit")
	}
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	marker := filepath.Join(root, "side_effect.txt")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestRunnerRestartInterruption$")
	child.Env = append(os.Environ(), "A2MATEX_G10_CRASH_MARKER="+marker)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 77 {
		t.Fatalf("unexpected child exit: %v %s", err, output)
	}
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	repo := repositories.NewExecutionRepository(store)
	before, err := repo.ActiveSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	recovery := services.NewExecutionRecovery(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := recovery.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := repo.SnapshotByID(t.Context(), before.RunGroup.ID)
	if err != nil || after.RunGroup.Status != constants.ExecutionInterrupted {
		t.Fatalf("recovery: %+v %v", after, err)
	}
	page, err := repo.Attempts(t.Context(), after.DeviceExecutions[0].ID, 0, constants.MaxAttemptPageSize)
	if err != nil || len(page.Attempts) != 2 || page.Attempts[0].Status != constants.ExecutionSucceeded || page.Attempts[1].Status != constants.ExecutionInterrupted {
		t.Fatalf("recovery history: %+v %v", page, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "dispatched\n" {
		t.Fatalf("action replayed or lost: %q %v", data, err)
	}
	if _, err := repo.ActiveSnapshot(t.Context()); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatal("unfinished run survived recovery", err)
	}
}

// Concurrent callers for the same newly admitted device must still pass D4's
// expected QUEUED state; a second invocation cannot manufacture another worker.
func TestSQLiteCompetingRunnerCalls(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f := integrationFixture(t, document(t), []string{"0"}, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		once.Do(func() { close(entered); <-release })
		return delay.Invoke(ctx, call)
	}, nil)
	type reply struct {
		result Result
		err    error
	}
	done := make(chan reply, 1)
	go func() { result, err := f.runner.Run(t.Context(), f.requests[0]); done <- reply{result, err} }()
	released, joined := false, false
	defer func() {
		if !released {
			close(release)
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("worker did not exit")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter")
	}
	duplicate, err := f.runner.Run(t.Context(), f.requests[0])
	checkHandoff(t, duplicate, err)
	if !errors.Is(err, xerror.ErrExecutionStaleState) || duplicate.LastAttempt.Began {
		t.Fatalf("competing worker authorized: %+v %v", duplicate, err)
	}
	close(release)
	released = true
	winner := <-done
	joined = true
	if winner.err != nil || winner.result.DeviceStatus != constants.ExecutionSucceeded {
		t.Fatalf("winner: %+v %v", winner.result, winner.err)
	}
	if len(rows(t, f, 0)) != 4 {
		t.Fatal("competing call duplicated work")
	}
}
