package eventdelivery

import (
	"context"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
)

// A hidden-only full page must advance its scan cursor without delivering a frame.
func TestProductRowsSuppressedAndScanCursorAdvances(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &Session{after: 1, page: execution.EventPage{Events: []execution.Event{{Cursor: "2", Product: true}, {Cursor: "3", Product: false}, {Cursor: "4", Product: true}}, State: execution.EventStreamState{Watermark: 4}}}
	// Once this prepared page is drained the observer wait sees cancellation.
	session.service = &Service{}
	delivered := []string{}
	cursor, err := session.Run(ctx, &productSink{deliver: func(e execution.Event) { delivered = append(delivered, e.Cursor) }, idle: cancel})
	if err == nil || cursor != 4 || len(delivered) != 1 || delivered[0] != "3" {
		t.Fatal(cursor, err, delivered)
	}
}

type productSink struct {
	deliver func(execution.Event)
	idle    func()
}

func (s *productSink) Deliver(_ context.Context, e execution.Event) error { s.deliver(e); return nil }
func (s *productSink) Idle(context.Context) error                         { s.idle(); return context.Canceled }
