package runadmission_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type deviceSource struct {
	serials []string
	offline bool
}

func (s deviceSource) Discover(ctx context.Context) ([]adb.Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoints := make([]adb.Endpoint, len(s.serials))
	for i, serial := range s.serials {
		endpoints[len(s.serials)-1-i] = adb.Endpoint{ADBSerial: serial, State: adb.Connected}
	}
	return endpoints, nil
}
func (s deviceSource) ProbeReadiness(ctx context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
	if err := ctx.Err(); err != nil {
		return adb.Readiness{}, err
	}
	if s.offline {
		return adb.Readiness{ADBSerial: endpoint.ADBSerial, Reason: &adb.UnavailableReason{Code: constants.DeviceOffline, Message: "controlled offline device"}}, nil
	}
	return adb.Readiness{ADBSerial: endpoint.ADBSerial, Ready: true}, nil
}

type integration struct {
	deps        runadmission.Dependencies
	store       *sqlitestore.Store
	repo        *repositories.ExecutionRepository
	checkpoints *services.CheckpointService
	lifetime    *executionlifecycle.Service
	req         runadmission.Request
	calls       *atomic.Int32
}

func fixture(t *testing.T, count int) *integration {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	f := &integration{store: sqlitestore.New(), req: request(t, count)}
	if err := f.store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.repo = repositories.NewExecutionRepository(f.store)
	f.checkpoints = services.NewCheckpointService(f.repo, nil)
	f.lifetime = executionlifecycle.New(f.checkpoints)
	if err := f.lifetime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	validator, runnerDeps, calls := components(t)
	f.calls = calls
	runnerDeps.Checkpoints = f.checkpoints
	serials := make([]string, count)
	for i, d := range f.req.Devices {
		serials[i] = d.ADBSerial
	}
	f.deps = runadmission.Dependencies{Validator: validator, Runner: runnerDeps, Repository: f.repo, Lifecycle: f.lifetime, Preflight: services.NewDevicePreflight(deviceSource{serials: serials})}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		f.lifetime.PrepareStop(constants.ExecutionStopFailure, deadline)
		if err := f.lifetime.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := f.checkpoints.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := f.store.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}
func counts(t *testing.T, f *integration) (groups, devices, events, attempts int) {
	t.Helper()
	err := f.store.DB().QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM run_groups),(SELECT count(*) FROM device_executions),(SELECT count(*) FROM execution_events),(SELECT count(*) FROM node_attempts)`).Scan(&groups, &devices, &events, &attempts)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("admission invoked a workflow handler")
	}
	return
}
func requireCounts(t *testing.T, f *integration, g, d, e int) {
	t.Helper()
	groups, devices, events, attempts := counts(t, f)
	if groups != g || devices != d || events != e || attempts != 0 {
		t.Fatalf("counts: groups=%d devices=%d events=%d attempts=%d", groups, devices, events, attempts)
	}
}
func stop(t *testing.T, f *integration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
	if err := f.lifetime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func wrappedRepository(f *integration) repositoryStub {
	return repositoryStub{lookup: f.repo.LookupRequest, admit: f.repo.Admit, snapshot: f.repo.SnapshotByID}
}
func TestSQLiteAdmissionAndReplayBoundaries(t *testing.T) {
	for _, count := range []int{1, constants.MaxAdmissionDevices} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := fixture(t, count)
			s := service(t, f.deps)
			created, err := s.Admit(t.Context(), f.req)
			if err != nil || created.Work == nil || created.Disposition != constants.AdmissionCreated {
				t.Fatalf("create: %+v %v", created, err)
			}
			requireCounts(t, f, 1, count, count+1)
			snapshot, err := f.repo.SnapshotByID(t.Context(), created.RunGroupID)
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for i, device := range snapshot.DeviceExecutions {
				work := created.Work.Devices[i]
				if ids[device.ID] || device.ID != work.DeviceExecutionID || device.ADBSerial != f.req.Devices[i].ADBSerial || device.ADBSerial != work.ADBSerial || device.Status != constants.ExecutionQueued || device.NextNodeID == nil || *device.NextNodeID != "start" || string(work.WorkflowInputs["duration"]) != fmt.Sprint(i+1) {
					t.Fatalf("ordered pairing: %+v %+v", device, work)
				}
				ids[device.ID] = true
			}
			replay, err := s.Admit(t.Context(), runadmission.Request{RequestID: f.req.RequestID, RuntimeIR: json.RawMessage("changed-invalid")})
			if err != nil || replay.Work != nil || replay.Snapshot == nil || !reflect.DeepEqual(snapshot, *replay.Snapshot) {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			// A new intention cannot take the currently occupied group slot.
			next := f.req
			next.RequestID = uuid.NewString()
			busy, err := s.Admit(t.Context(), next)
			if err != nil || busy.Disposition != constants.AdmissionBusy || busy.Work != nil {
				t.Fatalf("busy: %+v %v", busy, err)
			}
			requireCounts(t, f, 1, count, count+1)
			clear(f.req.RuntimeIR)
			clear(f.req.Devices[0].Inputs)
			if string(created.Work.Devices[0].WorkflowInputs["duration"]) != "1" {
				t.Fatal("request bytes escaped into work")
			}
			stop(t, f)
			cancelled, _ := f.repo.SnapshotByID(t.Context(), created.RunGroupID)
			if cancelled.RunGroup.Summary.Cancelled != count {
				t.Fatalf("queued stop: %+v", cancelled)
			}
			requireCounts(t, f, 1, count, 2*(count+1))
		})
	}
}
func TestSQLiteRejectionCreatesNoRecords(t *testing.T) {
	for _, kind := range []string{"workflow", "inputs", "incompatible", "offline", "adb failure"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t, 2)
			switch kind {
			case "workflow":
				f.req.RuntimeIR = json.RawMessage(`{}`)
			case "inputs":
				f.req.Devices[1].Inputs = json.RawMessage(`{"duration":"wrong type"}`)
			case "incompatible":
				var doc map[string]any
				if err := json.Unmarshal(f.req.RuntimeIR, &doc); err != nil {
					t.Fatal(err)
				}
				doc["minimumRuntimeVersion"] = "999.0.0"
				var err error
				f.req.RuntimeIR, err = json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
			case "offline":
				f.deps.Preflight = services.NewDevicePreflight(deviceSource{serials: []string{f.req.Devices[0].ADBSerial, f.req.Devices[1].ADBSerial}, offline: true})
			case "adb failure":
				f.deps.Preflight = preflightFunc(func(context.Context, []string) (services.DeviceSelectionResult, error) {
					return services.DeviceSelectionResult{}, xerror.ErrADBDiscovery
				})
			}
			got, err := service(t, f.deps).Admit(t.Context(), f.req)
			if kind == "adb failure" {
				if !errors.Is(err, xerror.ErrADBDiscovery) || got != (runadmission.Result{}) {
					t.Fatalf("adb fault: %+v %v", got, err)
				}
			} else {
				if err != nil || got.Rejection == nil || got.Work != nil {
					t.Fatalf("rejected: %+v %v", got, err)
				}
				if kind == "offline" {
					if len(got.Rejection.Unavailable) != 2 || got.Rejection.FailureCode != constants.DeviceOffline {
						t.Fatalf("device details: %+v", got.Rejection)
					}
				} else if got.Rejection.Validation == nil || got.Rejection.Validation.Status != validation.StatusInvalid {
					t.Fatalf("missing diagnostics: %+v", got.Rejection)
				}
			}
			requireCounts(t, f, 0, 0, 0)
		})
	}
}
func TestSQLiteConcurrentAdmission(t *testing.T) {
	for _, sameID := range []bool{true, false} {
		t.Run(fmt.Sprint(sameID), func(t *testing.T) {
			f := fixture(t, 1)
			entered, proceed := make(chan struct{}, 2), make(chan struct{})
			repo := wrappedRepository(f)
			repo.lookup = func(ctx context.Context, id string) (string, error) {
				found, err := f.repo.LookupRequest(ctx, id)
				entered <- struct{}{}
				select {
				case <-proceed:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return found, err
			}
			f.deps.Repository = repo
			s := service(t, f.deps)
			second := f.req
			if !sameID {
				second.RequestID = uuid.NewString()
			}
			type reply struct {
				result runadmission.Result
				err    error
			}
			replies := make(chan reply, 2)
			for _, req := range []runadmission.Request{f.req, second} {
				go func() { out, err := s.Admit(t.Context(), req); replies <- reply{out, err} }()
			}
			<-entered
			<-entered
			close(proceed)
			seen := map[constants.AdmissionDisposition]int{}
			var id string
			for range 2 {
				item := <-replies
				if item.err != nil {
					t.Fatal(item.err)
				}
				seen[item.result.Disposition]++
				if item.result.Work != nil {
					id = item.result.RunGroupID
				}
				if item.result.Disposition == constants.AdmissionExisting && item.result.Work != nil {
					t.Fatal("duplicate work")
				}
			}
			other := constants.AdmissionBusy
			if sameID {
				other = constants.AdmissionExisting
			}
			if seen[constants.AdmissionCreated] != 1 || seen[other] != 1 || !execution.ValidID(id) {
				t.Fatalf("concurrent outcomes: %+v", seen)
			}
			requireCounts(t, f, 1, 1, 2)
		})
	}
}
func TestSQLiteStopWaitsForAdmissionAndIncludesItsCommit(t *testing.T) {
	f := fixture(t, 1)
	entered, proceed := make(chan struct{}), make(chan struct{})
	repo := wrappedRepository(f)
	repo.admit = func(ctx context.Context, a execution.Admission) (execution.AdmissionResult, error) {
		close(entered)
		select {
		case <-proceed:
		case <-ctx.Done():
			return execution.AdmissionResult{}, ctx.Err()
		}
		return f.repo.Admit(ctx, a)
	}
	f.deps.Repository = repo
	type reply struct {
		result runadmission.Result
		err    error
	}
	admitted := make(chan reply, 1)
	go func() { out, err := service(t, f.deps).Admit(t.Context(), f.req); admitted <- reply{out, err} }()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
	stopped := make(chan error, 1)
	go func() { stopped <- f.lifetime.Shutdown(ctx) }()
	select {
	case err := <-stopped:
		t.Fatalf("shutdown passed held lease: %v", err)
	default:
	}
	close(proceed)
	out := <-admitted
	if out.err != nil || out.result.Work == nil {
		t.Fatalf("committed handoff lost: %+v %v", out.result, out.err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.repo.SnapshotByID(t.Context(), out.result.RunGroupID)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled {
		t.Fatalf("admission escaped settlement: %+v %v", snapshot, err)
	}
	if _, err := f.lifetime.Run(t.Context(), out.result.Work.Runner, out.result.Work.Devices[0]); !errors.Is(err, xerror.ErrExecutionStopping) {
		t.Fatalf("late dispatch: %v", err)
	}
	requireCounts(t, f, 1, 1, 4)
}
func TestSQLiteStopDuringPreflightRejectsBeforeAdmission(t *testing.T) {
	f := fixture(t, 1)
	f.deps.Preflight = preflightFunc(func(_ context.Context, serials []string) (services.DeviceSelectionResult, error) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
		return services.DeviceSelectionResult{AcceptedSerials: slices.Clone(serials)}, nil
	})
	got, err := service(t, f.deps).Admit(t.Context(), f.req)
	if !errors.Is(err, xerror.ErrExecutionStopping) || got != (runadmission.Result{}) {
		t.Fatalf("stopping gate: %+v %v", got, err)
	}
	requireCounts(t, f, 0, 0, 0)
}
func TestSQLiteConfirmedCommitSurvivesRequestCancellation(t *testing.T) {
	f := fixture(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repo := wrappedRepository(f)
	repo.admit = func(ctx context.Context, a execution.Admission) (execution.AdmissionResult, error) {
		out, err := f.repo.Admit(ctx, a)
		cancel()
		return out, err
	}
	f.deps.Repository = repo
	out, err := service(t, f.deps).Admit(ctx, f.req)
	if err != nil || out.Work == nil || ctx.Err() == nil {
		t.Fatalf("commit replaced with cancellation: %+v %v", out, err)
	}
	requireCounts(t, f, 1, 1, 2)
}
func TestSQLiteAdmissionStatementFailuresRollback(t *testing.T) {
	for _, table := range []string{"device_executions", "execution_events"} {
		t.Run(table, func(t *testing.T) {
			f := fixture(t, 1)
			if _, err := f.store.DB().ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER reject_admission BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'controlled admission fault'); END", table)); err != nil {
				t.Fatal(err)
			}
			got, err := service(t, f.deps).Admit(t.Context(), f.req)
			if !errors.Is(err, xerror.ErrExecutionPersistence) || got != (runadmission.Result{}) {
				t.Fatalf("rollback: %+v %v", got, err)
			}
			requireCounts(t, f, 0, 0, 0)
		})
	}
}
func TestSQLiteUnknownCommitNeverProducesWork(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			f := fixture(t, 1)
			if !committed {
				if _, err := f.store.DB().ExecContext(t.Context(), "CREATE TRIGGER rollback_admission BEFORE INSERT ON execution_events BEGIN SELECT RAISE(ABORT, 'controlled unknown rollback'); END"); err != nil {
					t.Fatal(err)
				}
			}
			repo := wrappedRepository(f)
			writes := 0
			repo.admit = func(ctx context.Context, a execution.Admission) (execution.AdmissionResult, error) {
				writes++
				_, err := f.repo.Admit(ctx, a)
				if committed && err != nil {
					return execution.AdmissionResult{}, err
				}
				if !committed && !errors.Is(err, xerror.ErrExecutionPersistence) {
					return execution.AdmissionResult{}, fmt.Errorf("expected controlled rollback, got %v", err)
				}
				return execution.AdmissionResult{}, xerror.ErrExecutionCommitUnknown
			}
			f.deps.Repository = repo
			s := service(t, f.deps)
			out, err := s.Admit(t.Context(), f.req)
			if !errors.Is(err, xerror.ErrExecutionCommitUnknown) || out != (runadmission.Result{}) || writes != 1 {
				t.Fatalf("uncertain outcome: %+v writes=%d %v", out, writes, err)
			}
			if committed {
				replay, err := s.Admit(t.Context(), runadmission.Request{RequestID: f.req.RequestID})
				if err != nil || replay.Work != nil || replay.Disposition != constants.AdmissionExisting || writes != 1 {
					t.Fatalf("uncertainty replay: %+v %v", replay, err)
				}
				requireCounts(t, f, 1, 1, 2)
			} else {
				requireCounts(t, f, 0, 0, 0)
				if _, err := f.store.DB().ExecContext(t.Context(), "DROP TRIGGER rollback_admission"); err != nil {
					t.Fatal(err)
				}
			}
			// Reopen the same file, as a process restart would; never reconstruct work.
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			f.lifetime.PrepareStop(constants.ExecutionStopFailure, deadline)
			if err := f.lifetime.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.checkpoints.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			f.store = sqlitestore.New()
			if err := f.store.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.repo = repositories.NewExecutionRepository(f.store)
			recovery := services.NewExecutionRecovery(f.repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := recovery.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			if committed {
				id, err := f.repo.LookupRequest(t.Context(), f.req.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := f.repo.SnapshotByID(t.Context(), id)
				if err != nil || snapshot.RunGroup.Status != constants.ExecutionInterrupted {
					t.Fatalf("restart evidence: %+v %v", snapshot, err)
				}
				requireCounts(t, f, 1, 1, 4)
			} else {
				requireCounts(t, f, 0, 0, 0)
			}
		})
	}
}

func TestSQLiteCancellationBeforeCommitRollsBackAndReleasesLease(t *testing.T) {
	f := fixture(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repo := wrappedRepository(f)
	repo.admit = func(ctx context.Context, a execution.Admission) (execution.AdmissionResult, error) {
		cancel()
		return f.repo.Admit(ctx, a)
	}
	f.deps.Repository = repo
	out, err := service(t, f.deps).Admit(ctx, f.req)
	if !errors.Is(err, context.Canceled) || out != (runadmission.Result{}) {
		t.Fatalf("cancelled admission: %+v %v", out, err)
	}
	stop(t, f)
	requireCounts(t, f, 0, 0, 0)
}
func TestSQLiteTerminalReplayWhileAnotherRunIsActive(t *testing.T) {
	f := fixture(t, 1)
	s := service(t, f.deps)
	first, err := s.Admit(t.Context(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit worker invocation only establishes terminal history for this fixture.
	// The controlled handler fails; admission itself never invokes it.
	terminal, err := f.lifetime.Run(t.Context(), first.Work.Runner, first.Work.Devices[0])
	if err != nil || terminal.DeviceStatus != constants.ExecutionFailed {
		t.Fatalf("terminal setup: %+v %v", terminal, err)
	}
	f.calls.Store(0)
	next := f.req
	next.RequestID = uuid.NewString()
	second, err := s.Admit(t.Context(), next)
	if err != nil || second.Work == nil || first.RunGroupID == second.RunGroupID || first.Work.Devices[0].DeviceExecutionID == second.Work.Devices[0].DeviceExecutionID {
		t.Fatalf("fresh identity: %+v %v", second, err)
	}
	replay, err := s.Admit(t.Context(), runadmission.Request{RequestID: f.req.RequestID})
	if err != nil || replay.Work != nil || replay.Snapshot == nil || replay.Snapshot.RunGroup.Status != constants.ExecutionFailed || replay.RunGroupID != first.RunGroupID {
		t.Fatalf("terminal replay: %+v %v", replay, err)
	}
	active, err := f.repo.ActiveSnapshot(t.Context())
	if err != nil || active.RunGroup.ID != second.RunGroupID || f.calls.Load() != 0 {
		t.Fatalf("replay disturbed active run: %+v %v", active, err)
	}
}
