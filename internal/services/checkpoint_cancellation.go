package services

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// CancelUnfinished requires accepted cancellation and joined/fenced execution work.
// The caller supplies the supervisor's bounded completion context.
func (s *CheckpointService) CancelUnfinished(ctx context.Context, request execution.CancellationRequest) (execution.CancellationResult, error) {
	if _, bounded := ctx.Deadline(); !bounded || request.Now.IsZero() {
		return execution.CancellationResult{}, xerror.WithOperation("cancel_unfinished_execution", xerror.ErrExecutionInvalidInput)
	}
	if err := s.lockWriter(ctx, true); err != nil {
		return execution.CancellationResult{}, err
	}
	defer func() { <-s.gate }()
	var result execution.CancellationResult
	err := s.repository.WithCancellationTx(ctx, func(tx execution.CancellationTx) error {
		state, err := tx.ReadCancellationState(ctx)
		if err != nil {
			return err
		}
		write, events, err := cancellationPlan(state, request.Now.UTC())
		if err != nil || write.RunGroupID == "" {
			return err
		}
		if err := tx.CancelUnfinished(ctx, write); err != nil {
			return err
		}
		cursor, err := execution.AppendEventBatches(ctx, tx, events)
		if err != nil {
			return err
		}
		result = execution.CancellationResult{RunGroupID: write.RunGroupID, Changed: true, Cursor: cursor}
		return nil
	})
	if err != nil {
		return execution.CancellationResult{}, s.fail(xerror.WithOperation("cancel_unfinished_execution", err))
	}
	return result, nil
}

func cancellationPlan(state execution.RecoveryState, now time.Time) (execution.CancellationWrite, []execution.PendingEvent, error) {
	if state.Status == "" && len(state.Attempts) == 0 {
		return execution.CancellationWrite{}, nil, nil
	}
	fail := func() (execution.CancellationWrite, []execution.PendingEvent, error) {
		return execution.CancellationWrite{}, nil, xerror.ErrRecoveryIntegrity
	}
	if !execution.ValidID(state.RunGroupID) || (state.Status != constants.ExecutionRunning && state.Status != constants.ExecutionPaused) {
		return fail()
	}
	devices, earliest, err := recoverableDevices(state)
	if err != nil {
		return fail()
	}
	for _, attempt := range state.Attempts {
		device, found := devices[attempt.DeviceID]
		if !found || device.Status != constants.ExecutionRunning || device.NextNodeID != attempt.NodeID ||
			!attempt.Latest || !execution.ValidID(attempt.ID) || attempt.Number < 1 || attempt.Number > constants.MaxNodeAttemptNumber ||
			device.StartedAt == nil || attempt.StartedAt < *device.StartedAt {
			return fail()
		}
		earliest = max(earliest, attempt.StartedAt)
	}
	if now.UnixMilli() < earliest {
		now = time.UnixMilli(earliest).UTC()
	}
	write := execution.CancellationWrite{RunGroupID: state.RunGroupID, Now: now, Devices: len(devices), Attempts: len(state.Attempts)}
	slices.SortFunc(state.Attempts, func(a, b execution.RecoveryAttempt) int { return strings.Compare(a.ID, b.ID) })
	ids := make([]string, 0, len(devices))
	for id := range devices {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	events := make([]execution.PendingEvent, 0, len(ids)+len(state.Attempts)+1)
	for _, attempt := range state.Attempts {
		event, err := attemptCheckpointEvent(execution.AttemptRef{RunGroupID: state.RunGroupID, DeviceExecutionID: attempt.DeviceID, NodeID: attempt.NodeID, AttemptID: attempt.ID}, now,
			attemptEventPayload{Status: constants.ExecutionCancelled, AttemptNumber: attempt.Number})
		if err != nil {
			return execution.CancellationWrite{}, nil, err
		}
		events = append(events, event)
	}
	for _, id := range ids {
		event, err := deviceCheckpointEvent(execution.DeviceCheckpoint{AttemptRef: execution.AttemptRef{RunGroupID: state.RunGroupID, DeviceExecutionID: id}, Status: constants.ExecutionCancelled, Now: now})
		if err != nil {
			return execution.CancellationWrite{}, nil, err
		}
		events = append(events, event)
	}
	event, err := groupStateEvent(state.RunGroupID, now, constants.ExecutionCancelled)
	return write, append(events, event), err
}
