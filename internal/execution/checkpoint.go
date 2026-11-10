package execution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type AttemptRef struct {
	RunGroupID        string
	DeviceExecutionID string
	NodeID            string
	AttemptID         string
}

type BeginAttemptRequest struct {
	AttemptRef
	ExpectedDeviceStatus string
	AttemptNumber        int
	MaxAttempts          int
	PreviousAttemptID    string
	Now                  time.Time
}

type RetryDecision struct {
	MaxAttempts int
	DelayMS     int
}

type FinishAttemptRequest struct {
	AttemptRef
	Status            string
	NextNodeID        string
	Output            json.RawMessage // Already validated against the node contract by the engine.
	Error             json.RawMessage // Sanitized A6 error from the engine.
	Retry             *RetryDecision
	GroupCancellation bool // Set after the scheduler accepts group cancellation.
	Now               time.Time
	ShutdownDeadline  time.Time // Optional supervisor deadline for the final checkpoint.
}

type CheckpointResult struct {
	Disposition constants.CheckpointDisposition
	Sequence    int64
	Cursor      string
}

// CheckpointAttempt uses Unix milliseconds, matching the SQLite representation.
type CheckpointAttempt struct {
	ID, NodeID, Status string
	Sequence           int64
	Number             int
	StartedAt          int64
	FinishedAt         *int64
	Output, Error      json.RawMessage
}

type CheckpointState struct {
	GroupStatus, DeviceStatus, NextNodeID string
	DeviceCreatedAt                       int64
	LatestSequence                        int64
	HasActiveAttempt                      bool
	Attempt                               *CheckpointAttempt
}

type DeviceCheckpoint struct {
	AttemptRef
	Status     string
	NextNodeID string
	Error      json.RawMessage
	Now        time.Time
}

type CheckpointTx interface {
	CheckpointState(ctx context.Context, ref AttemptRef, attemptID string) (CheckpointState, error)
	InsertAttempt(ctx context.Context, request BeginAttemptRequest, sequence int64) error
	StartDevice(ctx context.Context, request BeginAttemptRequest) error
	FinalizeAttempt(ctx context.Context, request FinishAttemptRequest) error
	UpdateCheckpointDevice(ctx context.Context, device DeviceCheckpoint) error
	OtherDeviceStatuses(ctx context.Context, ref AttemptRef) ([]string, error)
	UpdateCheckpointGroup(ctx context.Context, runGroupID, status string, now time.Time) error
	AppendEvents(context.Context, []PendingEvent) ([]string, error)
}

type CheckpointRepository interface {
	WithCheckpointTx(context.Context, func(CheckpointTx) error) error
	ReadCheckpoint(context.Context, AttemptRef) (CheckpointState, int64, error)
	NotifyRecoveredCheckpoint()
}
