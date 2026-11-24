package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/api/events"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/eventdelivery"
	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

func TestStartReturnsBindFailureSynchronously(t *testing.T) {
	t.Parallel()

	server := New(
		config.Config{HTTP: config.HTTPConfig{ListenAddress: "127.0.0.1"}},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	)

	err := server.Start(context.Background())
	if err == nil {
		t.Fatal("Start() error = nil, want bind failure")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want bind failure", err)
	}
}

type shutdownEventReader struct{ wake chan struct{} }

func (r shutdownEventReader) EventWakeup() <-chan struct{} { return r.wake }
func (r shutdownEventReader) EventWatermark(context.Context) (execution.EventStreamState, error) {
	return execution.EventStreamState{}, nil
}
func (r shutdownEventReader) ReplayEvents(context.Context, int64, int) (execution.EventPage, error) {
	return execution.EventPage{State: execution.EventStreamState{Watermark: 1}, Events: []execution.Event{{Cursor: "1", Type: "RUN_GROUP_STATE_CHANGED", OccurredAt: "2026-11-24T03:00:00.000Z", RunGroupID: "10000000-0000-4000-8000-000000000001", Payload: json.RawMessage(`{"status":"RUNNING"}`)}}}, nil
}

// This controlled server writer stalls body I/O on a real net.Pipe. Header
// flushing stays on HTTP; deadline operations reach both transports.
type blockedEventWriter struct {
	http.ResponseWriter
	pipe    net.Conn
	entered chan struct{}
}

func (w *blockedEventWriter) Write(data []byte) (int, error) {
	close(w.entered)
	return w.pipe.Write(data)
}
func (w *blockedEventWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *blockedEventWriter) SetWriteDeadline(deadline time.Time) error {
	return errors.Join(w.pipe.SetWriteDeadline(deadline), http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline))
}

func TestShutdownCancelsStreamAndBlockedWrite(t *testing.T) {
	for _, parentStop := range []bool{false, true} {
		name := "direct shutdown"
		if parentStop {
			name = "parent cancellation"
		}
		t.Run(name, func(t *testing.T) {
			reserved, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := reserved.Addr().String()
			if err := reserved.Close(); err != nil {
				t.Fatal(err)
			}
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			entered := make(chan struct{})
			gin.SetMode(gin.ReleaseMode)
			router := gin.New()
			router.GET("/events", events.NewHandler(eventdelivery.New(shutdownEventReader{wake: make(chan struct{})}), slog.New(slog.NewJSONHandler(io.Discard, nil))).Stream)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				router.ServeHTTP(&blockedEventWriter{ResponseWriter: w, pipe: left, entered: entered}, r)
			})
			server := New(config.Config{HTTP: config.HTTPConfig{ListenAddress: address, WriteTimeout: time.Second}}, handler)
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := server.Start(parent); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := server.Shutdown(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			client := &http.Client{Timeout: 3 * time.Second}
			request, err := http.NewRequest(http.MethodGet, "http://"+address+"/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Accept", "text/event-stream")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("stream never blocked in body I/O")
			}
			if parentStop {
				cancel()
			}
			deadline, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			if err := server.Shutdown(deadline); err != nil {
				t.Fatal("stream survived cancellation until forced close", err)
			}
			if err := server.Wait(); err != nil {
				t.Fatal(err)
			}
			if t.Context().Err() != nil {
				t.Fatal("shutdown cancelled caller parent")
			}
		})
	}
}
