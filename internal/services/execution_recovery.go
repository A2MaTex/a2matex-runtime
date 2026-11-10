package services

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// ExecutionRecovery repairs unfinished work synchronously before serving requests.
type ExecutionRecovery struct {
	repository execution.RecoveryRepository
	logger     *slog.Logger
	done       chan struct{}
}

func NewExecutionRecovery(repository execution.RecoveryRepository, logger *slog.Logger) *ExecutionRecovery {
	return &ExecutionRecovery{repository: repository, logger: logger, done: make(chan struct{})}
}

func (s *ExecutionRecovery) Start(ctx context.Context) error {
	var recovery execution.RecoveryWrite
	err := s.repository.WithRecoveryTx(ctx, func(tx execution.RecoveryTx) error {
		state, err := tx.ReadRecoveryState(ctx)
		if err != nil {
			return err
		}
		var events []execution.PendingEvent
		recovery, events, err = recoveryPlan(state, time.Now().UTC())
		if err != nil || recovery.RunGroupID == "" {
			return err
		}
		if err := tx.InterruptRecovery(ctx, recovery); err != nil {
			return err
		}
		_, err = tx.AppendEvents(ctx, events)
		return err
	})
	if err != nil {
		return err
	}
	if recovery.RunGroupID != "" {
		s.logger.InfoContext(ctx, "interrupted unfinished execution during startup",
			"component", "execution_recovery", "event", "execution_recovered",
			"run_groups", 1, "devices", recovery.Devices, "attempts", recovery.Attempts)
	}
	return nil
}

func (s *ExecutionRecovery) Wait() error {
	<-s.done
	return nil
}

func (s *ExecutionRecovery) Shutdown(context.Context) error {
	close(s.done)
	return nil
}

func recoveryPlan(state execution.RecoveryState, now time.Time) (execution.RecoveryWrite, []execution.PendingEvent, error) {
	if state.Status == "" && len(state.Attempts) == 0 {
		return execution.RecoveryWrite{}, nil, nil
	}
	if !execution.ValidID(state.RunGroupID) ||
		(state.Status != constants.ExecutionRunning && state.Status != constants.ExecutionPaused) {
		return execution.RecoveryWrite{}, nil, xerror.ErrRecoveryIntegrity
	}
	devices, earliestFinish, err := recoverableDevices(state)
	if err != nil {
		return execution.RecoveryWrite{}, nil, err
	}
	for _, attempt := range state.Attempts {
		device, found := devices[attempt.DeviceID]
		if !found || device.Status != constants.ExecutionRunning || device.NextNodeID != attempt.NodeID ||
			!attempt.Latest || !execution.ValidID(attempt.ID) || attempt.Number < 1 || attempt.Number > constants.MaxNodeAttemptNumber ||
			device.StartedAt == nil || attempt.StartedAt < *device.StartedAt {
			return execution.RecoveryWrite{}, nil, xerror.ErrRecoveryIntegrity
		}
		earliestFinish = max(earliestFinish, attempt.StartedAt)
	}
	// Keep a single finish timestamp that remains valid if the clock moved
	// backward while the agent was offline.
	if now.UnixMilli() < earliestFinish {
		now = time.UnixMilli(earliestFinish).UTC()
	}
	interruption, err := json.Marshal(struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}{Code: constants.ExecutionInterruptedCode, Message: constants.ExecutionInterruptedMessage})
	if err != nil {
		return execution.RecoveryWrite{}, nil, xerror.ErrRecoveryIntegrity
	}
	write := execution.RecoveryWrite{RunGroupID: state.RunGroupID, Now: now, Devices: len(devices),
		Attempts: len(state.Attempts), Interruption: interruption}
	events, err := recoveryEvents(write, state.Attempts, devices)
	return write, events, err
}

func recoverableDevices(state execution.RecoveryState) (map[string]execution.RecoveryDevice, int64, error) {
	devices := make(map[string]execution.RecoveryDevice, len(state.Devices))
	earliestFinish := state.CreatedAt
	for _, device := range state.Devices {
		switch device.Status {
		case constants.ExecutionSucceeded, constants.ExecutionFailed, constants.ExecutionCancelled, constants.ExecutionInterrupted:
			continue
		case constants.ExecutionQueued, constants.ExecutionRunning, constants.ExecutionPaused:
		default:
			return nil, 0, xerror.ErrRecoveryIntegrity
		}
		if !execution.ValidID(device.ID) || !execution.ValidNodeID(device.NextNodeID) ||
			(state.Status == constants.ExecutionPaused && device.Status != constants.ExecutionPaused) ||
			(device.Status == constants.ExecutionRunning && device.StartedAt == nil) {
			return nil, 0, xerror.ErrRecoveryIntegrity
		}
		devices[device.ID] = device
		earliestFinish = max(earliestFinish, device.CreatedAt)
		if device.StartedAt != nil {
			earliestFinish = max(earliestFinish, *device.StartedAt)
		}
	}
	if len(devices) == 0 {
		return nil, 0, xerror.ErrRecoveryIntegrity
	}
	return devices, earliestFinish, nil
}

func recoveryEvents(write execution.RecoveryWrite, attempts []execution.RecoveryAttempt,
	devices map[string]execution.RecoveryDevice,
) ([]execution.PendingEvent, error) {
	slices.SortFunc(attempts, func(a, b execution.RecoveryAttempt) int { return strings.Compare(a.ID, b.ID) })
	deviceIDs := make([]string, 0, len(devices))
	for id := range devices {
		deviceIDs = append(deviceIDs, id)
	}
	slices.Sort(deviceIDs)
	events := make([]execution.PendingEvent, 0, len(attempts)+len(deviceIDs)+1)
	for _, attempt := range attempts {
		ref := execution.AttemptRef{RunGroupID: write.RunGroupID, DeviceExecutionID: attempt.DeviceID, NodeID: attempt.NodeID, AttemptID: attempt.ID}
		event, err := attemptCheckpointEvent(ref, write.Now, attemptEventPayload{
			Status: constants.ExecutionInterrupted, AttemptNumber: attempt.Number, Error: write.Interruption,
		})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	for _, id := range deviceIDs {
		event, err := deviceCheckpointEvent(execution.DeviceCheckpoint{
			AttemptRef: execution.AttemptRef{RunGroupID: write.RunGroupID, DeviceExecutionID: id},
			Status:     constants.ExecutionInterrupted, Error: write.Interruption, Now: write.Now,
		})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	group, err := groupStateEvent(write.RunGroupID, write.Now, constants.ExecutionInterrupted)
	if err != nil {
		return nil, err
	}
	return append(events, group), nil
}
