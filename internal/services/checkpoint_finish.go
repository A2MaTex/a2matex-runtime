package services

import (
	"bytes"
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (s *CheckpointService) FinishAttempt(ctx context.Context, request execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	if !validFinish(request) {
		return execution.CheckpointResult{}, xerror.ErrExecutionInvalidInput
	}
	// The handler's cancellation is itself an outcome to persist. Bound the
	// detached checkpoint and any read-back by the same shutdown-aware deadline.
	deadline := time.Now().Add(constants.CheckpointTimeout)
	if !request.ShutdownDeadline.IsZero() && request.ShutdownDeadline.Before(deadline) {
		deadline = request.ShutdownDeadline
	}
	checkpointCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	return s.write(checkpointCtx, request.AttemptRef, true, func(tx execution.CheckpointTx) (checkpointWrite, error) {
		return finishAttempt(checkpointCtx, tx, request)
	})
}

func finishAttempt(ctx context.Context, tx execution.CheckpointTx, request execution.FinishAttemptRequest) (checkpointWrite, error) {
	state, err := tx.CheckpointState(ctx, request.AttemptRef, request.AttemptID)
	if err != nil {
		return checkpointWrite{}, err
	}
	if !sameAttempt(state.Attempt, request.AttemptRef) {
		return checkpointWrite{}, xerror.ErrExecutionStaleState
	}
	attempt := state.Attempt
	if attempt.Status != constants.ExecutionRunning {
		return checkpointWrite{result: execution.CheckpointResult{
			Disposition: constants.CheckpointAlreadyFinalized, Sequence: attempt.Sequence,
		}}, nil
	}
	if attempt.Sequence != state.LatestSequence || state.GroupStatus != constants.ExecutionRunning ||
		state.DeviceStatus != constants.ExecutionRunning || state.NextNodeID != request.NodeID {
		return checkpointWrite{}, xerror.ErrExecutionStaleState
	}
	if request.Now.UnixMilli() < attempt.StartedAt || (request.Retry != nil && attempt.Number >= request.Retry.MaxAttempts) {
		return checkpointWrite{}, xerror.ErrExecutionInvalidInput
	}
	device := finishDevice(request)
	groupStatus := state.GroupStatus
	if device.Status != constants.ExecutionRunning {
		siblings, err := tx.OtherDeviceStatuses(ctx, request.AttemptRef)
		if err != nil {
			return checkpointWrite{}, err
		}
		groupStatus, err = checkpointGroupStatus(append(siblings, device.Status), request.GroupCancellation)
		if err != nil {
			return checkpointWrite{}, err
		}
	}
	deviceChanged := device.Status != state.DeviceStatus || device.NextNodeID != state.NextNodeID
	groupChanged := groupStatus != state.GroupStatus
	events, err := finishEvents(request, attempt.Number, device, deviceChanged, groupStatus, groupChanged)
	if err != nil {
		return checkpointWrite{}, err
	}
	if err := tx.FinalizeAttempt(ctx, request); err != nil {
		return checkpointWrite{}, err
	}
	if deviceChanged {
		if err := tx.UpdateCheckpointDevice(ctx, device); err != nil {
			return checkpointWrite{}, err
		}
	}
	if groupChanged {
		if err := tx.UpdateCheckpointGroup(ctx, request.RunGroupID, groupStatus, request.Now); err != nil {
			return checkpointWrite{}, err
		}
	}
	return appendCheckpoint(ctx, tx, attempt.Sequence, events, func(durable execution.CheckpointState) bool {
		stored := durable.Attempt
		return sameAttempt(stored, request.AttemptRef) && stored.Status == request.Status &&
			stored.Sequence == attempt.Sequence && durable.LatestSequence == attempt.Sequence &&
			stored.FinishedAt != nil && *stored.FinishedAt == request.Now.UnixMilli() &&
			bytes.Equal(stored.Output, request.Output) && bytes.Equal(stored.Error, request.Error) &&
			durable.DeviceStatus == device.Status && durable.NextNodeID == device.NextNodeID &&
			(!groupChanged || durable.GroupStatus == groupStatus)
	})
}

func validFinish(request execution.FinishAttemptRequest) bool {
	if !validAttemptRef(request.AttemptRef) || request.Now.IsZero() ||
		(request.NextNodeID != "" && !execution.ValidNodeID(request.NextNodeID)) {
		return false
	}
	switch request.Status {
	case constants.ExecutionSucceeded, constants.ExecutionFailed, constants.ExecutionTimedOut:
	case constants.ExecutionCancelled:
		return request.GroupCancellation && request.NextNodeID == "" && request.Retry == nil
	default:
		return false
	}
	if request.GroupCancellation && request.NextNodeID != "" {
		return false
	}
	if request.Retry == nil {
		return request.NextNodeID != request.NodeID
	}
	retry := request.Retry
	return failedAttempt(request.Status) && request.NextNodeID == request.NodeID && execution.RetryableEventError(request.Error) &&
		retry.MaxAttempts > 1 && retry.MaxAttempts <= constants.MaxNodeAttemptNumber &&
		retry.DelayMS >= 0 && retry.DelayMS <= constants.MaxRetryDelayMS
}

func failedAttempt(status string) bool {
	return status == constants.ExecutionFailed || status == constants.ExecutionTimedOut
}

func finishDevice(request execution.FinishAttemptRequest) execution.DeviceCheckpoint {
	device := execution.DeviceCheckpoint{AttemptRef: request.AttemptRef, Status: constants.ExecutionRunning,
		NextNodeID: request.NextNodeID, Now: request.Now}
	if device.NextNodeID == "" {
		device.Status = request.Status
		if failedAttempt(request.Status) {
			device.Status, device.Error = constants.ExecutionFailed, request.Error
		}
	}
	return device
}

func checkpointGroupStatus(statuses []string, cancellation bool) (string, error) {
	var active, paused, failed, cancelled bool
	for _, status := range statuses {
		switch status {
		case constants.ExecutionQueued, constants.ExecutionRunning:
			active = true
		case constants.ExecutionPaused:
			paused = true
		case constants.ExecutionFailed:
			failed = true
		case constants.ExecutionCancelled:
			cancelled = true
		case constants.ExecutionSucceeded:
		default:
			return "", xerror.ErrExecutionPersistence
		}
	}
	switch {
	case active || (paused && cancellation):
		return constants.ExecutionRunning, nil
	case paused:
		return constants.ExecutionPaused, nil
	case cancellation:
		return constants.ExecutionCancelled, nil
	case failed:
		return constants.ExecutionFailed, nil
	case cancelled:
		return "", xerror.ErrExecutionInvalidInput
	default:
		return constants.ExecutionSucceeded, nil
	}
}
