package executionlifecycle

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type checkpoints struct{}

func (checkpoints) BeginAttempt(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
	return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "3"}, nil
}
func (checkpoints) FinishAttempt(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "5"}, nil
}

func managedWork(t *testing.T) (*Service, *runner.Runner, runner.Request) {
	t.Helper()
	l := New(services.NewCheckpointService(nil, nil))
	if err := l.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	b, err := bindings.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	o, err := outputs.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	h, err := registry.New(catalog, []registry.Registration{{Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Run: end.Invoke}})
	if err != nil {
		t.Fatal(err)
	}
	i, err := invocation.New(h)
	if err != nil {
		t.Fatal(err)
	}
	w, err := runner.New(t.Context(), &ir.Document{EntryNodeID: "end", Nodes: []ir.Node{{ID: "end", Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType,
		Config: ir.Object{}, Inputs: ir.Object{}, TimeoutMS: 1000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop}}},
		runner.Dependencies{Bindings: b, Outputs: o, Invoker: i, Checkpoints: checkpoints{}})
	if err != nil {
		t.Fatal(err)
	}
	return l, w, runner.Request{RunGroupID: uuid.NewString(), DeviceExecutionID: uuid.NewString(), ADBSerial: "controlled", WorkflowInputs: ir.Object{}}
}

func TestDispatchCleanupPrecedesDrainAndReportsFailure(t *testing.T) {
	for _, name := range []string{"success", "worker failure", "cleanup failure"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, worker, request := managedWork(t)
				if name == "worker failure" {
					worker = &runner.Runner{}
				}
				entered, release := make(chan struct{}), make(chan struct{})
				calls := 0
				err := l.Dispatch(worker, request, func(ctx context.Context) error {
					calls++
					if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
						t.Error("cleanup is not live and bounded")
					}
					close(entered)
					<-release
					if name == "cleanup failure" {
						return xerror.ErrVariableUnavailable
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				<-entered
				l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(time.Second))
				done := make(chan error, 1)
				go func() { done <- l.Shutdown(t.Context()) }()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("drained before cleanup returned")
				default:
				}
				close(release)
				err = <-done
				if calls != 1 || (name == "success") != (err == nil) {
					t.Fatal(calls, err)
				}
				if name == "cleanup failure" && !errors.Is(err, xerror.ErrVariableUnavailable) {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestDispatchStopConsumesUnusedCleanupBeforeLeaseRelease(t *testing.T) {
	l, worker, request := managedWork(t)
	_, release, err := l.BeginStart(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(time.Second))
	calls := 0
	if err := l.Dispatch(worker, request, func(ctx context.Context) error {
		calls++
		if ctx.Err() != nil {
			t.Error(ctx.Err())
		}
		return nil
	}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	select {
	case <-l.drained:
		t.Fatal("start lease released early")
	default:
	}
	release()
	if err := l.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDirectRunRetainsCallerOwnedStore(t *testing.T) {
	l, worker, request := managedWork(t)
	store, err := variables.New(request.RunGroupID, []string{request.DeviceExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	request.VariableStore = store
	if _, err := l.Run(t.Context(), worker, request); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(t.Context(), request.DeviceExecutionID, constants.VariableScopeFlow, "missing"); err != nil || found {
		t.Fatal(found, err)
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
