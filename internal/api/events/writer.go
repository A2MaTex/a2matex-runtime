package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type streamWriter struct {
	tracked    http.ResponseWriter
	control    *http.ResponseController
	lastWrite  time.Time
	deadlineMu sync.Mutex
	cancelErr  error
	stop       func() bool
	stopped    chan struct{}
}

func newStreamWriter(ctx context.Context, tracked http.ResponseWriter) (*streamWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw := tracked
	for {
		unwrapper, ok := raw.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		raw = unwrapper.Unwrap()
	}
	if _, ok := raw.(interface{ FlushError() error }); !ok {
		return nil, xerror.WithOperation("configure_event_stream_writer", http.ErrNotSupported)
	}
	w := &streamWriter{tracked: tracked, control: http.NewResponseController(raw), stopped: make(chan struct{})}
	if err := w.setDeadline(ctx, time.Now().Add(constants.SSEWriteTimeout)); err != nil {
		return nil, err
	}
	w.stop = context.AfterFunc(ctx, func() {
		defer close(w.stopped)
		w.deadlineMu.Lock()
		defer w.deadlineMu.Unlock()
		w.cancelErr = xerror.WithOperation("cancel_event_stream_write", w.control.SetWriteDeadline(time.Now()))
	})
	return w, nil
}

// close joins cancellation control before the HTTP writer can be released.
func (w *streamWriter) close() error {
	if !w.stop() {
		<-w.stopped
	}
	return w.cancelErr
}

func (w *streamWriter) setDeadline(ctx context.Context, deadline time.Time) error {
	w.deadlineMu.Lock()
	defer w.deadlineMu.Unlock()
	if err := ctx.Err(); err != nil {
		return errors.Join(err, xerror.WithOperation("cancel_event_stream_write", w.control.SetWriteDeadline(time.Now())))
	}
	return xerror.WithOperation("set_event_stream_write_deadline", w.control.SetWriteDeadline(deadline))
}

func (w *streamWriter) Deliver(ctx context.Context, event execution.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return xerror.WithOperation("encode_execution_event", err)
	}
	frame := fmt.Appendf(nil, "id: %s\nevent: %s\ndata: ", event.Cursor, event.Type)
	frame = append(frame, data...)
	frame = append(frame, '\n', '\n')
	return w.write(ctx, frame)
}

func (w *streamWriter) Idle(ctx context.Context) error {
	if time.Since(w.lastWrite) < constants.SSEHeartbeatInterval {
		return ctx.Err()
	}
	return w.write(ctx, []byte(": heartbeat\n\n"))
}

func (w *streamWriter) write(ctx context.Context, frame []byte) error {
	if err := w.setDeadline(ctx, time.Now().Add(constants.SSEWriteTimeout)); err != nil {
		return err
	}
	n, err := w.tracked.Write(frame)
	if err != nil {
		return xerror.WithOperation("write_execution_event_stream", err)
	}
	if n != len(frame) {
		return xerror.WithOperation("write_execution_event_stream", io.ErrShortWrite)
	}
	return w.flush(ctx)
}

func (w *streamWriter) flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.control.Flush(); err != nil {
		return xerror.WithOperation("flush_execution_event_stream", err)
	}
	if err := w.setDeadline(ctx, time.Time{}); err != nil {
		return err
	}
	w.lastWrite = time.Now()
	return nil
}
