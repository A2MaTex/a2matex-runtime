package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	packageapi "github.com/A2MaTex/a2matex-runtime/internal/api/productpackages"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const packageSchemaPath = "/api/v1/product-packages/input-schema"
const packageSchemaBody = `{"packageRef":"11111111-1111-4111-8111-111111111111/package.a2m","launcherVersion":"0.1.0"}`

type packageInspectorFunc func(context.Context, packagevalidation.InspectionRequest) (*packagevalidation.Inspection, error)

func (f packageInspectorFunc) Inspect(ctx context.Context, r packagevalidation.InspectionRequest) (*packagevalidation.Inspection, error) {
	return f(ctx, r)
}

func packageRouter(inspector packageapi.Inspector, logs io.Writer) http.Handler {
	return newRouter(buildinfo.Info{Version: "0.1.0"}, exactAuthorizationVerifier{}, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(logs, nil)), packageapi.NewHandler(inspector))
}
func packageRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, packageSchemaPath, strings.NewReader(body))
	r.Header.Set("Authorization", validAuthorization)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(constants.ProductPackageDirectoryHeader, "%2FPRIVATE%2Fpackages")
	return r
}

func TestPackageSchemaStrictTransport(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		edit       func(*http.Request)
		status     int
	}{
		{name: "valid", body: packageSchemaBody, status: 200},
		{name: "utf8 charset", body: packageSchemaBody, edit: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }, status: 200},
		{name: "missing authorization", body: packageSchemaBody, edit: func(r *http.Request) { r.Header.Del("Authorization") }, status: 401},
		{name: "duplicate authorization", body: packageSchemaBody, edit: func(r *http.Request) { r.Header.Add("Authorization", validAuthorization) }, status: 401},
		{name: "query", body: packageSchemaBody, edit: func(r *http.Request) { r.URL.RawQuery = "secret=PRIVATE" }, status: 400},
		{name: "empty query", body: packageSchemaBody, edit: func(r *http.Request) { r.URL.ForceQuery = true }, status: 400},
		{name: "missing type", body: packageSchemaBody, edit: func(r *http.Request) { r.Header.Del("Content-Type") }, status: 400},
		{name: "duplicate type", body: packageSchemaBody, edit: func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, status: 400},
		{name: "wrong type", body: packageSchemaBody, edit: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, status: 400},
		{name: "encoding", body: packageSchemaBody, edit: func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, status: 400},
		{name: "null", body: "null", status: 400}, {name: "array", body: "[]", status: 400},
		{name: "missing fields", body: "{}", status: 400},
		{name: "trailing value", body: packageSchemaBody + " {}", status: 400},
		{name: "duplicate escaped key", body: strings.TrimSuffix(packageSchemaBody, "}") + `,"package\u0052ef":"PRIVATE"}`, status: 400},
		{name: "unknown hash", body: strings.TrimSuffix(packageSchemaBody, "}") + `,"packageHash":"PRIVATE"}`, status: 400},
		{name: "redundant ID", body: strings.TrimSuffix(packageSchemaBody, "}") + `,"productId":"PRIVATE"}`, status: 400},
		{name: "wrong field type", body: `{"packageRef":{},"launcherVersion":"0.1.0"}`, status: 400},
		{name: "empty field", body: `{"packageRef":"","launcherVersion":"0.1.0"}`, status: 400},
		{name: "invalid utf8", body: packageSchemaBody + string([]byte{255}), status: 400},
		{name: "oversize", body: strings.Repeat(" ", constants.ProductPackageSchemaRequestMaxBytes+1), status: 413},
		{name: "streamed oversize", body: strings.Repeat(" ", constants.ProductPackageSchemaRequestMaxBytes+1), edit: func(r *http.Request) { r.ContentLength = -1 }, status: 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var logs bytes.Buffer
			router := packageRouter(packageInspectorFunc(func(ctx context.Context, r packagevalidation.InspectionRequest) (*packagevalidation.Inspection, error) {
				calls++
				if r.LauncherVersion != "0.1.0" || !strings.HasSuffix(r.PackageRef, ".a2m") || len(r.DirectoryHeaderValues) != 1 {
					t.Fatal("request mapping")
				}
				return &packagevalidation.Inspection{Package: packagevalidation.PublicPackage{ProductID: "product", ReleaseID: "release", Version: "1.0.0", PackageHash: "hash"}, InputSchema: json.RawMessage(`{"type":"object"}`)}, nil
			}), &logs)
			r := packageRequest(tc.body)
			if tc.edit != nil {
				tc.edit(r)
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, r)
			if out.Code != tc.status || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d body=%s", out.Code, out.Body)
			}
			wantCalls := 0
			if tc.status == 200 {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("inspection calls=%d", calls)
			}
			if strings.Contains(logs.String(), "PRIVATE") || strings.Contains(logs.String(), "valid-local-session") {
				t.Fatal("sensitive request in logs")
			}
			if tc.status >= 400 && !strings.Contains(logs.String(), `"level":"ERROR"`) {
				t.Fatal("missing error log")
			}
		})
	}
}

func TestPackageSchemaFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cause     error
		status    int
		code      string
		retryable bool
	}{
		{"location", xerror.ErrProductPackageLocation, 400, "INVALID_REQUEST", false},
		{"selection", xerror.ErrProductPackageSelection, 400, "INVALID_REQUEST", false},
		{"missing", xerror.ErrProductPackageNotFound, 404, "RESOURCE_NOT_FOUND", false},
		{"invalid", xerror.ErrProductPackageInvalid, 422, "VALIDATION_FAILED", false},
		{"incompatible", xerror.ErrProductPackageIncompatible, 422, "RUNTIME_INCOMPATIBLE", false},
		{"unavailable", xerror.ErrProductPackageUnavailable, 503, "INTERNAL_ERROR", true},
		{"canceled", context.Canceled, 503, "INTERNAL_ERROR", true},
		{"deadline", context.DeadlineExceeded, 503, "INTERNAL_ERROR", true},
		{"unexpected", errors.New("PRIVATE underlying error"), 500, "INTERNAL_ERROR", false},
		{"nil success", nil, 500, "INTERNAL_ERROR", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			router := packageRouter(packageInspectorFunc(func(context.Context, packagevalidation.InspectionRequest) (*packagevalidation.Inspection, error) {
				return nil, tc.cause
			}), &logs)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, packageRequest(packageSchemaBody))
			var body struct {
				Error struct {
					Code      string
					Retryable bool
				}
			}
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if out.Code != tc.status || body.Error.Code != tc.code || body.Error.Retryable != tc.retryable {
				t.Fatalf("status=%d body=%s", out.Code, out.Body)
			}
			if strings.Contains(out.Body.String()+logs.String(), "PRIVATE") {
				t.Fatal("error leaked private data")
			}
		})
	}
}

func TestPackageSchemaRealPackage(t *testing.T) {
	raw, err := os.ReadFile("../platform/productpackage/testdata/j1-valid.a2m")
	if err != nil {
		t.Fatal(err)
	}
	var request packageapi.InputSchemaRequest
	if err := json.Unmarshal([]byte(packageSchemaBody), &request); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(request.PackageRef))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var entries []registry.Registration
	for _, kind := range []string{"android.tap", "android.screenshot"} {
		definition, _ := definitions.CompilationDefinition(kind)
		entries = append(entries, registry.Registration{Type: kind, RuntimeHandler: definition.RuntimeHandler, Run: func(context.Context, registry.Invocation) (map[string]any, error) {
			t.Error("inspection dispatched a node")
			return nil, nil
		}})
	}
	handlers, err := registry.New(definitions, entries)
	if err != nil {
		t.Fatal(err)
	}
	inspector, err := packagevalidation.New(definitions, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	router := packageRouter(inspector, io.Discard)
	call := func(body string, duplicateHeader bool) *httptest.ResponseRecorder {
		r := packageRequest(body)
		r.Header.Set(constants.ProductPackageDirectoryHeader, strings.ReplaceAll(url.QueryEscape(root), "+", "%20"))
		if duplicateHeader {
			r.Header.Add(constants.ProductPackageDirectoryHeader, r.Header.Get(constants.ProductPackageDirectoryHeader))
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	for range 2 {
		out := call(packageSchemaBody, false)
		var body packageapi.InputSchemaResponse
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &body) != nil {
			t.Fatalf("status=%d body=%s", out.Code, out.Body)
		}
		hash := sha256.Sum256(raw)
		if body.Package.ProductID != "11111111-1111-4111-8111-111111111111" || body.Package.ReleaseID != "22222222-2222-4222-8222-222222222222" || body.Package.Version != "1.0.0" || body.Package.PackageHash != hex.EncodeToString(hash[:]) || !json.Valid(body.InputSchema) {
			t.Fatal("response mapping")
		}
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(out.Body.Bytes(), &shape); err != nil || len(shape) != 2 {
			t.Fatal("unexpected response fields")
		}
		for _, secret := range []string{root, "workflow.enc", "nonce", "manifest", "AQIDBAUGBwgJCgsM"} {
			if strings.Contains(out.Body.String(), secret) {
				t.Fatalf("private response field %q", secret)
			}
		}
	}
	if out := call(packageSchemaBody, true); out.Code != 400 {
		t.Fatalf("duplicate root: %d", out.Code)
	}
	if out := call(strings.Replace(packageSchemaBody, "0.1.0", "0.0.1", 1), false); out.Code != 422 {
		t.Fatalf("old launcher: %d", out.Code)
	}
	if err := os.WriteFile(path, []byte("PRIVATE invalid ZIP"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := call(packageSchemaBody, false); out.Code != 422 {
		t.Fatalf("invalid ZIP: %d", out.Code)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if out := call(packageSchemaBody, false); out.Code != 404 {
		t.Fatalf("missing package: %d", out.Code)
	}
}
