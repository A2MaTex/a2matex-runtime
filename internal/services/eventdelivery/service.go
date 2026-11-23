// Package eventdelivery follows committed events from replay into live reads.
package eventdelivery

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/eventobservation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Sink delivers events and handles idle opportunities in the consumer's goroutine.
// Both methods must respect cancellation and bound any external I/O.
type Sink interface {
	Deliver(context.Context, execution.Event) error
	Idle(context.Context) error
}

type Service struct {
	reader   execution.EventReader
	observer *eventobservation.Observer
}

// New uses the same reader for durable replay and I2 post-commit observation.
func New(reader execution.EventReader) *Service {
	return &Service{reader: reader, observer: eventobservation.New(reader)}
}

// Prepare validates the initial read before a transport starts streaming.
// A nil cursor follows events after the captured watermark, including zero.
func (s *Service) Prepare(ctx context.Context, after *int64) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var cursor int64
	if after != nil {
		cursor = *after
		if cursor <= 0 {
			return nil, xerror.WithOperation("prepare_event_delivery", xerror.ErrExecutionInvalidInput)
		}
	}
	if s == nil || s.reader == nil {
		return nil, xerror.WithOperation("prepare_event_delivery", xerror.ErrExecutionPersistence)
	}
	wakeup, err := s.observer.Register(ctx)
	if err != nil {
		return nil, err
	}
	if after == nil {
		state, err := s.reader.EventWatermark(ctx)
		if err != nil {
			return nil, xerror.WithOperation("prepare_event_delivery", err)
		}
		cursor = state.Watermark
	}
	page, err := s.readPage(ctx, cursor)
	if err != nil {
		return nil, err
	}
	return &Session{service: s, after: cursor, page: page, wakeup: wakeup}, nil
}

func (s *Service) readPage(ctx context.Context, after int64) (execution.EventPage, error) {
	if err := ctx.Err(); err != nil {
		return execution.EventPage{}, err
	}
	page, err := s.reader.ReplayEvents(ctx, after, constants.MaxEventPageSize)
	if contextErr := ctx.Err(); contextErr != nil {
		return execution.EventPage{}, contextErr
	}
	if err != nil {
		return execution.EventPage{}, xerror.WithOperation("read_execution_event_stream", err)
	}
	if page.More && len(page.Events) == 0 {
		return execution.EventPage{}, xerror.WithOperation("read_execution_event_stream", xerror.ErrExecutionPersistence)
	}
	return page, nil
}
