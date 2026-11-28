package eventdelivery

import (
	"context"
	"sync/atomic"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Session owns one prepared page and can be run once for one consumer.
type Session struct {
	service *Service
	after   int64
	page    execution.EventPage
	wakeup  <-chan struct{}
	started atomic.Bool
}

// Run returns the last successful delivery cursor when cancelled or failed.
// It never retries a failed sink callback or stores a durable acknowledgement.
func (s *Session) Run(ctx context.Context, sink Sink) (int64, error) {
	if s == nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return 0, xerror.WithOperation("run_event_delivery", xerror.ErrExecutionPersistence)
	}
	if !s.started.CompareAndSwap(false, true) {
		return 0, xerror.WithOperation("run_event_delivery", xerror.ErrExecutionStaleState)
	}
	cursor, page, wakeup := s.after, s.page, s.wakeup
	// Release the prepared references even when Run fails before its first send.
	s.page, s.wakeup = execution.EventPage{}, nil
	if err := ctx.Err(); err != nil {
		return cursor, err
	}
	if s.service == nil || sink == nil {
		return cursor, xerror.WithOperation("run_event_delivery", xerror.ErrExecutionPersistence)
	}
	for {
		if err := ctx.Err(); err != nil {
			return cursor, err
		}
		for _, event := range page.Events {
			if err := ctx.Err(); err != nil {
				return cursor, err
			}
			next, err := execution.ParseEventCursor(event.Cursor)
			if err != nil || next <= cursor || next > page.State.Watermark {
				return cursor, xerror.WithOperation("validate_execution_event_cursor", xerror.ErrExecutionPersistence)
			}
			if !event.Product {
				if err := sink.Deliver(ctx, event); err != nil {
					return cursor, xerror.WithOperation("deliver_execution_event", err)
				}
			}
			cursor = next
		}
		more := page.More
		page = execution.EventPage{}
		if !more {
			if err := ctx.Err(); err != nil {
				return cursor, err
			}
			if err := sink.Idle(ctx); err != nil {
				return cursor, xerror.WithOperation("idle_execution_event_stream", err)
			}
			if err := s.service.observer.Wait(ctx, wakeup); err != nil {
				return cursor, err
			}
		}
		var err error
		wakeup, err = s.service.observer.Register(ctx)
		if err != nil {
			return cursor, err
		}
		page, err = s.service.readPage(ctx, cursor)
		if err != nil {
			return cursor, err
		}
	}
}
