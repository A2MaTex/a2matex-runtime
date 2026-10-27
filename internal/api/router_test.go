package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const (
	validAuthorization = "Bearer valid-local-session"

	invalidRequestBody = `{"error":{"code":"INVALID_REQUEST",` +
		`"message":"request is invalid","retryable":false}}`
	unauthorizedBody = `{"error":{"code":"UNAUTHORIZED",` +
		`"message":"local session token is missing or invalid","retryable":false}}`
	notFoundBody = `{"error":{"code":"RESOURCE_NOT_FOUND",` +
		`"message":"resource was not found","retryable":false}}`
	internalErrorBody = `{"error":{"code":"INTERNAL_ERROR",` +
		`"message":"internal server error","retryable":false}}`
)

type exactAuthorizationVerifier struct{}

func (exactAuthorizationVerifier) VerifyAuthorization(values []string) bool {
	return len(values) == 1 && values[0] == validAuthorization
}

func TestRouterContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		method        string
		target        string
		authorization []string
		cookie        string
		wantStatus    int
		wantBody      string
		wantNoStore   bool
	}{
		{
			name:       "public health",
			method:     http.MethodGet,
			target:     constants.LocalAPIHealthPath,
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"UP"}`,
		},
		{
			name:          "health ignores authorization",
			method:        http.MethodGet,
			target:        constants.LocalAPIHealthPath,
			authorization: []string{"malformed"},
			wantStatus:    http.StatusOK,
			wantBody:      `{"status":"UP"}`,
		},
		{
			name:        "health rejects query",
			method:      http.MethodGet,
			target:      constants.LocalAPIHealthPath + "?probe=true",
			wantStatus:  http.StatusBadRequest,
			wantBody:    invalidRequestBody,
			wantNoStore: true,
		},
		{
			name:        "runtime information requires authorization",
			method:      http.MethodGet,
			target:      constants.LocalAPIRuntimeInfoPath,
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:          "runtime information rejects wrong authorization",
			method:        http.MethodGet,
			target:        constants.LocalAPIRuntimeInfoPath,
			authorization: []string{"Bearer wrong"},
			wantStatus:    http.StatusUnauthorized,
			wantBody:      unauthorizedBody,
			wantNoStore:   true,
		},
		{
			name:          "runtime information rejects duplicate authorization",
			method:        http.MethodGet,
			target:        constants.LocalAPIRuntimeInfoPath,
			authorization: []string{validAuthorization, validAuthorization},
			wantStatus:    http.StatusUnauthorized,
			wantBody:      unauthorizedBody,
			wantNoStore:   true,
		},
		{
			name:        "query token does not authenticate",
			method:      http.MethodGet,
			target:      constants.LocalAPIRuntimeInfoPath + "?token=valid-local-session",
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:        "cookie token does not authenticate",
			method:      http.MethodGet,
			target:      constants.LocalAPIRuntimeInfoPath,
			cookie:      "token=valid-local-session",
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:          "authenticated runtime information",
			method:        http.MethodGet,
			target:        constants.LocalAPIRuntimeInfoPath,
			authorization: []string{validAuthorization},
			wantStatus:    http.StatusOK,
			wantBody:      `{"runtimeVersion":"1.2.3","runtimeIrFormatVersions":[1],"supportedNodes":[]}`,
			wantNoStore:   true,
		},
		{
			name:        "unmatched route is protected",
			method:      http.MethodGet,
			target:      constants.LocalAPIBasePath + "/missing",
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:          "authenticated unmatched route",
			method:        http.MethodGet,
			target:        constants.LocalAPIBasePath + "/missing",
			authorization: []string{validAuthorization},
			wantStatus:    http.StatusNotFound,
			wantBody:      notFoundBody,
			wantNoStore:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			router := newTestRouter(io.Discard)
			request := httptest.NewRequest(test.method, test.target, nil)
			for _, value := range test.authorization {
				request.Header.Add("Authorization", value)
			}
			if test.cookie != "" {
				request.Header.Set("Cookie", test.cookie)
			}

			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if response.Body.String() != test.wantBody {
				t.Fatalf("body = %s, want %s", response.Body.String(), test.wantBody)
			}
			gotNoStore := response.Header().Get("Cache-Control") == constants.CacheControlNoStore
			if gotNoStore != test.wantNoStore {
				got := response.Header().Get("Cache-Control")
				t.Fatalf("Cache-Control = %q, want no-store = %t", got, test.wantNoStore)
			}
		})
	}
}

func TestRouterDoesNotLogAuthorization(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	router := newTestRouter(&output)
	request := httptest.NewRequest(http.MethodGet, constants.LocalAPIRuntimeInfoPath, nil)
	request.Header.Set("Authorization", "Bearer private-token-value")

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if strings.Contains(output.String(), "private-token-value") {
		t.Fatal("request log contains authorization credential")
	}
	if !strings.Contains(output.String(), `"status":401`) {
		t.Fatalf("request log does not contain 401 status: %s", output.String())
	}
}

func TestRecoveryUsesAPIErrorContract(t *testing.T) {
	t.Parallel()

	router := newTestRouter(io.Discard)
	router.GET("/panic", func(*gin.Context) {
		panic("private panic value")
	})
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set("Authorization", validAuthorization)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if response.Body.String() != internalErrorBody {
		t.Fatalf("body = %s, want %s", response.Body.String(), internalErrorBody)
	}
	if got := response.Header().Get("Cache-Control"); got != constants.CacheControlNoStore {
		t.Fatalf("Cache-Control = %q, want %q", got, constants.CacheControlNoStore)
	}
}

func newTestRouter(output io.Writer) *gin.Engine {
	logger := slog.New(slog.NewJSONHandler(output, nil))
	info := buildinfo.Info{Version: "1.2.3"}

	return newRouter(info, exactAuthorizationVerifier{}, logger)
}
