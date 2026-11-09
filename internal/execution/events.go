package execution

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// PendingEvent is produced from validated state by admission, checkpoints, or recovery.
// Output must already satisfy the node contract and errors must be sanitized by the producer.
type PendingEvent struct {
	Type              string
	OccurredAt        time.Time
	RunGroupID        string
	DeviceExecutionID string
	NodeAttemptID     string
	NodeID            string
	Payload           json.RawMessage
}

type Event struct {
	Cursor            string          `json:"cursor"`
	Type              string          `json:"type"`
	OccurredAt        string          `json:"occurredAt"`
	RunGroupID        string          `json:"runGroupId"`
	DeviceExecutionID string          `json:"deviceExecutionId,omitempty"`
	NodeAttemptID     string          `json:"nodeAttemptId,omitempty"`
	NodeID            string          `json:"nodeId,omitempty"`
	Payload           json.RawMessage `json:"payload"`
}

type EventStreamState struct {
	Watermark int64
	Floor     int64
}

type EventPage struct {
	Events []Event
	State  EventStreamState
	More   bool
}

// EventReader accepts numeric cursors internally. Zero starts at the beginning
// of retained history only while the replay floor is also zero.
type EventReader interface {
	ReplayEvents(context.Context, int64, int) (EventPage, error)
	EventWatermark(context.Context) (EventStreamState, error)
	EventWakeup() <-chan struct{}
}

type EventRetentionRepository interface {
	PruneEvents(context.Context, int, int) (int, error)
	EventMaintenance() <-chan struct{}
}

// ParseEventCursor parses an explicit API cursor. Omitted cursors are handled
// separately by capturing the connection-time watermark after registering a wakeup.
func ParseEventCursor(value string) (int64, error) {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, xerror.ErrExecutionInvalidInput
		}
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor <= 0 {
		return 0, xerror.ErrExecutionInvalidInput
	}
	return cursor, nil
}
