package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func TestValidEvent(t *testing.T) {
	const group = "10000000-0000-4000-8000-000000000001"
	const device = "10000000-0000-4000-8000-000000000002"
	const attempt = "10000000-0000-4000-8000-000000000003"
	for _, test := range []struct {
		name, kind, device, attempt, node, payload string
		valid                                      bool
	}{
		{"group", constants.EventRunGroupStateChanged, "", "", "", `{"status":"RUNNING"}`, true},
		{"device", constants.EventDeviceStateChanged, device, "", "", `{"status":"RUNNING","nextNodeId":"start_01"}`, true},
		{"terminal device", constants.EventDeviceStateChanged, device, "", "", `{"status":"SUCCEEDED","nextNodeId":null}`, true},
		{"attempt", constants.EventAttemptStateChanged, device, attempt, "start_01", `{"status":"SUCCEEDED","attemptNumber":1,"output":{}}`, true},
		{"retry", constants.EventRetryScheduled, device, attempt, "tap_01", `{"nextAttemptNumber":2,"delayMs":1000}`, true},
		{"running attempt", constants.EventAttemptStateChanged, device, attempt, "start_01", `{"status":"RUNNING","attemptNumber":1}`, true},
		{"failed attempt", constants.EventAttemptStateChanged, device, attempt, "tap_01", `{"status":"FAILED","attemptNumber":1,"error":{"code":"COMMAND_FAILED","message":"Command failed","retryable":false}}`, true},
		{"unknown event", "LOG", "", "", "", `{}`, false},
		{"unknown field", constants.EventRunGroupStateChanged, "", "", "", `{"status":"RUNNING","secret":"token"}`, false},
		{"missing device", constants.EventDeviceStateChanged, "", "", "", `{"status":"RUNNING","nextNodeId":"start_01"}`, false},
		{"missing next node", constants.EventDeviceStateChanged, device, "", "", `{"status":"RUNNING"}`, false},
		{"terminal next node", constants.EventDeviceStateChanged, device, "", "", `{"status":"SUCCEEDED","nextNodeId":"start_01"}`, false},
		{"invalid attempt number", constants.EventAttemptStateChanged, device, attempt, "start_01", `{"status":"RUNNING","attemptNumber":4}`, false},
		{"output during running", constants.EventAttemptStateChanged, device, attempt, "start_01", `{"status":"RUNNING","attemptNumber":1,"output":{}}`, false},
		{"null output", constants.EventAttemptStateChanged, device, attempt, "start_01", `{"status":"SUCCEEDED","attemptNumber":1,"output":null}`, false},
		{"transport error", constants.EventAttemptStateChanged, device, attempt, "tap_01", `{"status":"FAILED","attemptNumber":1,"error":{"code":"UNAUTHORIZED","message":"Denied","retryable":false}}`, false},
		{"unsafe error field", constants.EventAttemptStateChanged, device, attempt, "tap_01", `{"status":"FAILED","attemptNumber":1,"error":{"code":"COMMAND_FAILED","message":"Failed","retryable":false,"details":"secret"}}`, false},
		{"oversize", constants.EventAttemptStateChanged, device, attempt, "start_01", `{"status":"SUCCEEDED","attemptNumber":1,"output":{"value":"` + strings.Repeat("x", constants.MaxEventBytes) + `"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := PendingEvent{Type: test.kind, RunGroupID: group, DeviceExecutionID: test.device,
				NodeAttemptID: test.attempt, NodeID: test.node, OccurredAt: time.Now(), Payload: json.RawMessage(test.payload)}
			if got := ValidEvent(event); got != test.valid {
				t.Fatalf("ValidEvent = %v, want %v", got, test.valid)
			}
		})
	}
}

func TestParseEventCursor(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1.0", "1e2", "9223372036854775808"} {
		if _, err := ParseEventCursor(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	for _, value := range []string{"1", "42", "9223372036854775807"} {
		if _, err := ParseEventCursor(value); err != nil {
			t.Errorf("rejected %q: %v", value, err)
		}
	}
}
