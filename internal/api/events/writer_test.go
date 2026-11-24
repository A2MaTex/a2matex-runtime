package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type controlledWriter struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	flushes     int
	flushErr    error
	writeErr    error
	short       bool
	deadlineErr error
	mu          sync.Mutex
	deadline    time.Time
}

func newControlledWriter() *controlledWriter       { return &controlledWriter{header: make(http.Header)} }
func (w *controlledWriter) Header() http.Header    { return w.header }
func (w *controlledWriter) WriteHeader(status int) { w.status = status }
func (w *controlledWriter) Write(data []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return 0, nil
	}
	return w.body.Write(data)
}
func (w *controlledWriter) FlushError() error { w.flushes++; return w.flushErr }
func (w *controlledWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = deadline
	return w.deadlineErr
}

func TestWriterFramesAndA8Conformance(t *testing.T) {
	data, err := os.ReadFile("testdata/valid_node_attempt_event.json")
	if err != nil {
		t.Fatal(err)
	}
	var event execution.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	raw := newControlledWriter()
	ginContext, _ := gin.CreateTestContext(raw)
	writer, err := newStreamWriter(t.Context(), ginContext.Writer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := writer.close(); err != nil {
			t.Error(err)
		}
	}()
	if err := writer.Deliver(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/valid_node_attempt_event.sse")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw.body.Bytes(), expected) || raw.flushes != 1 {
		t.Fatal("A8 frame mismatch", raw.body.String())
	}
	for _, kind := range []string{constants.EventRunGroupStateChanged, constants.EventDeviceStateChanged, constants.EventAttemptStateChanged, constants.EventRetryScheduled} {
		raw.body.Reset()
		event.Type, event.Payload = kind, json.RawMessage("{\"output\":{\"text\":\"line\\nTiếng Việt\"}}")
		if err := writer.Deliver(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(raw.body.String(), "\n")
		var decoded execution.Event
		if len(lines) != 5 || lines[0] != "id: 3" || lines[1] != "event: "+kind || !strings.HasPrefix(lines[2], "data: ") || json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &decoded) != nil || !bytes.Equal(decoded.Payload, event.Payload) {
			t.Fatal("invalid single-line frame", raw.body.String())
		}
	}
}

func TestWriterHeartbeatAndErrorAwareFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := newControlledWriter()
		writer, err := newStreamWriter(t.Context(), raw)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := writer.close(); err != nil {
				t.Error(err)
			}
		}()
		if err := writer.flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(constants.SSEHeartbeatInterval - time.Millisecond)
		if err := writer.Idle(t.Context()); err != nil || raw.body.Len() != 0 {
			t.Fatal("early heartbeat", err)
		}
		time.Sleep(time.Millisecond)
		if err := writer.Idle(t.Context()); err != nil || raw.body.String() != ": heartbeat\n\n" {
			t.Fatal("missing heartbeat", err)
		}
		if err := writer.Idle(t.Context()); err != nil || raw.body.String() != ": heartbeat\n\n" {
			t.Fatal("duplicate heartbeat", err)
		}
		time.Sleep(time.Second)
		if err := writer.Deliver(t.Context(), execution.Event{Cursor: "1", Type: constants.EventRunGroupStateChanged, Payload: json.RawMessage(`{"status":"RUNNING"}`)}); err != nil {
			t.Fatal(err)
		}
		size := raw.body.Len()
		time.Sleep(constants.SSEHeartbeatInterval - time.Millisecond)
		if err := writer.Idle(t.Context()); err != nil || raw.body.Len() != size {
			t.Fatal("event activity did not reset heartbeat timing", err)
		}
		time.Sleep(time.Millisecond)
		if err := writer.Idle(t.Context()); err != nil || raw.body.Len() != size+len(": heartbeat\n\n") {
			t.Fatal("heartbeat missing after event inactivity", err)
		}
		if !raw.deadline.IsZero() {
			t.Fatal("successful flush retained a deadline")
		}
	})
	for _, kind := range []string{"flush", "write", "short", "deadline", "clear"} {
		t.Run(kind, func(t *testing.T) {
			raw := newControlledWriter()
			ginContext, _ := gin.CreateTestContext(raw)
			writer, err := newStreamWriter(t.Context(), ginContext.Writer)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := writer.close(); err != nil {
					t.Error(err)
				}
			}()
			failure := errors.New("controlled I/O failure")
			switch kind {
			case "flush":
				raw.flushErr = failure
			case "write":
				raw.writeErr = failure
			case "short":
				raw.short = true
				failure = io.ErrShortWrite
			case "deadline", "clear":
				raw.deadlineErr = failure
			}
			if kind == "clear" {
				err = writer.flush(t.Context())
			} else {
				err = writer.write(t.Context(), []byte("frame"))
			}
			if !errors.Is(err, failure) {
				t.Fatalf("lost %s error: %v", kind, err)
			}
		})
	}
}

type pipeWriter struct {
	net.Conn
	header http.Header
}

func (w *pipeWriter) Header() http.Header { return w.header }
func (w *pipeWriter) WriteHeader(int)     {}
func (w *pipeWriter) FlushError() error   { return nil }

func TestWriterBlockedIODeadlineAndCancellation(t *testing.T) {
	for _, cancelWrite := range []bool{false, true} {
		name := "deadline"
		if cancelWrite {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				left, right := net.Pipe()
				defer left.Close()
				defer right.Close()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				writer, err := newStreamWriter(ctx, &pipeWriter{Conn: left, header: make(http.Header)})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := writer.close(); err != nil {
						t.Error(err)
					}
				}()
				started := time.Now()
				done := make(chan error, 1)
				go func() { done <- writer.write(ctx, []byte("unread frame")) }()
				if cancelWrite {
					synctest.Wait()
					cancel()
				}
				if err := <-done; err == nil {
					t.Fatal("blocked write succeeded")
				}
				want := constants.SSEWriteTimeout
				if cancelWrite {
					want = 0
				}
				if time.Since(started) != want {
					t.Fatal("write did not end at its deadline", time.Since(started), want)
				}
			})
		})
	}
}

func TestCancellationCannotBeOverwrittenByDeadlineReset(t *testing.T) {
	for range 32 {
		raw := newControlledWriter()
		ctx, cancel := context.WithCancel(t.Context())
		writer, err := newStreamWriter(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		var workers sync.WaitGroup
		workers.Go(func() { cancel() })
		reset := func(deadline time.Time) {
			if err := writer.setDeadline(ctx, deadline); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("unexpected deadline error: %v", err)
			}
		}
		workers.Go(func() { reset(time.Time{}) })
		workers.Go(func() { reset(time.Now().Add(time.Hour)) })
		workers.Wait()
		if err := writer.close(); err != nil {
			t.Fatal(err)
		}
		if raw.deadline.IsZero() || raw.deadline.After(time.Now()) {
			t.Fatal("cancelled deadline was reset", raw.deadline)
		}
	}
}
