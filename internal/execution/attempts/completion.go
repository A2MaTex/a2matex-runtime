package attempts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/retry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (c *Coordinator) completion(ctx context.Context, request Request, observation invocation.Observation,
	stageError error, started time.Time,
) (execution.FinishAttemptRequest, outcomes.Result, error) {
	var checked json.RawMessage
	if stageError == nil && observation.HandlerError == nil && observation.AttemptError == nil && observation.ParentError == nil {
		checked, stageError = c.dependencies.Outputs.Validate(ctx, request.Node.Type, observation.Output)
	}
	now := c.dependencies.Now().UTC()
	if now.Before(started) {
		now = started
	}
	control := snapshot(request)
	outcome := normalize(ctx, observation, checked, stageError, control)
	if outcome.Status == constants.ExecutionSucceeded && !successFits(request, now, outcome.Output) {
		checked = nil
		stageError = xerror.CommandError{Code: constants.NodeOutputInvalid, Message: "Node output exceeds the execution event limit"}
		outcome = normalize(ctx, observation, nil, stageError, control)
	}
	var next string
	var decision *execution.RetryDecision
	var err error
	if !outcome.Stop {
		decision, err = retry.Decide(ctx, request.Node.RetryPolicy, request.AttemptNumber, outcome)
		if err == nil && decision != nil {
			next = request.Node.ID
		}
		if err == nil && decision == nil {
			route, routeError := c.dependencies.Routes.Select(ctx, request.Node.ID, outcome.Status)
			next, err = route.NextNodeID, routeError
		}
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// A new guard failure must not be combined with an old handler failure.
			observation, checked, stageError = invocation.Observation{}, nil, err
		}
	}
	// Freeze only after sampling the latest authority and parent stop signal.
	control = snapshot(request)
	outcome = normalize(ctx, observation, checked, stageError, control)
	if outcome.Stop {
		next, decision = "", nil
	}
	if outcome.Error != nil && outcome.Error.Code == constants.PersistenceFailed {
		return execution.FinishAttemptRequest{}, outcomes.Result{}, attemptError("attempt_finish_unconfirmed", xerror.ErrExecutionPersistence)
	}
	if outcome.Status == constants.ExecutionCancelled && !control.GroupCancellation {
		return execution.FinishAttemptRequest{}, outcomes.Result{}, attemptError("attempt_cancellation_unsettled", context.Canceled)
	}
	finish := execution.FinishAttemptRequest{AttemptRef: request.Ref, NodeType: request.Node.Type,
		Status: outcome.Status, Output: outcome.Output, NextNodeID: next, Retry: decision,
		GroupCancellation: control.GroupCancellation, Now: now, ShutdownDeadline: control.ShutdownDeadline}
	if outcome.Error != nil {
		finish.Error, err = json.Marshal(outcome.Error)
		if err != nil {
			return execution.FinishAttemptRequest{}, outcomes.Result{}, attemptError("attempt_finish_unconfirmed", xerror.ErrAttemptInvalid)
		}
	}
	return finish, outcome, nil
}

func normalize(ctx context.Context, observation invocation.Observation, checked json.RawMessage, stageError error, control Control) outcomes.Result {
	outcome := outcomes.Normalize(ctx, observation, checked, stageError)
	infrastructure := outcome.Error != nil && (outcome.Error.Code == constants.InternalError || outcome.Error.Code == constants.PersistenceFailed)
	if control.GroupCancellation && !infrastructure {
		return outcomes.Result{Status: constants.ExecutionCancelled, Stop: true}
	}
	return outcome
}

// successFits mirrors D4's success payload without publishing a probe event.
func successFits(request Request, now time.Time, output json.RawMessage) bool {
	payload, err := json.Marshal(struct {
		Status        string          `json:"status"`
		AttemptNumber int             `json:"attemptNumber"`
		Output        json.RawMessage `json:"output"`
	}{constants.ExecutionSucceeded, request.AttemptNumber, output})
	return err == nil && execution.ValidEvent(execution.PendingEvent{Type: constants.EventAttemptStateChanged,
		OccurredAt: now, RunGroupID: request.Ref.RunGroupID, DeviceExecutionID: request.Ref.DeviceExecutionID,
		NodeAttemptID: request.Ref.AttemptID, NodeID: request.Ref.NodeID, Payload: payload})
}
