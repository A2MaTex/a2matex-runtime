// Package attempts coordinates one durable node attempt without running a workflow loop.
package attempts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/routing"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Checkpoints interface {
	BeginAttempt(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error)
	FinishAttempt(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error)
}

type Dependencies struct {
	Bindings    *bindings.Resolver
	Invoker     *invocation.Invoker
	Outputs     *outputs.Validator
	Routes      *routing.Router
	Checkpoints Checkpoints
	Artifacts   ScreenshotArtifacts
	Now         func() time.Time
}

// Control is a trusted, nonblocking lifecycle snapshot, supplied by the owner.
type Control struct {
	GroupCancellation bool
	ShutdownDeadline  time.Time
}

type Request struct {
	BeforeBegin          func(context.Context, int64) (bool, error)
	BeforeInvoke         func(context.Context, int64) error
	Ref                  execution.AttemptRef
	Node                 ir.Node
	ExpectedDeviceStatus string
	AttemptNumber        int
	PreviousAttemptID    string
	ADBSerial            string
	Values               bindings.Values
	VariableStore        *variables.Store
	Control              func() Control
}

// Result releases continuation data only after this call confirms both checkpoints.
// An error after begin preserves only Began and Sequence for lifecycle settlement.
type Result struct {
	Parked     bool
	Began      bool
	Finished   bool
	Sequence   int64
	Cursor     string
	Outcome    outcomes.Result
	NextNodeID string
	Retry      *execution.RetryDecision
}

type Coordinator struct{ dependencies Dependencies }

func New(dependencies Dependencies) (*Coordinator, error) {
	if !dependencies.available() {
		return nil, &xerror.OperationError{Operation: "configure_node_attempt", Detail: "attempt_dependencies_unavailable", Err: xerror.ErrAttemptInvalid}
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	return &Coordinator{dependencies: dependencies}, nil
}

// Run prepares, begins, invokes at most once, and finishes one admitted node.
// The caller keeps the node and execution-local values immutable during the call.
func (c *Coordinator) Run(ctx context.Context, request Request) (Result, error) {
	if err := entryError(ctx, request); err != nil {
		return Result{}, err
	}
	if c == nil || !c.dependencies.available() {
		return Result{}, attemptError("attempt_dependencies_unavailable", xerror.ErrAttemptInvalid)
	}
	if !validRequest(request) {
		return Result{}, attemptError("attempt_request_invalid", xerror.ErrAttemptInvalid)
	}
	switch request.Node.Type {
	case constants.WaitVariableNodeType, constants.LogNodeType:
		return Result{}, attemptError("attempt_node_unsupported", xerror.ErrAttemptInvalid)
	}
	if request.Node.Type == constants.SetVariableNodeType && request.VariableStore == nil {
		return Result{}, attemptError("variable_write_authority_unavailable", xerror.ErrVariableInvalid)
	}
	if request.Node.Type == constants.ScreenshotNodeType && c.dependencies.Artifacts == nil {
		return Result{}, screenshotGuard("screenshot_store_unavailable")
	}
	var inputs ir.Object
	var stageError error
	if request.Node.Type != constants.SwitchNodeType {
		inputs, stageError = c.dependencies.Bindings.ResolveInputs(ctx, request.Node, request.Values)
	}
	if err := entryError(ctx, request); err != nil {
		return Result{}, err
	}
	if request.BeforeBegin != nil {
		allowed, err := request.BeforeBegin(ctx, request.Node.TimeoutMS)
		if err != nil {
			return Result{}, err
		}
		if !allowed {
			return Result{Parked: true}, nil
		}
	}
	started := c.dependencies.Now().UTC()
	if started.IsZero() {
		return Result{}, attemptError("attempt_request_invalid", xerror.ErrAttemptInvalid)
	}
	begin, err := c.dependencies.Checkpoints.BeginAttempt(ctx, execution.BeginAttemptRequest{
		AttemptRef: request.Ref, ExpectedDeviceStatus: request.ExpectedDeviceStatus,
		AttemptNumber: request.AttemptNumber, MaxAttempts: int(request.Node.RetryPolicy.MaxAttempts),
		PreviousAttemptID: request.PreviousAttemptID, Now: started,
	})
	if err != nil {
		return Result{}, xerror.WithOperation("run_node_attempt", err)
	}
	if err := confirmed(begin, 0, "attempt_begin_unconfirmed"); err != nil {
		return Result{}, err
	}
	active := Result{Began: true, Sequence: begin.Sequence}
	if request.BeforeInvoke != nil {
		if err := request.BeforeInvoke(ctx, request.Node.TimeoutMS); err != nil {
			return active, err
		}
	}
	var shot *screenshotStage
	if request.Node.Type == constants.ScreenshotNodeType {
		shot = &screenshotStage{store: c.dependencies.Artifacts, attemptID: request.Ref.AttemptID}
		defer shot.cleanup(ctx, request)
	}
	var observation invocation.Observation
	control := snapshot(request)
	if stageError == nil && contextError(ctx) == nil && !control.GroupCancellation {
		call := registry.Invocation{
			ADBSerial: request.ADBSerial, Config: request.Node.Config, Inputs: inputs,
		}
		if shot != nil {
			call.StageScreenshot = func(attemptCtx context.Context, data []byte) (string, error) {
				return shot.stage(ctx, attemptCtx, data)
			}
		}
		if request.Node.Type == constants.SwitchNodeType {
			call.ResolveInput = func(ctx context.Context, field string) (json.RawMessage, error) {
				return c.dependencies.Bindings.Resolve(ctx, request.Node, field, request.Values)
			}
		}
		observation, stageError = c.dependencies.Invoker.Invoke(ctx, request.Node, call)
	} else if stageError == nil && control.GroupCancellation {
		// Accepted cancellation may arrive before the parent's signal propagates.
		observation.ParentError = context.Canceled
	}
	if shot != nil {
		shot.closed = true
		if stageError == nil && observation.HandlerError == nil && observation.AttemptError == nil && observation.ParentError == nil {
			stageError = shot.failure // An ignored callback failure must not become success.
		}
	}
	finish, outcome, err := c.completion(ctx, request, observation, stageError, started)
	if err == nil && shot != nil && outcome.Status == constants.ExecutionSucceeded {
		if pairingError := shot.validate(outcome.Output); pairingError != nil {
			finish, outcome, err = c.completion(ctx, request, observation, pairingError, started)
		}
	}
	if err != nil {
		return active, err
	}
	var committed execution.CheckpointResult
	if request.Node.Type == constants.SetVariableNodeType && outcome.Status == constants.ExecutionSucceeded {
		committed, finish, outcome, err = c.assign(ctx, request, inputs, observation, started, begin.Sequence, finish, outcome)
	} else {
		if shot != nil {
			shot.attach(&finish)
			shot.submitted = true // D4/D5 now own settlement, including uncertain returns.
		}
		committed, err = c.dependencies.Checkpoints.FinishAttempt(ctx, finish)
	}
	if err != nil {
		return active, xerror.WithOperation("run_node_attempt", err)
	}
	if err := confirmed(committed, begin.Sequence, "attempt_finish_unconfirmed"); err != nil {
		return active, err
	}
	return Result{Began: true, Finished: true, Sequence: committed.Sequence, Cursor: committed.Cursor,
		Outcome: outcome, NextNodeID: finish.NextNodeID, Retry: finish.Retry}, nil
}

func (d Dependencies) available() bool {
	return d.Bindings != nil && d.Invoker != nil && d.Outputs != nil && d.Routes != nil && d.Checkpoints != nil
}

func validRequest(r Request) bool {
	p := r.Node.RetryPolicy
	if !execution.ValidID(r.Ref.RunGroupID) || !execution.ValidID(r.Ref.DeviceExecutionID) || !execution.ValidID(r.Ref.AttemptID) ||
		!r.Values.Variables.ValidFor(r.Ref.RunGroupID, r.Ref.DeviceExecutionID) ||
		!execution.ValidNodeID(r.Ref.NodeID) || r.Ref.NodeID != r.Node.ID ||
		(r.ExpectedDeviceStatus != constants.ExecutionQueued && r.ExpectedDeviceStatus != constants.ExecutionRunning) ||
		p.MaxAttempts < 1 || p.MaxAttempts > constants.MaxNodeAttemptNumber || p.DelayMS < 0 || p.DelayMS > constants.MaxRetryDelayMS ||
		r.AttemptNumber < 1 || int64(r.AttemptNumber) > p.MaxAttempts {
		return false
	}
	if r.AttemptNumber == 1 {
		return r.PreviousAttemptID == ""
	}
	return execution.ValidID(r.PreviousAttemptID) && r.PreviousAttemptID != r.Ref.AttemptID
}

func confirmed(result execution.CheckpointResult, sequence int64, reason string) error {
	if result.Disposition == constants.CheckpointStale {
		return attemptError(reason, xerror.ErrExecutionStaleState)
	}
	cursor, err := execution.ParseEventCursor(result.Cursor)
	if result.Disposition != constants.CheckpointCommitted || result.Sequence < 1 || err != nil || cursor < 1 ||
		(sequence != 0 && result.Sequence != sequence) {
		return attemptError(reason, xerror.ErrAttemptInvalid)
	}
	return nil
}

func snapshot(request Request) Control {
	if request.Control != nil {
		return request.Control()
	}
	return Control{}
}

func entryError(ctx context.Context, request Request) error {
	control := snapshot(request)
	if err := contextError(ctx); err != nil {
		return xerror.WithOperation("run_node_attempt", err)
	}
	if control.GroupCancellation {
		return attemptError("attempt_cancellation_unsettled", context.Canceled)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func attemptError(reason string, err error) error {
	return &xerror.OperationError{Operation: "run_node_attempt", Detail: reason, Err: err}
}
