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
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func groupDevices(count int) (string, []runner.Request) {
	id := uuid.NewString()
	devices := make([]runner.Request, count)
	for i := range devices {
		devices[i] = runner.Request{RunGroupID: id, DeviceExecutionID: uuid.NewString(), ADBSerial: fmt.Sprintf("device-%d", i),
			WorkflowInputs: ir.Object{"input": json.RawMessage(`"original"`)}}
	}
	return id, devices
}

func writeGroup(t *testing.T, g *groupExecution, index int, scope constants.VariableScope, value string) {
	t.Helper()
	_, err := g.store.Apply(t.Context(), execution.VariableWrite{AttemptRef: execution.AttemptRef{
		RunGroupID: g.id, DeviceExecutionID: g.devices[index].DeviceExecutionID, NodeID: "set", AttemptID: uuid.NewString()},
		Scope: scope, Name: "value", Value: []byte(value)}, func(context.Context) (execution.CheckpointResult, error) {
		return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewGroupOwnsOrderedInputsAndIsolatedViews(t *testing.T) {
	for _, count := range []int{1, 128} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			id, devices := groupDevices(count)
			g, err := newGroup(t.Context(), id, &runner.Runner{}, devices)
			if err != nil {
				t.Fatal(err)
			}
			defer g.cancel()
			defer func() {
				if err := g.close(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			for i := range devices {
				owned := g.devices[i]
				if owned.DeviceExecutionID != devices[i].DeviceExecutionID || owned.ADBSerial != devices[i].ADBSerial ||
					owned.VariableStore != g.store || !owned.Variables.ValidFor(id, devices[i].DeviceExecutionID) ||
					devices[i].VariableStore != nil || devices[i].Variables.Lookup != nil {
					t.Fatal("membership or authority mismatch")
				}
				devices[i].WorkflowInputs["input"][1] = 'X'
				devices[i].WorkflowInputs["extra"] = []byte("true")
				devices[i].ADBSerial = "mutated"
				if string(owned.WorkflowInputs["input"]) != `"original"` || len(owned.WorkflowInputs) != 1 || g.devices[i].ADBSerial == "mutated" {
					t.Fatal("caller still owns live inputs")
				}
				writeGroup(t, g, i, constants.VariableScopeFlow, fmt.Sprint(i))
			}
			writeGroup(t, g, count-1, constants.VariableScopeRunGroup, "false")
			for i, device := range g.devices {
				for scope, want := range map[constants.VariableScope]string{constants.VariableScopeFlow: fmt.Sprint(i), constants.VariableScopeRunGroup: "false"} {
					value, found, err := device.Variables.Lookup(t.Context(), scope, "value")
					if err != nil || !found || string(value) != want {
						t.Fatalf("device %d scope %s: %s %v %v", i, scope, value, found, err)
					}
				}
			}
		})
	}
}

func TestNewGroupRejectsInvalidWorkBeforeOwnership(t *testing.T) {
	cases := map[string]func(*string, **runner.Runner, *[]runner.Request){
		"group":  func(id *string, _ **runner.Runner, _ *[]runner.Request) { *id = "invalid" },
		"runner": func(_ *string, worker **runner.Runner, _ *[]runner.Request) { *worker = nil },
		"empty":  func(_ *string, _ **runner.Runner, devices *[]runner.Request) { *devices = nil },
		"mixed group": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].RunGroupID = uuid.NewString()
		},
		"invalid device": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].DeviceExecutionID = "invalid"
		},
		"duplicate device": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].DeviceExecutionID = (*devices)[0].DeviceExecutionID
		},
		"duplicate serial": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].ADBSerial = (*devices)[0].ADBSerial
		},
		"invalid serial": func(_ *string, _ **runner.Runner, devices *[]runner.Request) { (*devices)[1].ADBSerial = "bad serial" },
		"nil inputs":     func(_ *string, _ **runner.Runner, devices *[]runner.Request) { (*devices)[1].WorkflowInputs = nil },
		"control": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].Control = func() attempts.Control { return attempts.Control{} }
		},
		"view identity": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].Variables.DeviceExecutionID = uuid.NewString()
		},
		"lookup": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].Variables.Lookup = func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error) {
				return nil, false, nil
			}
		},
		"store": func(_ *string, _ **runner.Runner, devices *[]runner.Request) {
			(*devices)[1].VariableStore = &variables.Store{}
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			id, devices := groupDevices(2)
			worker := &runner.Runner{}
			corrupt(&id, &worker, &devices)
			if g, err := newGroup(t.Context(), id, worker, devices); g != nil || !errors.Is(err, xerror.ErrExecutionLifecycleInvalid) {
				t.Fatalf("invalid work accepted: %v %v", g, err)
			}
			if len(devices) != 0 && (devices[0].VariableStore != nil || devices[0].Variables.Lookup != nil) {
				t.Fatal("validation mutated prepared work")
			}
		})
	}
}

type groupCheckpoints struct {
	checkpoints
	finish func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error)
}

func (c groupCheckpoints) FinishAttempt(ctx context.Context, req execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	return c.finish(ctx, req)
}

func TestDispatchGroupWaitsForPriorCleanupAndRetainsFreshRAM(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finishing, releaseFinish, secondFinish, releaseSecond := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		l, worker, request := managedWorkWithCheckpoints(t, groupCheckpoints{finish: func(ctx context.Context, req execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
			if calls.Add(1) == 1 {
				close(finishing)
				<-releaseFinish
			} else {
				close(secondFinish)
				<-releaseSecond
			}
			return (checkpoints{}).FinishAttempt(ctx, req)
		}})
		if err := l.DispatchGroup(request.RunGroupID, worker, []runner.Request{request}); err != nil {
			t.Fatal(err)
		}
		<-finishing
		l.mu.Lock()
		g := l.group
		l.mu.Unlock()
		store, view := g.store, g.devices[0].Variables
		if err := l.DispatchGroup(request.RunGroupID, worker, []runner.Request{request}); !errors.Is(err, xerror.ErrExecutionStaleState) {
			t.Fatal("duplicate ownership", err)
		}
		entered, releaseStore, applied := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			_, err := store.Apply(t.Context(), execution.VariableWrite{AttemptRef: execution.AttemptRef{RunGroupID: request.RunGroupID,
				DeviceExecutionID: request.DeviceExecutionID, NodeID: "set", AttemptID: uuid.NewString()}, Scope: constants.VariableScopeRunGroup, Name: "old", Value: []byte("true")},
				func(context.Context) (execution.CheckpointResult, error) {
					close(entered)
					<-releaseStore
					return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
				})
			applied <- err
		}()
		<-entered
		close(releaseFinish)
		id, devices := groupDevices(1)
		next := make(chan error, 1)
		go func() { next <- l.DispatchGroup(id, worker, devices) }()
		synctest.Wait()
		select {
		case err := <-next:
			t.Fatalf("handoff before prior cleanup: %v", err)
		default:
		}
		select {
		case <-g.done:
			t.Fatal("group finished before RAM cleanup")
		default:
		}
		close(releaseStore)
		if err := <-applied; err != nil {
			t.Fatal(err)
		}
		if err := <-next; err != nil {
			t.Fatal(err)
		}
		<-secondFinish
		l.mu.Lock()
		newView, newStore := l.group.devices[0].Variables, l.group.store
		l.mu.Unlock()
		if _, found, err := newView.Lookup(t.Context(), constants.VariableScopeRunGroup, "old"); found || err != nil || newStore == store {
			t.Fatal("new run reused old RAM", found, err)
		}
		close(releaseSecond)
		synctest.Wait()
		if _, _, err := view.Lookup(t.Context(), constants.VariableScopeRunGroup, "old"); !errors.Is(err, xerror.ErrVariableUnavailable) {
			t.Fatal("old RAM not closed", err)
		}
		l.mu.Lock()
		active, failure := l.group, l.failure
		l.mu.Unlock()
		if active != nil || failure != nil || calls.Load() != 2 {
			t.Fatal("ownership did not retire", active, failure, calls.Load())
		}
	})
}

func TestDispatchGroupKnownStopAndEmptySelection(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			l, worker, request := managedWork(t)
			if err := l.DispatchGroup(uuid.NewString(), worker, nil); !errors.Is(err, xerror.ErrExecutionLifecycleInvalid) {
				t.Fatal("empty selection", err)
			}
			if l.group != nil || len(l.workers) != 0 {
				t.Fatal("partial group dispatch")
			}
			if !stopped {
				return
			}
			_, release, err := l.BeginStart(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(time.Second))
			if err := l.DispatchGroup(request.RunGroupID, worker, []runner.Request{request}); err != nil {
				t.Fatal(err)
			}
			if l.group != nil || len(l.workers) != 0 {
				t.Fatal("stop started a worker")
			}
			select {
			case <-l.drained:
				t.Fatal("lease released early")
			default:
			}
			release()
			if err := l.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDispatchGroupShutdownJoinsCleanupAndReportsDeadline(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				finishing, releaseFinish := make(chan struct{}), make(chan struct{})
				l, worker, request := managedWorkWithCheckpoints(t, groupCheckpoints{finish: func(ctx context.Context, req execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
					close(finishing)
					<-releaseFinish
					return (checkpoints{}).FinishAttempt(ctx, req)
				}})
				if err := l.DispatchGroup(request.RunGroupID, worker, []runner.Request{request}); err != nil {
					t.Fatal(err)
				}
				<-finishing
				l.mu.Lock()
				g := l.group
				l.mu.Unlock()
				store := g.store
				entered, releaseStore, applied := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				go func() {
					_, err := store.Apply(t.Context(), execution.VariableWrite{AttemptRef: execution.AttemptRef{RunGroupID: request.RunGroupID,
						DeviceExecutionID: request.DeviceExecutionID, NodeID: "set", AttemptID: uuid.NewString()}, Scope: constants.VariableScopeFlow, Name: "value", Value: []byte("0")},
						func(context.Context) (execution.CheckpointResult, error) {
							close(entered)
							<-releaseStore
							return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
						})
					applied <- err
				}()
				<-entered
				close(releaseFinish)
				synctest.Wait()
				l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(time.Second))
				stopped := make(chan error, 1)
				go func() { stopped <- l.Shutdown(t.Context()) }()
				synctest.Wait()
				select {
				case err := <-stopped:
					t.Fatalf("shutdown skipped cleanup: %v", err)
				default:
				}
				if expire {
					if err := <-stopped; !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("cleanup deadline hidden", err)
					}
				}
				close(releaseStore)
				if err := <-applied; err != nil {
					t.Fatal(err)
				}
				if !expire {
					if err := <-stopped; err != nil {
						t.Fatal(err)
					}
					if _, _, err := store.Get(t.Context(), request.DeviceExecutionID, constants.VariableScopeFlow, "value"); !errors.Is(err, xerror.ErrVariableUnavailable) {
						t.Fatal("drain acknowledged before store closed", err)
					}
				}
				<-g.done
				l.mu.Lock()
				active, failure := l.group, l.failure
				l.mu.Unlock()
				if active != nil || expire != (failure != nil) {
					t.Fatal("invalid cleanup outcome", active, failure)
				}
			})
		})
	}
}
