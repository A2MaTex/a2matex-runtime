package execution

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// Boundary contains only confirmed continuation metadata, never execution values.
type Boundary struct {
	DeviceExecutionID string
	Status            string
	NextNodeID        string
	Sequence          int64
	AttemptID         string
}

type GroupControlRequest struct {
	RunGroupID string
	Now        time.Time
	Members    []Boundary
}

type GroupControlResult struct {
	RunGroupID  string
	Disposition constants.GroupControlDisposition
	Changed     int
	Cursor      string
}

type GroupControlMember struct {
	Boundary
	HasActiveAttempt, Finished, HasError bool
}

type GroupControlState struct {
	Status    string
	Finished  bool
	Members   []GroupControlMember
	Watermark int64
}

type GroupControlTx interface {
	GroupControlState(context.Context, string) (GroupControlState, error)
	UpdateGroupControl(context.Context, string, bool, int) error
	AppendEvents(context.Context, []PendingEvent) ([]string, error)
}

// GroupControlRepository is an optional, narrow capability of the checkpoint store.
type GroupControlRepository interface {
	WithGroupControlTx(context.Context, func(GroupControlTx) error) error
	ReadGroupControl(context.Context, string) (GroupControlState, error)
	NotifyRecoveredCheckpoint()
}
