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

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type actionController struct {
	calls int
	call  func(context.Context, string, string) (constants.GroupControlDisposition, error)
}

func (c *actionController) submit(ctx context.Context, id, action string) (constants.GroupControlDisposition, error) {
	c.calls++
	return c.call(ctx, id, action)
}
func (c *actionController) Pause(ctx context.Context, id string) (constants.GroupControlDisposition, error) {
	return c.submit(ctx, id, constants.RunActionPause)
}
func (c *actionController) Resume(ctx context.Context, id string) (constants.GroupControlDisposition, error) {
	return c.submit(ctx, id, constants.RunActionResume)
}
func (c *actionController) Cancel(ctx context.Context, id string) (constants.GroupControlDisposition, error) {
	return c.submit(ctx, id, constants.RunActionCancel)
}

type actionAuth struct{}

func (actionAuth) VerifyAuthorization(values []string) bool {
	return len(values) == 1 && values[0] == "Bearer test"
}

func actionRouter(r runs.SnapshotReader, c runs.Controller) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.Use(middleware.CacheControl(), middleware.Authentication(actionAuth{}))
	e.POST("/api/v1/runs/:runGroupId/actions", runs.NewHandler(r, nil, c).Action)
	return e
}

func actionRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+groupID+"/actions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer test")
	return r
}

func TestActionTransportNeverCallsDependenciesOnInvalidInput(t *testing.T) {
	c := &actionController{}
	r := &reader{}
	e := actionRouter(r, c)
	for _, body := range []string{"", "{}", "null", "[]", `{"action":null}`, `{"action":1}`, `{"action":"pause"}`, `{"action":" PAUSE"}`, `{"action":"PAUSE","extra":1}`, `{"action":"PAUSE","action":"CANCEL"}`, `{"action":"PAUSE","\u0061ction":"CANCEL"}`, `{"action":"PAUSE"}{}`, `{"action":"PAUSE` + string([]byte{0xff}) + `"}`} {
		t.Run(body, func(t *testing.T) {
			out := httptest.NewRecorder()
			e.ServeHTTP(out, actionRequest(body))
			requireError(t, out, 400, constants.APIErrorCodeInvalidRequest)
		})
	}
	for _, tc := range []struct {
		name   string
		modify func(*http.Request)
		status int
		code   string
	}{
		{"query", func(r *http.Request) { r.URL.RawQuery = "force=true" }, 400, constants.APIErrorCodeInvalidRequest},
		{"bad ID", func(r *http.Request) { r.URL.Path = "/api/v1/runs/bad/actions" }, 400, constants.APIErrorCodeInvalidRequest},
		{"uppercase ID", func(r *http.Request) { r.URL.Path = "/api/v1/runs/" + strings.ToUpper(groupID) + "/actions" }, 400, constants.APIErrorCodeInvalidRequest},
		{"media", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 400, constants.APIErrorCodeInvalidRequest},
		{"media parameter", func(r *http.Request) { r.Header.Set("Content-Type", "application/json; extra=yes") }, 400, constants.APIErrorCodeInvalidRequest},
		{"charset", func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=ascii") }, 400, constants.APIErrorCodeInvalidRequest},
		{"encoding", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, 400, constants.APIErrorCodeInvalidRequest},
		{"missing body", func(r *http.Request) { r.Body = nil }, 400, constants.APIErrorCodeInvalidRequest},
		{"read error", func(r *http.Request) { r.Body = io.NopCloser(failingBody{}) }, 400, constants.APIErrorCodeInvalidRequest},
		{"declared too large", func(r *http.Request) { r.ContentLength = constants.RunActionMaxContentBytes + 1 }, 413, constants.APIErrorCodePayloadTooLarge},
		{"chunked too large", func(r *http.Request) {
			r.ContentLength = -1
			r.Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", constants.RunActionMaxContentBytes+1)))
		}, 413, constants.APIErrorCodePayloadTooLarge},
		{"auth before invalid body", func(r *http.Request) { r.Header.Del("Authorization"); r.Body = nil }, 401, constants.APIErrorCodeUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := actionRequest(`{"action":"PAUSE"}`)
			tc.modify(req)
			out := httptest.NewRecorder()
			e.ServeHTTP(out, req)
			requireError(t, out, tc.status, tc.code)
		})
	}
	if c.calls != 0 || r.calls != 0 {
		t.Fatal("invalid transport invoked dependencies", c.calls, r.calls)
	}
}

func TestActionDispositionAndSnapshotMapping(t *testing.T) {
	for _, tc := range []struct {
		name, action, saved string
		disposition         constants.GroupControlDisposition
		err, readErr        error
		status              int
		code                string
	}{
		{"pause accepted", "PAUSE", "RUNNING", constants.GroupControlAccepted, nil, nil, 202, ""},
		{"accepted already advanced", "PAUSE", "PAUSED", constants.GroupControlAccepted, nil, nil, 202, ""},
		{"resume accepted", "RESUME", "PAUSED", constants.GroupControlAccepted, nil, nil, 202, ""},
		{"cancel cleanup pending", "CANCEL", "CANCELLED", constants.GroupControlAccepted, nil, nil, 202, ""},
		{"already paused", "PAUSE", "PAUSED", constants.GroupControlAlreadyTarget, nil, nil, 200, ""},
		{"already running", "RESUME", "RUNNING", constants.GroupControlAlreadyTarget, nil, nil, 200, ""},
		{"later action visible", "PAUSE", "RUNNING", constants.GroupControlAlreadyTarget, nil, nil, 200, ""},
		{"retired cancelled", "CANCEL", "CANCELLED", "", xerror.ErrExecutionControlState, nil, 200, ""},
		{"pending resume conflict", "PAUSE", "PAUSED", "", xerror.ErrExecutionControlState, nil, 409, constants.APIErrorCodeInvalidExecutionState},
		{"pending pause conflict", "RESUME", "RUNNING", "", xerror.ErrExecutionControlState, nil, 409, constants.APIErrorCodeInvalidExecutionState},
		{"natural terminal", "CANCEL", "SUCCEEDED", "", xerror.ErrExecutionControlState, nil, 409, constants.APIErrorCodeInvalidExecutionState},
		{"missing", "CANCEL", "", "", xerror.ErrExecutionControlState, xerror.ErrExecutionNotFound, 404, constants.APIErrorCodeResourceNotFound},
		{"accepted missing invariant", "CANCEL", "", constants.GroupControlAccepted, nil, xerror.ErrExecutionNotFound, 500, constants.InternalError},
		{"read failure after acceptance", "CANCEL", "", constants.GroupControlAccepted, nil, xerror.ErrExecutionPersistence, 500, constants.PersistenceFailed},
		{"read commit unknown", "PAUSE", "", constants.GroupControlAccepted, nil, xerror.ErrExecutionCommitUnknown, 500, constants.PersistenceFailed},
		{"wrong snapshot", "PAUSE", "wrong-id", constants.GroupControlAccepted, nil, nil, 500, constants.InternalError},
		{"unknown disposition", "PAUSE", "", constants.GroupControlCommitted, nil, nil, 500, constants.InternalError},
		{"stopping", "CANCEL", "", "", xerror.ErrExecutionStopping, nil, 503, constants.InternalError},
		{"storage failure", "CANCEL", "", "", xerror.ErrExecutionPersistence, nil, 500, constants.PersistenceFailed},
		{"unknown commit", "CANCEL", "", "", xerror.ErrExecutionCommitUnknown, nil, 500, constants.PersistenceFailed},
		{"integrity", "CANCEL", "", "", xerror.ErrRecoveryIntegrity, nil, 500, constants.PersistenceFailed},
		{"error beats disposition", "CANCEL", "", constants.GroupControlAccepted, errors.New("private dependency failure"), nil, 500, constants.InternalError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := &actionController{call: func(actual context.Context, id, action string) (constants.GroupControlDisposition, error) {
				if actual != ctx || id != groupID || action != tc.action {
					t.Fatal("wrong submission identity/context", id, action)
				}
				return tc.disposition, xerror.WithOperation("controlled_submit", tc.err)
			}}
			saved := execution.Snapshot{Cursor: "42", RunGroup: execution.RunGroup{ID: groupID, Status: tc.saved}, DeviceExecutions: []execution.DeviceExecution{}}
			if tc.saved == "wrong-id" {
				saved.RunGroup.ID = "another-group"
			}
			r := &reader{call: func(actual context.Context, id string) (execution.Snapshot, error) {
				if c.calls != 1 || actual != ctx || id != groupID {
					t.Fatal("read before submit/wrong target")
				}
				return saved, xerror.WithOperation("controlled_read", tc.readErr)
			}}
			req := actionRequest(`{"action":"` + tc.action + `"}`).WithContext(ctx)
			if tc.name == "pause accepted" {
				req.Header.Set("Content-Type", "application/json; charset=UTF-8")
				req.Body = io.NopCloser(strings.NewReader(`{"action":"PAUSE"}` + strings.Repeat(" ", constants.RunActionMaxContentBytes-len(`{"action":"PAUSE"}`))))
				req.ContentLength = -1
			}
			out := httptest.NewRecorder()
			actionRouter(r, c).ServeHTTP(out, req)
			if out.Code != tc.status || c.calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", out.Code, c.calls, out.Body.String())
			}
			if tc.status == 409 {
				var got runs.ActionConflictResponse
				if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || got.Error.Code != tc.code || got.Error.Retryable || got.Snapshot.RunGroup.ID != groupID {
					t.Fatal(out.Body.String(), err)
				}
				var fields map[string]json.RawMessage
				json.Unmarshal(out.Body.Bytes(), &fields)
				if len(fields) != 2 {
					t.Fatal("conflict shape", out.Body.String())
				}
			} else if tc.code != "" {
				requireError(t, out, tc.status, tc.code)
			} else {
				want, _ := json.Marshal(saved)
				if out.Body.String() != string(want) {
					t.Fatal("wrong snapshot envelope", out.Body.String())
				}
			}
			shouldRead := tc.err == nil && (tc.disposition == constants.GroupControlAccepted || tc.disposition == constants.GroupControlAlreadyTarget) || errors.Is(tc.err, xerror.ErrExecutionControlState)
			if (r.calls == 1) != shouldRead {
				t.Fatal("wrong read count", r.calls)
			}
		})
	}
}

func TestActionMissingDependenciesAndRequestCancellation(t *testing.T) {
	c := &actionController{call: func(context.Context, string, string) (constants.GroupControlDisposition, error) {
		return constants.GroupControlAccepted, nil
	}}
	for _, e := range []*gin.Engine{actionRouter(nil, c), actionRouter(&reader{}, nil)} {
		out := httptest.NewRecorder()
		e.ServeHTTP(out, actionRequest(`{"action":"CANCEL"}`))
		requireError(t, out, 500, constants.InternalError)
	}
	ctx, cancel := context.WithCancel(t.Context())
	c.call = func(actual context.Context, _ string, _ string) (constants.GroupControlDisposition, error) {
		cancel()
		return constants.GroupControlAccepted, nil
	}
	r := &reader{call: func(actual context.Context, _ string) (execution.Snapshot, error) {
		if actual != ctx || actual.Err() != context.Canceled {
			t.Fatal("request read detached")
		}
		return execution.Snapshot{}, actual.Err()
	}}
	out := httptest.NewRecorder()
	actionRouter(r, c).ServeHTTP(out, actionRequest(`{"action":"CANCEL"}`).WithContext(ctx))
	requireError(t, out, 500, constants.InternalError)
	if c.calls != 1 || r.calls != 1 {
		t.Fatal("accepted action was retried", c.calls, r.calls)
	}
}
