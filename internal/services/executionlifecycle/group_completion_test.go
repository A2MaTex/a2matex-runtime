package executionlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func TestGroupCompletionKeepsSiblingRAMAndJoinsFatalCallbacks(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(fmt.Sprint(fatal), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				id, devices := groupDevices(3)
				release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
				defer releaseGroupBarriers(release)
				entered := make(chan string, len(devices))
				cause := fmt.Errorf("%w: %w", xerror.ErrCheckpointFatal, xerror.ErrExecutionCommitUnknown)
				l, worker, _ := managedWorkWithCheckpoints(t, groupCheckpoints{finish: func(ctx context.Context, req execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
					entered <- req.DeviceExecutionID
					for i, device := range devices {
						if device.DeviceExecutionID == req.DeviceExecutionID {
							<-release[i]
							if fatal && i == 1 {
								return execution.CheckpointResult{}, cause
							}
						}
					}
					return (checkpoints{}).FinishAttempt(ctx, req)
				}})
				if err := l.DispatchGroup(id, worker, devices); err != nil {
					t.Fatal(err)
				}
				for range devices {
					<-entered
				}
				l.mu.Lock()
				g := l.group
				active := len(g.queue.active)
				l.mu.Unlock()
				if active != len(devices) {
					t.Fatal("not all members reserved", active)
				}
				store := g.store
				writeGroup(t, g, 0, constants.VariableScopeRunGroup, "42")
				for step, index := range []int{1, 0} {
					close(release[index])
					synctest.Wait()
					l.mu.Lock()
					active, failure, next := len(g.queue.active), l.failure, g.queue.next
					l.mu.Unlock()
					if active != len(devices)-step-1 || next != len(devices) || fatal != errors.Is(failure, cause) {
						t.Fatal("incorrect completion/failure fence", active, failure, next)
					}
					select {
					case <-g.done:
						t.Fatal("retired before all callbacks returned")
					default:
					}
					value, found, err := store.Get(t.Context(), devices[2].DeviceExecutionID, constants.VariableScopeRunGroup, "value")
					if err != nil || !found || string(value) != "42" {
						t.Fatal("sibling RAM was closed", err)
					}
				}
				close(release[2])
				<-g.done
				if _, _, err := store.Get(t.Context(), devices[2].DeviceExecutionID, constants.VariableScopeFlow, "value"); !errors.Is(err, xerror.ErrVariableUnavailable) {
					t.Fatal("RAM not closed after final join", err)
				}
				l.mu.Lock()
				retired := l.group == nil && len(l.workers) == 0 && len(g.queue.active) == 0
				l.mu.Unlock()
				if !retired {
					t.Fatal("ownership not retired")
				}
				l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(time.Second))
				if err := l.Shutdown(t.Context()); (fatal && !errors.Is(err, cause)) || (!fatal && err != nil) {
					t.Fatal("first infrastructure cause lost", err)
				}
			})
		})
	}
}

// Cancel at an owner-context check while the real initial producer is registering.
type registrationStopContext struct {
	context.Context
	remaining atomic.Int32
	cancel    context.CancelFunc
}

func (c *registrationStopContext) Err() error {
	if c.remaining.Load() > 0 && c.remaining.Add(-1) == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestGroupRegistrationOwnerLossRetiresZeroAndPartialWork(t *testing.T) {
	for _, stopAt := range []int32{1, 4} {
		t.Run(fmt.Sprint(stopAt), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base, cancel := context.WithCancel(t.Context())
				defer cancel()
				owner := &registrationStopContext{Context: base, cancel: cancel}
				l := New(services.NewCheckpointService(nil, nil))
				if err := l.Start(owner); err != nil {
					t.Fatal(err)
				}
				_, worker, _ := managedWork(t)
				id, devices := groupDevices(64)
				g, err := newGroup(owner, id, worker, devices)
				if err != nil {
					t.Fatal(err)
				}
				l.mu.Lock()
				l.group = g
				l.mu.Unlock()
				owner.remaining.Store(stopAt)
				if err := l.startGroup(g); err != nil {
					t.Fatal(err)
				}
				<-g.done
				l.mu.Lock()
				selected, failure := g.queue.next, l.failure
				retired := l.group == nil && len(l.workers) == 0 && len(g.queue.active) == 0
				l.mu.Unlock()
				if !retired || failure != nil || selected >= len(devices) || (stopAt == 1 && selected != 0) || (stopAt != 1 && selected == 0) {
					t.Fatal("registration crossed owner-loss fence", selected, failure, retired)
				}
				l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(time.Second))
				if err := l.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestGroupRejectsAlreadyRegisteredLaterMember(t *testing.T) {
	l, worker, _ := managedWork(t)
	id, devices := groupDevices(3)
	last := devices[len(devices)-1].DeviceExecutionID
	l.workers[last] = func() {}
	if err := l.DispatchGroup(id, worker, devices); !errors.Is(err, xerror.ErrExecutionStaleState) {
		t.Fatal("later registered identity accepted", err)
	}
	if l.group != nil || len(l.workers) != 1 || l.failure != nil {
		t.Fatal("rejection changed existing ownership")
	}
	delete(l.workers, last)
}

func releaseGroupBarriers(channels []chan struct{}) {
	for _, ch := range channels {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func awaitGroupValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("group did not reach the controlled boundary")
		var zero T
		return zero
	}
}

// Observe real D4 terminal commits without replacing the checkpoint implementation.
type completionCheckpoints struct {
	attempts.Checkpoints
	terminal chan string
}

func (c completionCheckpoints) FinishAttempt(ctx context.Context, req execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	result, err := c.Checkpoints.FinishAttempt(ctx, req)
	if err == nil && req.NextNodeID == "" {
		c.terminal <- req.DeviceExecutionID
	}
	return result, err
}

type groupTestOptions struct {
	wrap         func(*repositories.ExecutionRepository) execution.CheckpointRepository
	expectedStop error
}

func sqliteGroup(t *testing.T, count int, handler registry.Handler, options ...groupTestOptions) (*Service, *runner.Runner, []runner.Request, *repositories.ExecutionRepository, <-chan string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewExecutionRepository(store)
	var option groupTestOptions
	if len(options) > 0 {
		option = options[0]
	}
	var writerRepository execution.CheckpointRepository = repo
	if option.wrap != nil {
		writerRepository = option.wrap(repo)
	}
	cp := services.NewCheckpointService(writerRepository, nil)
	l := New(cp)
	if err := l.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(2*time.Second))
		if err := l.Shutdown(ctx); !errors.Is(err, option.expectedStop) {
			t.Error(err)
		}
		if err := cp.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := store.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	id, devices := groupDevices(count)
	admission := execution.Admission{RequestID: uuid.NewString(), RunGroupID: id, Now: time.Now().UTC()}
	for i := range devices {
		devices[i].WorkflowInputs = ir.Object{"duration": json.RawMessage(fmt.Sprint(i + 1))}
		admission.Devices = append(admission.Devices, execution.AdmissionDevice{DeviceExecutionID: devices[i].DeviceExecutionID, ADBSerial: devices[i].ADBSerial, EntryNodeID: "delay"})
	}
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	terminal := make(chan string, count)
	writer := completionCheckpoints{Checkpoints: cp, terminal: terminal}
	doc := &ir.Document{EntryNodeID: "delay", Nodes: []ir.Node{
		{ID: "delay", Type: "flow.delay", RuntimeHandler: "flow.delay", Config: ir.Object{},
			Inputs: ir.Object{"durationMs": json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"duration"}`)}, TimeoutMS: 10_000,
			RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
		{ID: "end", Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Config: ir.Object{}, Inputs: ir.Object{}, TimeoutMS: 10_000,
			RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
	}, Edges: []ir.Edge{{ID: "next", SourceNodeID: "delay", SourcePort: "success", TargetNodeID: "end"}}}
	worker := lifecycleRunner(t, writer, doc, []registry.Registration{
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: handler},
		{Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Run: end.Invoke},
	})
	return l, worker, devices, repo, terminal
}

func TestGroupSQLiteConcurrentOutcomesAndCompleteHistory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		fail  bool
	}{{"success", 3, false}, {"isolated failure", 3, true}, {"large membership", 64, false}} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan registry.Invocation, tc.count)
			release := make([]chan struct{}, tc.count)
			for i := range release {
				release[i] = make(chan struct{})
			}
			defer releaseGroupBarriers(release)
			l, worker, devices, repo, terminal := sqliteGroup(t, tc.count, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				var duration int
				if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil {
					return nil, err
				}
				entered <- call
				select {
				case <-release[duration-1]:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if tc.fail && duration == 2 {
					return nil, xerror.CommandError{Code: constants.CommandFailed, Message: "controlled workflow failure"}
				}
				return map[string]any{"durationMs": duration}, nil
			})
			if err := l.DispatchGroup(devices[0].RunGroupID, worker, devices); err != nil {
				t.Fatal(err)
			}
			for range devices {
				call := awaitGroupValue(t, entered)
				var duration int
				if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil || call.ADBSerial != devices[duration-1].ADBSerial {
					t.Fatal("serial/input isolation lost", call.ADBSerial, err)
				}
			}
			l.mu.Lock()
			g := l.group
			l.mu.Unlock()
			close(release[1])
			if id := awaitGroupValue(t, terminal); id != devices[1].DeviceExecutionID {
				t.Fatal("wrong first completion", id)
			}
			partial, err := repo.SnapshotByID(t.Context(), g.id)
			if err != nil || partial.RunGroup.Status != constants.ExecutionRunning || partial.RunGroup.Summary.Running != tc.count-1 {
				t.Fatal("early group terminal state", partial, err)
			}
			releaseGroupBarriers(release)
			for range tc.count - 1 {
				awaitGroupValue(t, terminal)
			}
			awaitGroupValue(t, g.done)
			final, err := repo.SnapshotByID(t.Context(), g.id)
			want, failed := constants.ExecutionSucceeded, 0
			if tc.fail {
				want, failed = constants.ExecutionFailed, 1
			}
			if err != nil || final.RunGroup.Status != want || final.RunGroup.Summary.Total != tc.count || final.RunGroup.Summary.Failed != failed ||
				final.RunGroup.Summary.Succeeded != tc.count-failed || final.RunGroup.FinishedAt == nil {
				t.Fatal("incorrect complete-group aggregation", final, err)
			}
			for i, device := range devices {
				page, err := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
				want := 2
				if tc.fail && i == 1 {
					want = 1
				}
				if err != nil || len(page.Attempts) != want || page.Attempts[len(page.Attempts)-1].Sequence != int64(want) {
					t.Fatal("device history lost or redispatched", page, err)
				}
			}
			var after int64
			finished := 0
			for {
				page, err := repo.ReplayEvents(t.Context(), after, 0)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range page.Events {
					var payload struct{ Status string }
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					if event.Type == constants.EventRunGroupStateChanged && payload.Status == want {
						finished++
					}
					after, _ = execution.ParseEventCursor(event.Cursor)
				}
				if !page.More {
					break
				}
			}
			if finished != 1 || l.failure != nil {
				t.Fatal("ordinary outcome fenced Runtime or duplicated terminal event", finished, l.failure)
			}
		})
	}
}

func TestGroupIntentionalStopJoinsAndCancelsEverySQLiteMember(t *testing.T) {
	entered := make(chan string, 3)
	l, worker, devices, repo, _ := sqliteGroup(t, 3, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		entered <- call.ADBSerial
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err := l.DispatchGroup(devices[0].RunGroupID, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	l.PrepareStop(constants.ExecutionStopIntentional, time.Now().Add(2*time.Second))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := l.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), devices[0].RunGroupID)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || snapshot.RunGroup.Summary.Cancelled != len(devices) {
		t.Fatal("incomplete intentional settlement", snapshot, err)
	}
	for _, device := range devices {
		page, err := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
		if err != nil || len(page.Attempts) != 1 || page.Attempts[0].Status != constants.ExecutionCancelled {
			t.Fatal("lost attempt or dispatched successor after stop", page, err)
		}
	}
}

func TestGroupFatalFenceRetiresUnstartedWork(t *testing.T) {
	l, worker, _ := managedWork(t)
	id, devices := groupDevices(3)
	g, err := newGroup(t.Context(), id, worker, devices)
	if err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.group = g
	l.mu.Unlock()
	cause := fmt.Errorf("%w: %w", xerror.ErrCheckpointFatal, xerror.ErrExecutionCommitUnknown)
	l.Fail(cause)
	if err := l.startGroup(g); !errors.Is(err, cause) {
		t.Fatal("dispatch lost the existing failure", err)
	}
	awaitGroupValue(t, g.done)
	l.mu.Lock()
	retired := g.queue.next == 0 && l.group == nil && len(l.workers) == 0 && len(g.queue.active) == 0
	l.mu.Unlock()
	if !retired {
		t.Fatal("fatal fence allowed dispatch or abandoned group retirement")
	}
	l.PrepareStop(constants.ExecutionStopFailure, time.Now().Add(time.Second))
	if err := l.Shutdown(t.Context()); !errors.Is(err, cause) {
		t.Fatal("shutdown lost the first failure", err)
	}
}

func TestGroupStopBeforeCommittedHandoffSettlesEveryMember(t *testing.T) {
	l, worker, devices, repo, _ := sqliteGroup(t, 2, func(context.Context, registry.Invocation) (map[string]any, error) {
		t.Error("handoff dispatched after intentional stop")
		return nil, nil
	})
	release, err := l.BeginAdmission(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	deadline := time.Now().Add(time.Second)
	l.PrepareStop(constants.ExecutionStopIntentional, deadline)
	finished := make(chan error, 1)
	go func() { finished <- l.Shutdown(t.Context()) }()
	// The fixture has committed admission; its still-held lease represents the handoff gap.
	if err := l.DispatchGroup(devices[0].RunGroupID, worker, devices); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		t.Fatal("shutdown bypassed admitted handoff lease", err)
	default:
	}
	before, err := repo.SnapshotByID(t.Context(), devices[0].RunGroupID)
	if err != nil || before.RunGroup.Summary.Queued != 2 {
		t.Fatal("settled before lease drained", before, err)
	}
	release()
	if err := awaitGroupValue(t, finished); err != nil {
		t.Fatal(err)
	}
	after, err := repo.SnapshotByID(t.Context(), devices[0].RunGroupID)
	if err != nil || after.RunGroup.Summary.Cancelled != 2 {
		t.Fatal("queued selection escaped settlement", after, err)
	}
	for _, device := range after.DeviceExecutions {
		page, err := repo.Attempts(t.Context(), device.ID, 0, 0)
		if err != nil || len(page.Attempts) != 0 || device.StartedAt != nil {
			t.Fatal("fabricated queued progress", device, page, err)
		}
	}
}
