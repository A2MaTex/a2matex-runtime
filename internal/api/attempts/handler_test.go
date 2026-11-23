package attempts_test

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

	"github.com/A2MaTex/a2matex-runtime/internal/api/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const deviceID = "550e8400-e29b-41d4-a716-446655440002"
const route = "/api/v1/device-executions/" + deviceID + "/attempts"

type reader struct {
	calls int
	read  func(context.Context, string, int64, int) (execution.AttemptPage, error)
}

func (r *reader) Attempts(ctx context.Context, id string, after int64, limit int) (execution.AttemptPage, error) {
	r.calls++
	if r.read != nil {
		return r.read(ctx, id, after, limit)
	}
	return execution.AttemptPage{Attempts: []execution.NodeAttempt{}}, nil
}

func router(t *testing.T, source attempts.Reader, logs io.Writer) (http.Handler, string) {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	session, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(middleware.RequestLogging(slog.New(slog.NewJSONHandler(logs, nil))), middleware.CacheControl(), middleware.Authentication(session))
	r.GET("/api/v1/device-executions/:deviceExecutionId/attempts", attempts.NewHandler(source).ByDeviceExecution)
	return r, session.ConnectionToken()
}

func send(r http.Handler, token string, req *http.Request) *httptest.ResponseRecorder {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestHistoryQueryDefaultsAndRequestContext(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		after       int64
		limit       int
	}{
		{"omitted", "", 0, 100}, {"empty query", "?", 0, 100}, {"separators", "?&&", 0, 100},
		{"explicit", "?afterSequence=2&limit=1", 2, 1}, {"leading zeros", "?afterSequence=0002&limit=01", 2, 1},
		{"escaped digits", "?afterSequence=%32&limit=%31", 2, 1}, {"maximum", "?afterSequence=9223372036854775807&limit=100", 9223372036854775807, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, route+tc.query, nil)
			source := &reader{read: func(ctx context.Context, id string, after int64, limit int) (execution.AttemptPage, error) {
				if ctx != req.Context() || id != deviceID || after != tc.after || limit != tc.limit {
					t.Fatal("changed query or request context", id, after, limit)
				}
				return execution.AttemptPage{Attempts: []execution.NodeAttempt{}}, nil
			}}
			r, token := router(t, source, io.Discard)
			out := send(r, token, req)
			if out.Code != 200 || source.calls != 1 || out.Body.String() != `{"attempts":[],"nextAfterSequence":null}` || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(out.Code, source.calls, out.Body.String())
			}
		})
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("private body failure") }
func (brokenBody) Close() error             { return nil }

func TestHistoryRejectsInvalidInputBeforeReading(t *testing.T) {
	for _, query := range []string{
		"limit=", "limit", "limit=0", "limit=101", "limit=-1", "limit=%2B1", "limit=+1", "limit=1.0", "limit=1e1", "limit=%201", "limit=١",
		"limit=1&limit=2", "limit=1&%6cimit=2", "Limit=1", "status=RUNNING", "cursor=1", "token=secret", "offset=0", "sort=desc",
		"afterSequence=", "afterSequence=-1", "afterSequence=%2B1", "afterSequence=1.1", "afterSequence=1e3", "afterSequence=1&afterSequence=2",
		"afterSequence=9223372036854775808", "limit=9223372036854775808", "limit=%", "limit=%GG", "limit=1;afterSequence=0",
	} {
		t.Run(query, func(t *testing.T) {
			source := &reader{}
			r, token := router(t, source, io.Discard)
			out := send(r, token, httptest.NewRequest(http.MethodGet, route+"?"+query, nil))
			if out.Code != 400 || source.calls != 0 || !strings.Contains(out.Body.String(), `"code":"INVALID_REQUEST"`) {
				t.Fatal(out.Code, source.calls, out.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		name   string
		modify func(*http.Request)
	}{
		{"invalid ID", func(r *http.Request) { r.URL.Path = strings.ReplaceAll(route, deviceID, "invalid") }},
		{"uppercase ID", func(r *http.Request) { r.URL.Path = strings.ReplaceAll(route, deviceID, strings.ToUpper(deviceID)) }},
		{"non-v4 ID", func(r *http.Request) { r.URL.Path = strings.ReplaceAll(route, "41d4", "51d4") }},
		{"declared body", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{}")); r.ContentLength = 2 }},
		{"unknown-length whitespace", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(" ")); r.ContentLength = -1 }},
		{"body read failure", func(r *http.Request) { r.Body = brokenBody{}; r.ContentLength = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &reader{}
			r, token := router(t, source, io.Discard)
			req := httptest.NewRequest(http.MethodGet, route, nil)
			tc.modify(req)
			if out := send(r, token, req); out.Code != 400 || source.calls != 0 {
				t.Fatal(out.Code, source.calls, out.Body.String())
			}
		})
	}
}

func TestHistoryProjectionPreservesEveryAttemptState(t *testing.T) {
	for _, state := range []string{"RUNNING", "SUCCEEDED", "FAILED", "TIMED_OUT", "CANCELLED", "INTERRUPTED"} {
		t.Run(state, func(t *testing.T) {
			attempt := execution.NodeAttempt{ID: deviceID, DeviceExecutionID: deviceID, Sequence: 2, NodeID: "capture", AttemptNumber: 2, Status: state, StartedAt: "2026-11-24T01:00:00.000Z"}
			if state != "RUNNING" {
				finished := "2026-11-24T01:00:00.100Z"
				attempt.FinishedAt = &finished
			}
			if state == "SUCCEEDED" {
				attempt.Output = json.RawMessage(`{"artifactId":"550e8400-e29b-41d4-a716-446655440003"}`)
			} else if state != "RUNNING" && state != "CANCELLED" {
				attempt.Error = json.RawMessage(`{"code":"COMMAND_FAILED","message":"controlled safe error","retryable":false}`)
			}
			page := execution.AttemptPage{Attempts: []execution.NodeAttempt{attempt}}
			source := &reader{read: func(context.Context, string, int64, int) (execution.AttemptPage, error) { return page, nil }}
			r, token := router(t, source, io.Discard)
			out := send(r, token, httptest.NewRequest(http.MethodGet, route, nil))
			want, _ := json.Marshal(page)
			if out.Code != 200 || out.Body.String() != string(want) {
				t.Fatal("projection changed", out.Code, out.Body.String(), string(want))
			}
		})
	}
}

func TestHistoryErrorMappingAndSafeContextualLogs(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		failure    error
		status     int
	}{
		{"missing", "RESOURCE_NOT_FOUND", xerror.ErrExecutionNotFound, 404},
		{"storage", "PERSISTENCE_FAILED", xerror.ErrExecutionPersistence, 500},
		{"unknown commit", "PERSISTENCE_FAILED", xerror.ErrExecutionCommitUnknown, 500},
		{"integrity", "PERSISTENCE_FAILED", xerror.ErrRecoveryIntegrity, 500},
		{"invalid trusted input", "INTERNAL_ERROR", xerror.ErrExecutionInvalidInput, 500},
		{"unexpected", "INTERNAL_ERROR", errors.New("private driver failure"), 500},
		{"cancelled", "INTERNAL_ERROR", context.Canceled, 500},
		{"deadline", "INTERNAL_ERROR", context.DeadlineExceeded, 500},
		{"missing reader", "INTERNAL_ERROR", nil, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var source attempts.Reader
			if tc.failure != nil {
				source = &reader{read: func(context.Context, string, int64, int) (execution.AttemptPage, error) {
					return execution.AttemptPage{}, errors.Join(tc.failure, errors.New("private database path"))
				}}
			}
			var logs bytes.Buffer
			r, token := router(t, source, &logs)
			out := send(r, token, httptest.NewRequest(http.MethodGet, route, nil))
			if out.Code != tc.status || !strings.Contains(out.Body.String(), `"code":"`+tc.code+`"`) || strings.Contains(out.Body.String(), "attempts") || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String())
			}
			if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "private") || strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), `"operation":`) {
				t.Fatal("missing context or exposed diagnostic data", logs.String())
			}
			if tc.code == constants.PersistenceFailed && !strings.Contains(out.Body.String(), constants.APIErrorMessageAttemptReadFailed) {
				t.Fatal("wrong history failure message", out.Body.String())
			}
		})
	}
	var logs bytes.Buffer
	source := &reader{}
	r, token := router(t, source, &logs)
	out := send(r, "", httptest.NewRequest(http.MethodGet, strings.ReplaceAll(route, deviceID, "invalid")+"?token="+token, nil))
	if out.Code != 401 || source.calls != 0 || strings.Contains(logs.String(), token) || !strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatal("authentication did not precede validation", out.Code, logs.String())
	}
}
