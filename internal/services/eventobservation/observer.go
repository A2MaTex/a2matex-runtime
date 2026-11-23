// Package eventobservation provides cancellable waits for committed-event reads.
package eventobservation

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Source supplies the repository's shared post-commit notification channel.
type Source interface {
	EventWakeup() <-chan struct{}
}

// Observer holds no subscriber state and starts no goroutines.
type Observer struct {
	source Source
}

func New(source Source) *Observer {
	return &Observer{source: source}
}

// Register captures the next notification before a consumer reads events.
// Re-register before each read cycle; a closed channel must not be reused.
func (o *Observer) Register(ctx context.Context) (<-chan struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o == nil || o.source == nil {
		return nil, xerror.WithOperation("register_event_observation", xerror.ErrExecutionPersistence)
	}
	wakeup := o.source.EventWakeup()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if wakeup == nil {
		return nil, xerror.WithOperation("register_event_observation", xerror.ErrExecutionPersistence)
	}
	return wakeup, nil
}

// Wait returns nil when a commit notification or fallback permits another read.
// It does not acknowledge an event or guarantee that new events are available.
func (o *Observer) Wait(ctx context.Context, wakeup <-chan struct{}) error {
	return o.wait(ctx, wakeup, constants.EventObservationRecheckInterval)
}

func (o *Observer) wait(ctx context.Context, wakeup <-chan struct{}, interval time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o == nil || o.source == nil || wakeup == nil {
		return xerror.WithOperation("wait_event_observation", xerror.ErrExecutionPersistence)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-wakeup:
	case <-timer.C:
	}
	// Cancellation takes precedence when it is observed alongside a notification.
	return ctx.Err()
}
