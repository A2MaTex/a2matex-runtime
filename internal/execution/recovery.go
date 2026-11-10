package execution

import (
	"context"
	"encoding/json"
	"time"
)

type RecoveryState struct {
	RunGroupID string
	Status     string
	CreatedAt  int64
	Devices    []RecoveryDevice
	Attempts   []RecoveryAttempt
}

type RecoveryDevice struct {
	ID, Status, NextNodeID string
	CreatedAt              int64
	StartedAt              *int64
}

type RecoveryAttempt struct {
	ID, DeviceID, NodeID string
	Number               int
	StartedAt            int64
	Latest               bool
}

type RecoveryWrite struct {
	RunGroupID   string
	Devices      int
	Attempts     int
	Now          time.Time
	Interruption json.RawMessage
}

type RecoveryTx interface {
	ReadRecoveryState(context.Context) (RecoveryState, error)
	InterruptRecovery(context.Context, RecoveryWrite) error
	AppendEvents(context.Context, []PendingEvent) ([]string, error)
}

type RecoveryRepository interface {
	WithRecoveryTx(context.Context, func(RecoveryTx) error) error
}
