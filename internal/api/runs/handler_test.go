package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
)

const groupID = "ab000000-0000-4000-8000-000000000001"

type reader struct {
	call  func(context.Context, string) (execution.Snapshot, error)
	calls int
}

func (r *reader) ActiveSnapshot(ctx context.Context) (execution.Snapshot, error) {
	r.calls++
	return r.call(ctx, "")
}
func (r *reader) SnapshotByID(ctx context.Context, id string) (execution.Snapshot, error) {
	r.calls++
	return r.call(ctx, id)
}
func router(r runs.SnapshotReader) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(middleware.CacheControl())
	h := runs.NewHandler(r, nil, nil)
	engine.GET("/api/v1/runs/active", h.Active)
	engine.GET("/api/v1/runs/:runGroupId", h.ByID)
	return engine
}
func requireError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code, Message string
			Retryable     bool
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || body.Error.Code != code || body.Error.Retryable || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("error: %d %s headers=%v", response.Code, response.Body.String(), response.Header())
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 1 || strings.Contains(response.Body.String(), "private") {
		t.Fatalf("error exposure: %s", response.Body.String())
	}
}
func TestSnapshotRoutesContextAndDTO(t *testing.T) {
	for _, suffix := range []string{"active", groupID} {
		t.Run(suffix, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			expectedID := suffix
			if suffix == "active" {
				expectedID = ""
			}
			r := &reader{call: func(actual context.Context, id string) (execution.Snapshot, error) {
				if actual != ctx || id != expectedID {
					t.Errorf("context/route mismatch: id=%q", id)
				}
				return execution.Snapshot{Cursor: "42", RunGroup: execution.RunGroup{ID: groupID, RequestID: "ab000000-0000-4000-8000-000000000002", Status: constants.ExecutionRunning, CreatedAt: "2026-11-18T08:00:00.000Z", Summary: execution.RunSummary{Total: 1, Queued: 1}}, DeviceExecutions: []execution.DeviceExecution{{ID: "ab000000-0000-4000-8000-000000000003", ADBSerial: "private-device", Status: constants.ExecutionQueued, CreatedAt: "2026-11-18T08:00:00.000Z"}}}, nil
			}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+suffix, nil).WithContext(ctx)
			response := httptest.NewRecorder()
			router(r).ServeHTTP(response, req)
			const want = `{"cursor":"42","runGroup":{"id":"ab000000-0000-4000-8000-000000000001","requestId":"ab000000-0000-4000-8000-000000000002","status":"RUNNING","createdAt":"2026-11-18T08:00:00.000Z","finishedAt":null,"summary":{"total":1,"queued":1,"running":0,"paused":0,"succeeded":0,"failed":0,"cancelled":0,"interrupted":0}},"deviceExecutions":[{"id":"ab000000-0000-4000-8000-000000000003","adbSerial":"private-device","status":"QUEUED","nextNodeId":null,"createdAt":"2026-11-18T08:00:00.000Z","startedAt":null,"finishedAt":null,"terminalError":null}]}`
			if response.Code != 200 || response.Body.String() != want || r.calls != 1 || response.Header().Get("Content-Type") != "application/json; charset=utf-8" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("snapshot: status=%d calls=%d body=%s", response.Code, r.calls, response.Body.String())
			}
		})
	}
}
func TestInvalidSnapshotRequestsNeverRead(t *testing.T) {
	for _, target := range []string{"active?limit=1", groupID + "?cursor=1", "invalid", strings.ToUpper(groupID), "ab000000-0000-1000-8000-000000000001", "ab000000000040008000000000000001"} {
		t.Run(target, func(t *testing.T) {
			r := &reader{}
			response := httptest.NewRecorder()
			router(r).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+target, nil))
			requireError(t, response, 400, constants.APIErrorCodeInvalidRequest)
			if r.calls != 0 {
				t.Fatal("invalid request reached reader")
			}
		})
	}
	for _, body := range []string{"{}", " "} {
		t.Run("body "+body, func(t *testing.T) {
			r := &reader{}
			response := httptest.NewRecorder()
			router(r).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/active", strings.NewReader(body)))
			requireError(t, response, 400, constants.APIErrorCodeInvalidRequest)
			if r.calls != 0 {
				t.Fatal("body reached reader")
			}
		})
	}
}

type boundedBody struct {
	t     *testing.T
	err   error
	empty bool
	reads int
}

func (b *boundedBody) Read(p []byte) (int, error) {
	b.reads++
	if len(p) > 1 {
		b.t.Error("GET body inspection exceeded one byte")
	}
	if b.err != nil {
		return 0, b.err
	}
	if b.empty {
		return 0, io.EOF
	}
	p[0] = ' '
	return 1, nil
}
func TestUnknownLengthBodyInspectionIsBounded(t *testing.T) {
	for _, kind := range []string{"nonempty", "read error", "empty"} {
		t.Run(kind, func(t *testing.T) {
			body := &boundedBody{t: t, empty: kind == "empty"}
			if kind == "read error" {
				body.err = errors.New("private body read failure")
			}
			r := &reader{call: func(context.Context, string) (execution.Snapshot, error) {
				return execution.Snapshot{}, xerror.ErrExecutionNotFound
			}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/active", nil)
			req.ContentLength = -1
			req.Body = io.NopCloser(body)
			response := httptest.NewRecorder()
			router(r).ServeHTTP(response, req)
			if kind == "empty" {
				requireError(t, response, 404, constants.APIErrorCodeResourceNotFound)
				if r.calls != 1 {
					t.Fatal("empty body blocked reader")
				}
			} else {
				requireError(t, response, 400, constants.APIErrorCodeInvalidRequest)
				if r.calls != 0 {
					t.Fatal("invalid body reached reader")
				}
			}
			if body.reads != 1 {
				t.Fatalf("body reads=%d", body.reads)
			}
		})
	}
}
func TestSnapshotErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"missing", xerror.ErrExecutionNotFound, 404, constants.APIErrorCodeResourceNotFound},
		{"persistence", xerror.ErrExecutionPersistence, 500, constants.PersistenceFailed},
		{"integrity", xerror.ErrRecoveryIntegrity, 500, constants.PersistenceFailed},
		{"cancelled", context.Canceled, 500, constants.APIErrorCodeInternal},
		{"deadline", context.DeadlineExceeded, 500, constants.APIErrorCodeInternal},
		{"adapter invalid input", xerror.ErrExecutionInvalidInput, 500, constants.APIErrorCodeInternal},
		{"unexpected", errors.New("private driver failure"), 500, constants.APIErrorCodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &reader{call: func(context.Context, string) (execution.Snapshot, error) {
				return execution.Snapshot{Cursor: "partial"}, xerror.WithOperation("controlled_snapshot_read", tc.err)
			}}
			response := httptest.NewRecorder()
			router(r).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+groupID, nil))
			requireError(t, response, tc.status, tc.code)
			if r.calls != 1 || strings.Contains(response.Body.String(), "partial") {
				t.Fatal("partial/error read escaped")
			}
			if tc.code == constants.PersistenceFailed && !strings.Contains(response.Body.String(), constants.APIErrorMessageSnapshotReadFailed) {
				t.Fatal("wrong persistence message")
			}
		})
	}
	response := httptest.NewRecorder()
	router(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/active", nil))
	requireError(t, response, 500, constants.APIErrorCodeInternal)
}
func TestCancelledRequestContextIsNotDetached(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := &reader{call: func(received context.Context, _ string) (execution.Snapshot, error) {
		if received != ctx {
			t.Error("request context detached")
		}
		return execution.Snapshot{}, received.Err()
	}}
	response := httptest.NewRecorder()
	router(r).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/active", nil).WithContext(ctx))
	requireError(t, response, 500, constants.APIErrorCodeInternal)
	if r.calls != 1 {
		t.Fatal("read retried")
	}
}
