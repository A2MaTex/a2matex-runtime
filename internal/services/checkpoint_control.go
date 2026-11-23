package services

import (
	"context"
	"errors"
	"strconv"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (s *CheckpointService) PauseGroup(ctx context.Context, request execution.GroupControlRequest) (execution.GroupControlResult, error) {
	return s.changeGroup(ctx, request, true)
}

func (s *CheckpointService) ResumeGroup(ctx context.Context, request execution.GroupControlRequest) (execution.GroupControlResult, error) {
	return s.changeGroup(ctx, request, false)
}

func (s *CheckpointService) changeGroup(ctx context.Context, request execution.GroupControlRequest, pause bool) (execution.GroupControlResult, error) {
	result := execution.GroupControlResult{RunGroupID: request.RunGroupID}
	if err := ctx.Err(); err != nil {
		return result, xerror.WithOperation("begin_group_checkpoint", err)
	}
	if !execution.ValidID(request.RunGroupID) || request.Now.IsZero() {
		return result, xerror.WithOperation("validate_group_checkpoint", xerror.ErrExecutionInvalidInput)
	}
	repo, ok := s.repository.(execution.GroupControlRepository)
	if !ok {
		return result, xerror.WithOperation("configure_group_checkpoint", xerror.ErrExecutionLifecycleInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, constants.CheckpointTimeout)
	defer cancel()
	if err := s.lockWriter(ctx, true); err != nil {
		return result, err
	}
	defer func() { <-s.gate }()
	var expected execution.GroupControlState
	err := repo.WithGroupControlTx(ctx, func(tx execution.GroupControlTx) error {
		state, err := tx.GroupControlState(ctx, request.RunGroupID)
		if err != nil {
			return err
		}
		from, to, memberTarget := constants.ExecutionPaused, constants.ExecutionRunning, constants.ExecutionQueued
		if pause {
			from, to, memberTarget = constants.ExecutionRunning, constants.ExecutionPaused, constants.ExecutionPaused
		}
		if state.Status == to && !state.Finished {
			result.Disposition = constants.GroupControlAlreadyTarget
			return nil
		}
		if state.Status != from || state.Finished || !controlEvidenceMatches(state, request.Members, pause) {
			return xerror.ErrExecutionStaleState
		}
		events := make([]execution.PendingEvent, 0, len(request.Members)+1)
		for i := range state.Members {
			m := &state.Members[i]
			if m.Finished {
				continue
			}
			event, err := deviceCheckpointEvent(execution.DeviceCheckpoint{AttemptRef: execution.AttemptRef{
				RunGroupID: request.RunGroupID, DeviceExecutionID: m.DeviceExecutionID}, Status: memberTarget,
				NextNodeID: m.NextNodeID, Now: request.Now})
			if err != nil {
				return err
			}
			events = append(events, event)
			m.Status = memberTarget
		}
		groupEvent, err := groupStateEvent(request.RunGroupID, request.Now, to)
		if err != nil {
			return err
		}
		events = append(events, groupEvent)
		if err := tx.UpdateGroupControl(ctx, request.RunGroupID, pause, len(request.Members)); err != nil {
			return err
		}
		cursor, err := execution.AppendEventBatches(ctx, tx, events)
		if err != nil {
			return err
		}
		state.Status = to
		expected = state
		result.Disposition, result.Changed, result.Cursor = constants.GroupControlCommitted, len(request.Members), cursor
		return nil
	})
	if errors.Is(err, xerror.ErrExecutionNotFound) || errors.Is(err, xerror.ErrExecutionStaleState) {
		result.Disposition = constants.GroupControlStale
		return result, nil
	}
	if errors.Is(err, xerror.ErrExecutionCommitUnknown) {
		if result.Disposition == constants.GroupControlAlreadyTarget {
			return result, nil // The transaction contained no writes.
		}
		actual, readErr := repo.ReadGroupControl(ctx, request.RunGroupID)
		cursor, cursorErr := strconv.ParseInt(result.Cursor, 10, 64)
		if readErr == nil && cursorErr == nil && actual.Watermark >= cursor && sameControlState(actual, expected) {
			repo.NotifyRecoveredCheckpoint()
			return result, nil
		}
	}
	if err != nil {
		return execution.GroupControlResult{}, s.fail(xerror.WithOperation("write_group_checkpoint", err))
	}
	return result, nil
}

func controlEvidenceMatches(state execution.GroupControlState, evidence []execution.Boundary, pause bool) bool {
	if len(evidence) == 0 {
		return false
	}
	wanted := make(map[string]execution.Boundary, len(evidence))
	for _, m := range evidence {
		if !execution.ValidID(m.DeviceExecutionID) || !execution.ValidNodeID(m.NextNodeID) || m.Sequence < 0 ||
			(m.Sequence == 0) != (m.AttemptID == "") || (m.AttemptID != "" && !execution.ValidID(m.AttemptID)) {
			return false
		}
		if _, duplicate := wanted[m.DeviceExecutionID]; duplicate {
			return false
		}
		wanted[m.DeviceExecutionID] = m
	}
	for _, m := range state.Members {
		if m.HasActiveAttempt {
			return false
		}
		if m.Finished {
			continue
		}
		if m.HasError || (!pause && m.Status != constants.ExecutionPaused) ||
			(pause && m.Status != constants.ExecutionQueued && m.Status != constants.ExecutionRunning) {
			return false
		}
		if expected, found := wanted[m.DeviceExecutionID]; !found || expected != m.Boundary {
			return false
		}
		delete(wanted, m.DeviceExecutionID)
	}
	return len(wanted) == 0
}

func sameControlState(a, b execution.GroupControlState) bool {
	if a.Status != b.Status || a.Finished != b.Finished || len(a.Members) != len(b.Members) {
		return false
	}
	for i := range a.Members {
		if a.Members[i] != b.Members[i] {
			return false
		}
	}
	return true
}
