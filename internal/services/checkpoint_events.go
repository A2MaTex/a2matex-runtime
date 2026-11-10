package services

import (
	"encoding/json"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type attemptEventPayload struct {
	Status        string          `json:"status"`
	AttemptNumber int             `json:"attemptNumber"`
	Output        json.RawMessage `json:"output,omitempty"`
	Error         json.RawMessage `json:"error,omitempty"`
}

func attemptCheckpointEvent(ref execution.AttemptRef, now time.Time, payload attemptEventPayload) (execution.PendingEvent, error) {
	return checkpointEvent(execution.PendingEvent{Type: constants.EventAttemptStateChanged, RunGroupID: ref.RunGroupID,
		DeviceExecutionID: ref.DeviceExecutionID, NodeAttemptID: ref.AttemptID, NodeID: ref.NodeID, OccurredAt: now}, payload)
}

func deviceCheckpointEvent(device execution.DeviceCheckpoint) (execution.PendingEvent, error) {
	var next *string
	if device.NextNodeID != "" {
		next = &device.NextNodeID
	}
	payload := struct {
		Status     string          `json:"status"`
		NextNodeID *string         `json:"nextNodeId"`
		Error      json.RawMessage `json:"error,omitempty"`
	}{device.Status, next, device.Error}
	return checkpointEvent(execution.PendingEvent{Type: constants.EventDeviceStateChanged, RunGroupID: device.RunGroupID,
		DeviceExecutionID: device.DeviceExecutionID, OccurredAt: device.Now}, payload)
}

func retryCheckpointEvent(request execution.FinishAttemptRequest, number int) (execution.PendingEvent, error) {
	payload := struct {
		NextAttemptNumber int `json:"nextAttemptNumber"`
		DelayMS           int `json:"delayMs"`
	}{number, request.Retry.DelayMS}
	return checkpointEvent(execution.PendingEvent{Type: constants.EventRetryScheduled, RunGroupID: request.RunGroupID,
		DeviceExecutionID: request.DeviceExecutionID, NodeAttemptID: request.AttemptID, NodeID: request.NodeID, OccurredAt: request.Now}, payload)
}

func groupStateEvent(groupID string, now time.Time, status string) (execution.PendingEvent, error) {
	payload := struct {
		Status string `json:"status"`
	}{status}
	return checkpointEvent(execution.PendingEvent{Type: constants.EventRunGroupStateChanged, RunGroupID: groupID,
		OccurredAt: now}, payload)
}

func checkpointEvent(event execution.PendingEvent, payload any) (execution.PendingEvent, error) {
	data, err := json.Marshal(payload)
	event.Payload = data
	if err != nil || !execution.ValidEvent(event) {
		return execution.PendingEvent{}, xerror.ErrExecutionInvalidInput
	}
	return event, nil
}

func finishEvents(request execution.FinishAttemptRequest, number int, device execution.DeviceCheckpoint,
	deviceChanged bool, groupStatus string, groupChanged bool,
) ([]execution.PendingEvent, error) {
	attempt, err := attemptCheckpointEvent(request.AttemptRef, request.Now, attemptEventPayload{
		Status: request.Status, AttemptNumber: number, Output: request.Output, Error: request.Error,
	})
	if err != nil {
		return nil, err
	}
	events := []execution.PendingEvent{attempt}
	if request.Retry != nil {
		event, err := retryCheckpointEvent(request, number+1)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if deviceChanged {
		event, err := deviceCheckpointEvent(device)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if groupChanged {
		event, err := groupStateEvent(request.RunGroupID, request.Now, groupStatus)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
