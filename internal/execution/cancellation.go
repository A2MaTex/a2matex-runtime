package execution

import (
	"context"
	"time"
)

type CancellationRequest struct{ Now time.Time }

type CancellationResult struct {
	RunGroupID string
	Changed    bool
	Cursor     string
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
