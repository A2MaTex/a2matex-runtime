package execution

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// ValidEvent validates the closed A6 envelope in memory. Producers own node
// output validation, retry-policy agreement, and sanitization of error messages.
func ValidEvent(event PendingEvent) bool {
	if len(event.Payload) > constants.MaxEventBytes ||
		!utf8.Valid(event.Payload) ||
		!ValidID(event.RunGroupID) ||
		event.OccurredAt.IsZero() ||
		(event.DeviceExecutionID != "" && !ValidID(event.DeviceExecutionID)) ||
		(event.NodeAttemptID != "" && !ValidID(event.NodeAttemptID)) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(event.Payload, &fields) != nil || fields == nil {
		return false
	}
	attempt := event.Type == constants.EventAttemptStateChanged || event.Type == constants.EventRetryScheduled
	if (event.Type == constants.EventRunGroupStateChanged) != (event.DeviceExecutionID == "") ||
		attempt != (event.NodeAttemptID != "") || attempt != (event.NodeID != "") ||
		(attempt && !ValidNodeID(event.NodeID)) {
		return false
	}
	status := stringField(fields["status"])
	valid := false
	switch event.Type {
	case constants.EventRunGroupStateChanged:
		valid = len(fields) == 1 && oneOf(status, "RUNNING", "PAUSED", "SUCCEEDED", "FAILED", "CANCELLED", "INTERRUPTED")
	case constants.EventDeviceStateChanged:
		next := bytes.TrimSpace(fields["nextNodeId"])
		terminal := oneOf(status, "SUCCEEDED", "FAILED", "CANCELLED", "INTERRUPTED")
		validNext := (terminal && bytes.Equal(next, []byte("null"))) ||
			(!terminal && ValidNodeID(stringField(next)))
		valid = validNext && oneOf(status, "QUEUED", "RUNNING", "PAUSED", "SUCCEEDED", "FAILED", "CANCELLED", "INTERRUPTED") &&
			((len(fields) == 2 && fields["status"] != nil) ||
				(len(fields) == 3 && oneOf(status, "FAILED", "INTERRUPTED") && validEventError(fields["error"])))
	case constants.EventAttemptStateChanged:
		number := integerField(fields["attemptNumber"])
		valid = fields["status"] != nil && number >= 1 && number <= constants.MaxNodeAttemptNumber
		switch status {
		case "RUNNING", "CANCELLED":
			valid = valid && len(fields) == 2
		case "SUCCEEDED":
			valid = valid && len(fields) == 3 && objectJSON(fields["output"])
		case "FAILED", "TIMED_OUT", "INTERRUPTED":
			valid = valid && len(fields) == 3 && validEventError(fields["error"])
		default:
			valid = false
		}
	case constants.EventRetryScheduled:
		number, delay := integerField(fields["nextAttemptNumber"]), integerField(fields["delayMs"])
		valid = len(fields) == 2 && number >= 2 && number <= constants.MaxNodeAttemptNumber && delay >= 0 && delay <= constants.MaxRetryDelayMS
	}
	if !valid {
		return false
	}
	// Reserve the widest SQLite cursor, so every assigned envelope fits A6.
	wire, err := json.Marshal(Event{Cursor: "9223372036854775807", Type: event.Type,
		OccurredAt: event.OccurredAt.UTC().Format(constants.ExecutionTimestampLayout), RunGroupID: event.RunGroupID,
		DeviceExecutionID: event.DeviceExecutionID, NodeAttemptID: event.NodeAttemptID, NodeID: event.NodeID, Payload: event.Payload})
	return err == nil && len(wire) <= constants.MaxEventBytes
}

func stringField(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func integerField(raw json.RawMessage) int64 {
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return -1
	}
	return value
}

func objectJSON(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func oneOf(value string, values ...string) bool {
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}

func validEventError(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return false
	}
	message := stringField(fields["message"])
	length := utf8.RuneCountInString(message)
	retryable := bytes.TrimSpace(fields["retryable"])
	return length >= 1 && length <= constants.MaxRuntimeErrorMessageChars &&
		(bytes.Equal(retryable, []byte("true")) || bytes.Equal(retryable, []byte("false"))) &&
		oneOf(stringField(fields["code"]), "RUNTIME_BUSY", "RUNTIME_INCOMPATIBLE", "VALIDATION_FAILED",
			"INVALID_EXECUTION_STATE", "EVENT_CURSOR_EXPIRED", "ADB_UNAVAILABLE", "DEVICE_NOT_FOUND",
			"DEVICE_UNAUTHORIZED", "DEVICE_OFFLINE", "DEVICE_NOT_READY", "DEVICE_DISCONNECTED",
			"COMMAND_START_FAILED", "COMMAND_INPUT_UNSUPPORTED", "COMMAND_FAILED", "COMMAND_TIMED_OUT",
			"NODE_TIMED_OUT", "NODE_OUTPUT_INVALID", "PERSISTENCE_FAILED", "EXECUTION_INTERRUPTED", "INTERNAL_ERROR")
}

func RetryableEventError(raw json.RawMessage) bool {
	if len(raw) > constants.MaxEventBytes || !utf8.Valid(raw) || !validEventError(raw) {
		return false
	}
	var value struct {
		Retryable bool `json:"retryable"`
	}
	return json.Unmarshal(raw, &value) == nil && value.Retryable
}
