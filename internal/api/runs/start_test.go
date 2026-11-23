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
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
)

const startBody = `{"requestId":"ab000000-0000-4000-8000-000000000001","runtimeIr":{},"devices":[{"adbSerial":"device","inputs":{}}]}`

type starterFunc func(context.Context, runadmission.Request) (runadmission.StartResult, error)

func (f starterFunc) Start(ctx context.Context, r runadmission.Request) (runadmission.StartResult, error) {
	return f(ctx, r)
}

func startRouter(starter runs.Starter) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(middleware.CacheControl())
	router.POST("/api/v1/runs", runs.NewHandler(nil, starter, nil).Start)
	return router
}

func postStart(router http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r := httptest.NewRecorder()
	router.ServeHTTP(r, req)
	return r
}

func TestStartStrictTransportAndBounds(t *testing.T) {
	calls := 0
	router := startRouter(starterFunc(func(context.Context, runadmission.Request) (runadmission.StartResult, error) {
		calls++
		return runadmission.StartResult{}, errors.New("private")
	}))
	cases := []string{"", "null", "[]", startBody + `{}`, strings.Replace(startBody, `"requestId"`, `"RequestId"`, 1),
		strings.Replace(startBody, `"runtimeIr":{}`, `"runtimeIr":null`, 1),
		strings.Replace(startBody, `"runtimeIr":{}`, `"runtimeIr":{},"runtimeIr":{}`, 1),
		strings.Replace(startBody, `"runtimeIr":{}`, `"runtimeIr":{"nested":[{"a":1,"\u0061":2}]}`, 1),
		strings.Replace(startBody, `"devices":[`, `"devices":null,"other":[`, 1),
		strings.Replace(startBody, `"inputs":{}`, `"inputs":null`, 1),
		strings.Replace(startBody, `"inputs":{}`, `"inputs":{},"extra":1`, 1),
		strings.Replace(startBody, "ab000000", "AB000000", 1),
		strings.Replace(startBody, `"runtimeIr":{}`, `"runtimeIr":{"a":"`+string([]byte{0xff})+`"}`, 1)}
	for _, body := range cases {
		requireError(t, postStart(router, body), 400, constants.APIErrorCodeInvalidRequest)
	}
	for _, modify := range []func(*http.Request){
		func(r *http.Request) { r.URL.RawQuery = "limit=1" },
		func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=ascii") },
		func(r *http.Request) { r.Body = io.NopCloser(failingBody{}) },
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(startBody))
		req.Header.Set("Content-Type", "application/json")
		modify(req)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		requireError(t, response, 400, constants.APIErrorCodeInvalidRequest)
	}
	if calls != 0 {
		t.Fatal("invalid transport reached start use case")
	}
	for _, streamed := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(strings.Repeat(" ", constants.RunRequestMaxContentBytes+1)))
		req.Header.Set("Content-Type", "application/json")
		if streamed {
			req.ContentLength = -1
		}
		r := httptest.NewRecorder()
		router.ServeHTTP(r, req)
		requireError(t, r, 413, constants.APIErrorCodePayloadTooLarge)
	}
	for _, size := range []int{constants.RuntimeIRMaxContentBytes, constants.RuntimeIRMaxContentBytes + 1} {
		ir := "{" + strings.Repeat(" ", size-2) + "}"
		response := postStart(router, strings.Replace(startBody, `"runtimeIr":{}`, `"runtimeIr":`+ir, 1))
		status := 500
		if size > constants.RuntimeIRMaxContentBytes {
			status = 413
		}
		if response.Code != status {
			t.Fatalf("IR bound: %d %s", response.Code, response.Body.String())
		}
	}
	exact := startBody + strings.Repeat(" ", constants.RunRequestMaxContentBytes-len(startBody))
	if r := postStart(router, exact); r.Code != 500 || calls != 2 {
		t.Fatalf("exact body bound: status=%d calls=%d", r.Code, calls)
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("private read failure") }

func TestStartResponseClassification(t *testing.T) {
	for _, disposition := range []constants.AdmissionDisposition{constants.AdmissionCreated, constants.AdmissionExisting} {
		snapshot := execution.Snapshot{Cursor: "13", RunGroup: execution.RunGroup{ID: groupID, Status: constants.ExecutionSucceeded}}
		router := startRouter(starterFunc(func(ctx context.Context, r runadmission.Request) (runadmission.StartResult, error) {
			if r.RequestID != groupID || len(r.Devices) != 1 {
				t.Fatal("request mapping")
			}
			return runadmission.StartResult{Disposition: disposition, Snapshot: &snapshot}, nil
		}))
		r := postStart(router, startBody)
		status := 200
		if disposition == constants.AdmissionCreated {
			status = 201
		}
		want, _ := json.Marshal(snapshot)
		if r.Code != status || r.Body.String() != string(want) {
			t.Fatalf("snapshot response: %d %s", r.Code, r.Body.String())
		}
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{xerror.ErrRunAdmissionInvalid, 400, constants.APIErrorCodeInvalidRequest},
		{xerror.ErrRunAdmissionContract, 500, constants.InternalError},
		{xerror.ErrExecutionCommitUnknown, 500, constants.PersistenceFailed},
		{xerror.ErrExecutionPersistence, 500, constants.PersistenceFailed},
		{xerror.ErrExecutionStopping, 503, constants.InternalError},
		{xerror.ErrADBDiscovery, 503, constants.ADBUnavailable},
		{xerror.ErrADBReadiness, 503, constants.ADBUnavailable},
		{context.Canceled, 500, constants.InternalError},
	} {
		r := postStart(startRouter(starterFunc(func(context.Context, runadmission.Request) (runadmission.StartResult, error) {
			return runadmission.StartResult{}, tc.err
		})), startBody)
		var body runs.AdmissionErrorResponse
		if json.Unmarshal(r.Body.Bytes(), &body) != nil || r.Code != tc.status || body.Error.Code != tc.code || body.Validation != nil {
			t.Fatalf("classification: %d %s", r.Code, r.Body.String())
		}
	}
	for _, diagnostics := range []bool{false, true} {
		rejection := &runadmission.Rejection{FailureCode: constants.DeviceNotReady}
		if diagnostics {
			result := validation.BuildResult([]validation.Diagnostic{validation.NewDiagnostic(validation.CodeRuntimeIRContractInvalid, validation.Path{"runtimeIr"}, "", "")}, nil)
			rejection = &runadmission.Rejection{FailureCode: constants.ValidationFailed, Validation: &result}
		}
		r := postStart(startRouter(starterFunc(func(context.Context, runadmission.Request) (runadmission.StartResult, error) {
			return runadmission.StartResult{Rejection: rejection}, nil
		})), startBody)
		var body map[string]json.RawMessage
		if json.Unmarshal(r.Body.Bytes(), &body) != nil || r.Code != 422 || (body["validation"] != nil) != diagnostics {
			t.Fatalf("rejection: %d %s", r.Code, r.Body.String())
		}
	}
	r := postStart(startRouter(starterFunc(func(context.Context, runadmission.Request) (runadmission.StartResult, error) {
		return runadmission.StartResult{Disposition: constants.AdmissionBusy}, nil
	})), startBody)
	if r.Code != 409 {
		t.Fatalf("busy: %d %s", r.Code, r.Body.String())
	}
}
