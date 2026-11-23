package execution

import (
	"context"
	"time"
)

type CancellationRequest struct {
	Now time.Time
	// RunGroupID scopes live cancellation. Empty retains process-stop settlement.
	RunGroupID string
}

type CancellationResult struct {
	RunGroupID string
	Changed    bool
	Cursor     string
	Status     string
}

type CancellationTarget struct {
	Status        string
	HasUnfinished bool
}

// CancellationTargetTx is required only for exact-group live cancellation.
type CancellationTargetTx interface {
	ReadCancellationTarget(context.Context, string) (CancellationTarget, error)
}

type CancellationWrite struct {
	RunGroupID string
	Now        time.Time
	Devices    int
	Attempts   int
}

type CancellationTx interface {
	ReadCancellationState(context.Context) (RecoveryState, error)
	CancelUnfinished(context.Context, CancellationWrite) error
	AppendEvents(context.Context, []PendingEvent) ([]string, error)
}

type CancellationRepository interface {
	WithCancellationTx(context.Context, func(CancellationTx) error) error
}
