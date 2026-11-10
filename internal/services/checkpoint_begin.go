package services

import (
	"context"
	"math"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (s *CheckpointService) BeginAttempt(ctx context.Context, request execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
	if !validBegin(request) {
		return execution.CheckpointResult{}, xerror.ErrExecutionInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, constants.CheckpointTimeout)
	defer cancel()
	return s.write(ctx, request.AttemptRef, false, func(tx execution.CheckpointTx) (checkpointWrite, error) {
		state, err := tx.CheckpointState(ctx, request.AttemptRef, "")
		if err != nil {
			return checkpointWrite{}, err
		}
		if state.GroupStatus != constants.ExecutionRunning || state.DeviceStatus != request.ExpectedDeviceStatus ||
			state.NextNodeID != request.NodeID || state.HasActiveAttempt || !validAttemptPredecessor(state.Attempt, request) {
			return checkpointWrite{}, xerror.ErrExecutionStaleState
		}
		if request.Now.UnixMilli() < state.DeviceCreatedAt ||
			(state.Attempt != nil && state.Attempt.FinishedAt != nil && request.Now.UnixMilli() < *state.Attempt.FinishedAt) {
			return checkpointWrite{}, xerror.ErrExecutionInvalidInput
		}
		if state.LatestSequence == math.MaxInt64 {
			return checkpointWrite{}, xerror.ErrExecutionPersistence
		}
		sequence := state.LatestSequence + 1
		if err := tx.InsertAttempt(ctx, request, sequence); err != nil {
			return checkpointWrite{}, err
		}
		events := make([]execution.PendingEvent, 0, 2)
		if state.DeviceStatus == constants.ExecutionQueued {
			if err := tx.StartDevice(ctx, request); err != nil {
				return checkpointWrite{}, err
			}
			event, err := deviceCheckpointEvent(execution.DeviceCheckpoint{
				AttemptRef: request.AttemptRef, Status: constants.ExecutionRunning, NextNodeID: request.NodeID, Now: request.Now,
			})
			if err != nil {
				return checkpointWrite{}, err
			}
			events = append(events, event)
		}
		event, err := attemptCheckpointEvent(request.AttemptRef, request.Now,
			attemptEventPayload{Status: constants.ExecutionRunning, AttemptNumber: request.AttemptNumber})
		if err != nil {
			return checkpointWrite{}, err
		}
		events = append(events, event)
		return appendCheckpoint(ctx, tx, sequence, events, func(durable execution.CheckpointState) bool {
			return sameAttempt(durable.Attempt, request.AttemptRef) && durable.Attempt.Status == constants.ExecutionRunning &&
				durable.Attempt.Sequence == sequence && durable.LatestSequence == sequence &&
				durable.Attempt.Number == request.AttemptNumber && durable.Attempt.StartedAt == request.Now.UnixMilli() &&
				durable.GroupStatus == constants.ExecutionRunning && durable.DeviceStatus == constants.ExecutionRunning &&
				durable.NextNodeID == request.NodeID
		})
	})
}

func validBegin(request execution.BeginAttemptRequest) bool {
	if !validAttemptRef(request.AttemptRef) || request.Now.IsZero() || request.AttemptNumber < 1 ||
		request.AttemptNumber > request.MaxAttempts || request.MaxAttempts > constants.MaxNodeAttemptNumber ||
		(request.ExpectedDeviceStatus != constants.ExecutionQueued && request.ExpectedDeviceStatus != constants.ExecutionRunning) {
		return false
	}
	if request.AttemptNumber == 1 {
		return request.PreviousAttemptID == ""
	}
	return execution.ValidID(request.PreviousAttemptID) && request.PreviousAttemptID != request.AttemptID
}

func validAttemptPredecessor(previous *execution.CheckpointAttempt, request execution.BeginAttemptRequest) bool {
	if request.AttemptNumber == 1 {
		return previous == nil || previous.NodeID != request.NodeID
	}
	return previous != nil && previous.ID == request.PreviousAttemptID && previous.NodeID == request.NodeID &&
		previous.Number+1 == request.AttemptNumber && failedAttempt(previous.Status) && execution.RetryableEventError(previous.Error)
}
