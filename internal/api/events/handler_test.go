package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/eventdelivery"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const eventTestToken = "private-event-session-token"

type verifier struct{}

func (verifier) VerifyAuthorization(values []string) bool {
	return len(values) == 1 && values[0] == "Bearer "+eventTestToken
}

type eventReader struct {
	calls int
	cause error
	page  execution.EventPage
	wake  <-chan struct{}
	read  func() (execution.EventPage, error)
}

func (r *eventReader) EventWakeup() <-chan struct{} {
	r.calls++
	if r.wake != nil {
		return r.wake
	}
	return make(chan struct{})
}
func (r *eventReader) EventWatermark(context.Context) (execution.EventStreamState, error) {
	r.calls++
	return execution.EventStreamState{Watermark: 10}, nil
}
func (r *eventReader) ReplayEvents(context.Context, int64, int) (execution.EventPage, error) {
	r.calls++
	if r.read != nil {
		return r.read()
	}
	return r.page, r.cause
}

type bodyError struct{}

func (bodyError) Read([]byte) (int, error) { return 0, errors.New("private body failure") }
func (bodyError) Close() error             { return nil }

func eventRouter(source execution.EventReader, output io.Writer) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	logger := slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	router := gin.New()
	router.Use(middleware.Recovery(logger), middleware.RequestLogging(logger), middleware.CacheControl(), middleware.Authentication(verifier{}))
	router.GET("/api/v1/events", NewHandler(eventdelivery.New(source), logger).Stream)
	return router
}

func streamRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	r.Header.Set("Authorization", "Bearer "+eventTestToken)
	r.Header.Set("Accept", "text/event-stream")
	return r
}

func TestEventRequestAndPreparationFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(*http.Request)
		cause  error
		status int
		code   string
		noRead bool
	}{
		{"no auth", func(r *http.Request) { r.Header.Del("Authorization"); r.Header.Set("Last-Event-ID", "bad") }, nil, 401, "UNAUTHORIZED", true},
		{"duplicate auth", func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+eventTestToken) }, nil, 401, "UNAUTHORIZED", true},
		{"no accept", func(r *http.Request) { r.Header.Del("Accept") }, nil, 400, "INVALID_REQUEST", true},
		{"duplicate accept", func(r *http.Request) { r.Header.Add("Accept", "text/event-stream") }, nil, 400, "INVALID_REQUEST", true},
		{"accept list", func(r *http.Request) { r.Header.Set("Accept", "text/event-stream, */*") }, nil, 400, "INVALID_REQUEST", true},
		{"accept parameter", func(r *http.Request) { r.Header.Set("Accept", "text/event-stream;q=1") }, nil, 400, "INVALID_REQUEST", true},
		{"query", func(r *http.Request) { r.URL.RawQuery = "token=" + eventTestToken }, nil, 400, "INVALID_REQUEST", true},
		{"body", func(r *http.Request) { r.ContentLength = 1 }, nil, 400, "INVALID_REQUEST", true},
		{"unknown body", func(r *http.Request) { r.ContentLength = -1; r.Body = io.NopCloser(strings.NewReader("x")) }, nil, 400, "INVALID_REQUEST", true},
		{"body error", func(r *http.Request) { r.Body = bodyError{} }, nil, 400, "INVALID_REQUEST", true},
		{"duplicate cursor", func(r *http.Request) { r.Header.Add("Last-Event-ID", "2"); r.Header.Add("Last-Event-ID", "3") }, nil, 400, "INVALID_REQUEST", true},
		{"expired", nil, xerror.ErrEventCursorExpired, 410, "EVENT_CURSOR_EXPIRED", false},
		{"future", nil, xerror.ErrEventCursorFuture, 400, "INVALID_REQUEST", false},
		{"persistence", nil, errors.Join(xerror.ErrExecutionPersistence, errors.New("private driver details")), 500, "PERSISTENCE_FAILED", false},
		{"unknown", nil, errors.New("private driver details"), 500, "INTERNAL_ERROR", false},
		{"unsupported writer", nil, nil, 500, "INTERNAL_ERROR", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &eventReader{cause: test.cause}
			var logs bytes.Buffer
			r := streamRequest()
			if test.modify != nil {
				test.modify(r)
			}
			out := httptest.NewRecorder()
			eventRouter(reader, &logs).ServeHTTP(out, r)
			var body struct {
				Error struct {
					Code      string `json:"code"`
					Retryable bool   `json:"retryable"`
				} `json:"error"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil || out.Code != test.status || body.Error.Code != test.code || body.Error.Retryable || (test.noRead && reader.calls != 0) {
				t.Fatalf("response=%d %s calls=%d err=%v", out.Code, out.Body.String(), reader.calls, err)
			}
			if out.Header().Get("Cache-Control") != "no-store" || strings.Contains(logs.String(), eventTestToken) || strings.Contains(logs.String(), "private driver") || strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), `"level":"ERROR"`) {
				t.Fatalf("unsafe or duplicate logging: %s", logs.String())
			}
		})
	}
	for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1,2", "1.0", "1e2", "１", "9223372036854775808"} {
		t.Run("cursor="+value, func(t *testing.T) {
			r := streamRequest()
			r.Header.Set("Last-Event-ID", value)
			if _, ok := decodeRequest(r); ok {
				t.Fatalf("accepted invalid cursor %q", value)
			}
		})
	}
	r := streamRequest()
	r.Header.Set("Accept", "\tTEXT/EVENT-STREAM ")
	r.Header.Set("Last-Event-ID", "0002")
	r.ContentLength, r.Body = -1, io.NopCloser(strings.NewReader(""))
	if request, ok := decodeRequest(r); !ok || request.After == nil || *request.After != 2 {
		t.Fatal("valid normalized accept/cursor/empty body rejected")
	}
}

func TestStartedEventFailureNeverAppendsJSON(t *testing.T) {
	reader := &eventReader{page: execution.EventPage{State: execution.EventStreamState{Watermark: 2}, Events: []execution.Event{{Cursor: "2", Type: "NODE_ATTEMPT_STATE_CHANGED", Payload: json.RawMessage(`invalid`)}}}}
	r := streamRequest()
	r.Header.Set("Last-Event-ID", "1")
	raw := newControlledWriter()
	var logs bytes.Buffer
	func() {
		defer func() {
			if failure := recover(); failure != http.ErrAbortHandler {
				t.Fatalf("wrong abort: %v", failure)
			}
		}()
		eventRouter(reader, &logs).ServeHTTP(raw, r)
	}()
	if raw.status != 200 || raw.body.Len() != 0 || raw.flushes != 1 || raw.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" || raw.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatal("failure corrupted the started response", raw.status, raw.body.String(), raw.Header())
	}
	if strings.Count(logs.String(), "execution_event_stream_closed") != 1 || !strings.Contains(logs.String(), `"operation":"encode_execution_event"`) || strings.Contains(logs.String(), "invalid") {
		t.Fatal("unsafe stream-close logging", logs.String())
	}
}

func TestEventStreamMissingConfigurationAndEarlyCancellation(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	for _, handler := range []*Handler{nil, {}, NewHandler(nil, slog.Default())} {
		router := gin.New()
		router.GET("/api/v1/events", handler.Stream)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, streamRequest())
		if out.Code != 500 || !strings.Contains(out.Body.String(), `"code":"INTERNAL_ERROR"`) {
			t.Fatal("missing dependency looked like a stream", out.Code, out.Body.String())
		}
	}
	reader := &eventReader{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw := newControlledWriter()
	func() {
		defer func() {
			if failure := recover(); failure != http.ErrAbortHandler {
				t.Fatalf("wrong cancellation abort: %v", failure)
			}
		}()
		eventRouter(reader, io.Discard).ServeHTTP(raw, streamRequest().WithContext(ctx))
	}()
	if raw.status != 0 || raw.body.Len() != 0 || reader.calls != 0 {
		t.Fatal("cancelled preparation committed a response or accessed storage")
	}
}

func TestStartedEventExpiryClosesWithoutJSON(t *testing.T) {
	wake := make(chan struct{})
	close(wake)
	reads := 0
	reader := &eventReader{wake: wake, read: func() (execution.EventPage, error) {
		reads++
		if reads > 1 {
			return execution.EventPage{}, xerror.ErrEventCursorExpired
		}
		return execution.EventPage{State: execution.EventStreamState{Watermark: 2}, Events: []execution.Event{{Cursor: "2", Type: "RUN_GROUP_STATE_CHANGED", Payload: json.RawMessage(`{"status":"RUNNING"}`)}}}, nil
	}}
	r := streamRequest()
	r.Header.Set("Last-Event-ID", "1")
	raw := newControlledWriter()
	var logs bytes.Buffer
	func() {
		defer func() {
			if failure := recover(); failure != http.ErrAbortHandler {
				t.Fatalf("wrong expiry abort: %v", failure)
			}
		}()
		eventRouter(reader, &logs).ServeHTTP(raw, r)
	}()
	if raw.status != 200 || !strings.HasPrefix(raw.body.String(), "id: 2\nevent: RUN_GROUP_STATE_CHANGED\ndata: ") || strings.Contains(raw.body.String(), `"error":`) || strings.Count(raw.body.String(), "\n\n") != 1 {
		t.Fatal("expiry corrupted the started stream", raw.status, raw.body.String())
	}
	if reads != 2 || !strings.Contains(logs.String(), `"error_code":"EVENT_CURSOR_EXPIRED"`) || strings.Count(logs.String(), "execution_event_stream_closed") != 1 {
		t.Fatal("expiry was not safely classified", logs.String())
	}
}
