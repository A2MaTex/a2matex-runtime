package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sessionapi "github.com/A2MaTex/a2matex-runtime/internal/api/productsessions"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const prepareBody = `{"requestId":"33333333-3333-4333-8333-333333333333","purpose":"START","package":{"packageRef":"11111111-1111-4111-8111-111111111111/package.a2m","packageHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","launcherVersion":"0.1.0"}}`

type sessionPreparerFunc func(context.Context, productsession.Request) (productsession.Prepared, error)

func (f sessionPreparerFunc) Prepare(ctx context.Context, r productsession.Request) (productsession.Prepared, error) {
	return f(ctx, r)
}

func sessionTrust(t *testing.T) *executionlease.Verifier {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := executionlease.New(executionlease.Options{VerificationKeys: map[string]string{
		"HTTP.Test": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return trust
}

func sessionRouter(t *testing.T, preparer sessionapi.Preparer, trust *executionlease.Verifier, output io.Writer) http.Handler {
	t.Helper()
	handler, err := sessionapi.NewHandler(preparer, trust)
	if err != nil {
		t.Fatal(err)
	}
	return newRouter(buildinfo.Info{}, exactAuthorizationVerifier{}, nil, nil, nil, nil, nil, nil, nil,
		slog.New(slog.NewJSONHandler(output, nil)), nil, handler, nil)
}

func preparationRequest(body, root string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/product-sessions", strings.NewReader(body))
	r.Header.Set("Authorization", validAuthorization)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(constants.ProductPackageDirectoryHeader, url.PathEscape(root))
	return r
}

func preparedResponse(r productsession.Request) productsession.Prepared {
	return productsession.Prepared{RequestID: r.RequestID, Purpose: r.Purpose,
		ExecutionID: "55555555-5555-4555-8555-555555555555", RuntimeSessionID: "66666666-6666-4666-8666-666666666666",
		RuntimePublicKey: "PUBLIC-KEY", RuntimeKeyThumbprint: strings.Repeat("b", 64), ExpiresAt: "2026-11-27T12:05:00.000Z",
		Package: packagevalidation.PublicPackage{ProductID: "11111111-1111-4111-8111-111111111111", ReleaseID: "22222222-2222-4222-8222-222222222222", Version: "1.0.0", PackageHash: r.PackageHash}}
}

func TestProductSessionNewReplayAndPrivateShape(t *testing.T) {
	trust, root := sessionTrust(t), t.TempDir()
	var calls int
	var logs bytes.Buffer
	router := sessionRouter(t, sessionPreparerFunc(func(_ context.Context, r productsession.Request) (productsession.Prepared, error) {
		calls++
		if r.RunGroupID != "" || r.Purpose != constants.ProductSessionStart || len(r.DirectoryHeaderValues) != 1 || r.LauncherVersion != "0.1.0" {
			t.Fatal("request mapping")
		}
		p := preparedResponse(r)
		p.Replayed = calls > 1
		return p, nil
	}), trust, &logs)
	var before string
	for _, status := range []int{201, 200} {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, preparationRequest(prepareBody, root))
		if out.Code != status || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("success status", out.Code, out.Body)
		}
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(out.Body.Bytes(), &shape); err != nil || len(shape) != 9 || string(shape["previousLeaseFamilyId"]) != "null" {
			t.Fatal("public shape", err)
		}
		if before != "" && before != out.Body.String() {
			t.Fatal("replay changed public fields")
		}
		before = out.Body.String()
	}
	if strings.Contains(logs.String(), root) || strings.Contains(logs.String(), "PUBLIC-KEY") || strings.Contains(logs.String(), "valid-local-session") {
		t.Fatal("payload logged")
	}
}

func TestProductSessionStrictTransportAndSelection(t *testing.T) {
	trust, root := sessionTrust(t), t.TempDir()
	for _, test := range []struct {
		name, body string
		edit       func(*http.Request)
		status     int
	}{
		{"missing auth", prepareBody, func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"duplicate auth", prepareBody, func(r *http.Request) { r.Header.Add("Authorization", validAuthorization) }, 401},
		{"query", prepareBody, func(r *http.Request) { r.URL.RawQuery = "private=value" }, 400},
		{"empty query", prepareBody, func(r *http.Request) { r.URL.ForceQuery = true }, 400},
		{"missing type", prepareBody, func(r *http.Request) { r.Header.Del("Content-Type") }, 400},
		{"duplicate type", prepareBody, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, 400},
		{"wrong charset", prepareBody, func(r *http.Request) { r.Header.Set("Content-Type", "application/json;charset=ascii") }, 400},
		{"encoding", prepareBody, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, 400},
		{"missing root", prepareBody, func(r *http.Request) { r.Header.Del(constants.ProductPackageDirectoryHeader) }, 400},
		{"duplicate root", prepareBody, func(r *http.Request) { r.Header.Add(constants.ProductPackageDirectoryHeader, "%2Fprivate") }, 400},
		{"bad root escape", prepareBody, func(r *http.Request) { r.Header.Set(constants.ProductPackageDirectoryHeader, "%ZZ") }, 400},
		{"null", "null", nil, 400}, {"array", "[]", nil, 400}, {"empty", "", nil, 400},
		{"trailing", prepareBody + " {}", nil, 400}, {"invalid utf8", prepareBody + string([]byte{255}), nil, 400},
		{"duplicate root key", strings.TrimSuffix(prepareBody, "}") + `,"purp\u006fse":"START"}`, nil, 400},
		{"duplicate nested key", strings.Replace(prepareBody, `"launcherVersion":"0.1.0"`, `"launcherVersion":"0.1.0","package\u0052ef":"other"`, 1), nil, 400},
		{"unknown nested key", strings.Replace(prepareBody, `"launcherVersion":"0.1.0"`, `"launcherVersion":"0.1.0","releaseId":"x"`, 1), nil, 400},
		{"unknown root key", strings.TrimSuffix(prepareBody, "}") + `,"inputs":{}}`, nil, 400},
		{"start run id null", strings.TrimSuffix(prepareBody, "}") + `,"runGroupId":null}`, nil, 400},
		{"missing reauth id", strings.Replace(prepareBody, "START", "REAUTHORIZE", 1), nil, 400},
		{"unknown purpose", strings.Replace(prepareBody, "START", "start", 1), nil, 400},
		{"bad UUID", strings.Replace(prepareBody, "33333333-3333-4333-8333-333333333333", "invalid", 1), nil, 400},
		{"bad hash", strings.Replace(prepareBody, strings.Repeat("a", 64), strings.Repeat("A", 64), 1), nil, 400},
		{"number version", strings.Replace(prepareBody, `"0.1.0"`, `123`, 1), nil, 400},
		{"null version", strings.Replace(prepareBody, `"0.1.0"`, `null`, 1), nil, 400},
		{"old package layout", strings.Replace(prepareBody, "package.a2m", "release/package.a2m", 1), nil, 400},
		{"traversal", strings.Replace(prepareBody, "package.a2m", "../package.a2m", 1), nil, 400},
		{"declared oversize", strings.Repeat(" ", constants.ProductSessionRequestMaxBytes+1), nil, 413},
		{"streamed oversize", strings.Repeat(" ", constants.ProductSessionRequestMaxBytes+1), func(r *http.Request) { r.ContentLength = -1 }, 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			var logs bytes.Buffer
			router := sessionRouter(t, sessionPreparerFunc(func(context.Context, productsession.Request) (productsession.Prepared, error) {
				calls++
				return productsession.Prepared{}, nil
			}), trust, &logs)
			r := preparationRequest(test.body, root)
			if test.edit != nil {
				test.edit(r)
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, r)
			if out.Code != test.status || calls != 0 || !strings.Contains(logs.String(), `"level":"ERROR"`) {
				t.Fatal("transport accepted or missing error context", out.Code, out.Body)
			}
		})
	}
}

func TestProductSessionFailureMappingAndDisabledFeature(t *testing.T) {
	trust, root := sessionTrust(t), t.TempDir()
	for _, test := range []struct {
		name, code string
		cause      error
		status     int
	}{
		{"invalid", "INVALID_REQUEST", xerror.ErrProductSessionInvalid, 400},
		{"location", "INVALID_REQUEST", xerror.ErrProductPackageLocation, 400},
		{"missing", "RESOURCE_NOT_FOUND", xerror.ErrProductPackageNotFound, 404},
		{"package", "VALIDATION_FAILED", xerror.ErrProductPackageInvalid, 422},
		{"incompatible", "VALIDATION_FAILED", xerror.ErrProductPackageIncompatible, 422},
		{"busy", "RUNTIME_BUSY", xerror.ErrProductSessionBusy, 409},
		{"conflict", "IDEMPOTENCY_CONFLICT", xerror.ErrProductSessionConflict, 409},
		{"unavailable session", "AUTHORIZATION_SESSION_UNAVAILABLE", xerror.ErrProductSessionUnavailable, 409},
		{"reauthorize state", "INVALID_EXECUTION_STATE", xerror.ErrExecutionControlState, 409},
		{"reauthorize missing", "RESOURCE_NOT_FOUND", xerror.ErrExecutionNotFound, 404},
		{"IO", "INTERNAL_ERROR", xerror.ErrProductPackageUnavailable, 503},
		{"cancel", "INTERNAL_ERROR", context.Canceled, 503},
		{"deadline", "INTERNAL_ERROR", context.DeadlineExceeded, 503},
		{"key generator", "INTERNAL_ERROR", xerror.ErrProductSessionKeyGeneration, 500},
		{"unexpected", "INTERNAL_ERROR", errors.New("PRIVATE failure text"), 500},
		{"empty success", "INTERNAL_ERROR", nil, 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			router := sessionRouter(t, sessionPreparerFunc(func(context.Context, productsession.Request) (productsession.Prepared, error) {
				return productsession.Prepared{}, test.cause
			}), trust, &logs)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, preparationRequest(prepareBody, root))
			var response struct {
				Error struct {
					Code      string
					Retryable bool
				}
			}
			if json.Unmarshal(out.Body.Bytes(), &response) != nil || out.Code != test.status || response.Error.Code != test.code || response.Error.Retryable {
				t.Fatal("failure mapping", out.Code, out.Body)
			}
			if strings.Contains(logs.String()+out.Body.String(), "PRIVATE") || strings.Contains(logs.String(), root) {
				t.Fatal("private failure details leaked")
			}
		})
	}
	for _, body := range []string{prepareBody, `{}`} {
		router := sessionRouter(t, sessionPreparerFunc(func(context.Context, productsession.Request) (productsession.Prepared, error) {
			t.Error("disabled feature invoked preparation")
			return productsession.Prepared{}, nil
		}), nil, io.Discard)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, preparationRequest(body, root))
		want := 503
		if body == `{}` {
			want = 400
		}
		if out.Code != want {
			t.Fatal("disabled precedence", out.Code, out.Body)
		}
	}
}

func TestProductSessionCancellationAndReauthorizationMapping(t *testing.T) {
	trust, root := sessionTrust(t), t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	router := sessionRouter(t, sessionPreparerFunc(func(_ context.Context, r productsession.Request) (productsession.Prepared, error) {
		cancel() // Publication can win; HTTP must not manufacture a live response.
		return preparedResponse(r), nil
	}), trust, io.Discard)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, preparationRequest(prepareBody, root).WithContext(ctx))
	if out.Code != 503 {
		t.Fatal("cancellation became success", out.Code)
	}
	body := strings.Replace(prepareBody, "START", "REAUTHORIZE", 1)
	body = strings.TrimSuffix(body, "}") + `,"runGroupId":"55555555-5555-4555-8555-555555555555"}`
	router = sessionRouter(t, sessionPreparerFunc(func(_ context.Context, r productsession.Request) (productsession.Prepared, error) {
		if r.Purpose != constants.ProductSessionReauthorize || r.RunGroupID != "55555555-5555-4555-8555-555555555555" {
			t.Error("reauthorize mapping")
		}
		return productsession.Prepared{}, xerror.ErrExecutionControlState
	}), trust, io.Discard)
	out = httptest.NewRecorder()
	router.ServeHTTP(out, preparationRequest(body, root))
	if out.Code != 409 {
		t.Fatal("reauthorize wire branch", out.Code, out.Body)
	}
}
