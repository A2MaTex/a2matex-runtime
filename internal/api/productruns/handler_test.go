package productruns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productrun"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type stub struct {
	err         error
	calls       int
	replayed    bool
	disposition constants.GroupControlDisposition
}

func (s *stub) Start(context.Context, productrun.Request) (productrun.Result, error) {
	s.calls++
	return productrun.Result{Snapshot: productrun.Snapshot{RunGroup: execution.RunGroup{ID: testID}}, Replayed: s.replayed}, s.err
}
func (s *stub) ByID(context.Context, string) (productrun.Snapshot, error) {
	s.calls++
	return productrun.Snapshot{RunGroup: execution.RunGroup{ID: testID}}, s.err
}
func (s *stub) Active(context.Context) (productrun.Snapshot, error) {
	return s.ByID(context.Background(), testID)
}
func (s *stub) Action(context.Context, string, string) (productrun.Snapshot, constants.GroupControlDisposition, error) {
	snap, err := s.ByID(context.Background(), testID)
	return snap, s.disposition, err
}

const testID = "11111111-1111-4111-8111-111111111111"

func body() string {
	return `{"requestId":"` + uuid.NewString() + `","runtimeSessionId":"` + uuid.NewString() + `","authorization":{"lease":"lease","keyEnvelope":{"releaseId":"` + testID + `","algorithm":"RSA-OAEP-SHA256","runtimeSessionId":"` + testID + `","runtimeKeyThumbprint":"hash","encryptedKey":"encrypted"},"serverTime":"2026-11-27T10:00:00.000Z"},"devices":[{"adbSerial":"device-a","inputs":{}}]}`
}
func router(s *stub) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	h := NewHandler(s)
	r.POST("/product-runs", h.Start)
	r.GET("/product-runs/active", h.Active)
	r.GET("/product-runs/:runGroupId", h.ByID)
	r.POST("/product-runs/:runGroupId/actions", h.Action)
	return r
}
func call(r http.Handler, method, path, body, root string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if root != "" {
		req.Header.Set(constants.ProductPackageDirectoryHeader, root)
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}
func TestStrictProductRequests(t *testing.T) {
	root := url.PathEscape(filepath.Join(t.TempDir(), "products"))
	for _, tc := range []struct {
		name, body, path, root string
		want                   int
	}{
		{"valid", body(), "/product-runs", root, 201},
		{"no root", body(), "/product-runs", "", 400},
		{"query", body(), "/product-runs?", root, 400},
		{"unknown", strings.Replace(body(), `"devices":`, `"userId":"private","devices":`, 1), "/product-runs", root, 400},
		{"duplicate input", strings.Replace(body(), `"inputs":{}`, `"inputs":{"a":1,"\u0061":2}`, 1), "/product-runs", root, 400},
		{"root array", "[]", "/product-runs", root, 400},
		{"null envelope", strings.Replace(body(), `"keyEnvelope":{`, `"keyEnvelope":null,"extra":{`, 1), "/product-runs", root, 400},
		{"wrong device", strings.Replace(body(), "device-a", "bad serial", 1), "/product-runs", root, 400},
		{"empty devices", strings.Replace(body(), `[{"adbSerial":"device-a","inputs":{}}]`, `[]`, 1), "/product-runs", root, 400},
		{"oversize", strings.Repeat(" ", constants.RunRequestMaxContentBytes+1), "/product-runs", root, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &stub{}
			out := call(router(service), "POST", tc.path, tc.body, tc.root)
			if out.Code != tc.want {
				t.Fatal(out.Code, out.Body.String())
			}
			if tc.want >= 400 && service.calls != 0 {
				t.Fatal("invalid called service")
			}
		})
	}
}
func TestProductFailureMappingAndControls(t *testing.T) {
	root := url.PathEscape(t.TempDir())
	for _, tc := range []struct {
		err  error
		want int
		code string
	}{
		{xerror.ErrProductRunConflict, 409, "IDEMPOTENCY_CONFLICT"}, {xerror.ErrProductRunUnavailable, 503, "INTERNAL_ERROR"}, {xerror.ErrProductSessionUnavailable, 409, "AUTHORIZATION_SESSION_UNAVAILABLE"}, {xerror.ErrExecutionLeaseInvalid, 422, "AUTHORIZATION_INVALID"}, {xerror.ErrProductKeyEnvelopeInvalid, 422, "AUTHORIZATION_INVALID"}, {xerror.ErrProductPackageInvalid, 422, "VALIDATION_FAILED"}, {xerror.ErrExecutionCommitUnknown, 500, "PERSISTENCE_FAILED"}, {xerror.ErrExecutionNotFound, 404, "RESOURCE_NOT_FOUND"}, {&productrun.DeviceRejection{Code: constants.DeviceOffline}, 422, constants.DeviceOffline},
	} {
		s := &stub{err: tc.err}
		out := call(router(s), "POST", "/product-runs", body(), root)
		var v struct {
			Error struct {
				Code, Message string
				Retryable     bool
			}
		}
		if json.Unmarshal(out.Body.Bytes(), &v) != nil || out.Code != tc.want || v.Error.Code != tc.code || v.Error.Retryable {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	for _, tc := range []struct {
		err         error
		disposition constants.GroupControlDisposition
		status      int
	}{
		{nil, constants.GroupControlAccepted, 202}, {nil, constants.GroupControlAlreadyTarget, 200}, {xerror.ErrExecutionControlState, "", 409}, {xerror.ErrProductAuthorizationExpired, "", 409},
	} {
		s := &stub{err: tc.err, disposition: tc.disposition}
		out := call(router(s), "POST", "/product-runs/"+testID+"/actions", `{"action":"RESUME"}`, "")
		if out.Code != tc.status {
			t.Fatal(out.Code, out.Body.String())
		}
		if tc.err != nil && !strings.Contains(out.Body.String(), `"snapshot"`) {
			t.Fatal("no safe conflict")
		}
	}
	for _, path := range []string{"/product-runs/active?", "/product-runs/bad", "/product-runs/" + testID + "?x=1"} {
		out := call(router(&stub{}), "GET", path, "", "")
		if out.Code != 400 {
			t.Fatal(path, out.Code)
		}
	}
}
