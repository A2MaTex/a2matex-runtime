package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/api/devices"
	"github.com/A2MaTex/a2matex-runtime/internal/api/workflowvalidation"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const (
	validAuthorization             = "Bearer valid-local-session"
	localAPIBasePath               = "/api/v1"
	localAPIHealthPath             = localAPIBasePath + "/health"
	localAPIRuntimeInfoPath        = localAPIBasePath + "/runtime-info"
	localAPIWorkflowValidationPath = localAPIBasePath + "/workflow-validation"

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
			target:     localAPIHealthPath,
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"UP"}`,
		},
		{
			name:          "health ignores authorization",
			method:        http.MethodGet,
			target:        localAPIHealthPath,
			authorization: []string{"malformed"},
			wantStatus:    http.StatusOK,
			wantBody:      `{"status":"UP"}`,
		},
		{
			name:        "health rejects query",
			method:      http.MethodGet,
			target:      localAPIHealthPath + "?probe=true",
			wantStatus:  http.StatusBadRequest,
			wantBody:    invalidRequestBody,
			wantNoStore: true,
		},
		{
			name:        "runtime information requires authorization",
			method:      http.MethodGet,
			target:      localAPIRuntimeInfoPath,
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:          "runtime information rejects wrong authorization",
			method:        http.MethodGet,
			target:        localAPIRuntimeInfoPath,
			authorization: []string{"Bearer wrong"},
			wantStatus:    http.StatusUnauthorized,
			wantBody:      unauthorizedBody,
			wantNoStore:   true,
		},
		{
			name:          "runtime information rejects duplicate authorization",
			method:        http.MethodGet,
			target:        localAPIRuntimeInfoPath,
			authorization: []string{validAuthorization, validAuthorization},
			wantStatus:    http.StatusUnauthorized,
			wantBody:      unauthorizedBody,
			wantNoStore:   true,
		},
		{
			name:        "query token does not authenticate",
			method:      http.MethodGet,
			target:      localAPIRuntimeInfoPath + "?token=valid-local-session",
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:        "cookie token does not authenticate",
			method:      http.MethodGet,
			target:      localAPIRuntimeInfoPath,
			cookie:      "token=valid-local-session",
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:          "authenticated runtime information",
			method:        http.MethodGet,
			target:        localAPIRuntimeInfoPath,
			authorization: []string{validAuthorization},
			wantStatus:    http.StatusOK,
			wantBody:      `{"runtimeVersion":"1.2.3","runtimeIrFormatVersions":[1],"supportedNodes":[]}`,
			wantNoStore:   true,
		},
		{
			name:        "workflow validation authenticates before parsing",
			method:      http.MethodPost,
			target:      localAPIWorkflowValidationPath,
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:          "workflow validation rejects wrong authorization",
			method:        http.MethodPost,
			target:        localAPIWorkflowValidationPath,
			authorization: []string{"Bearer wrong"},
			wantStatus:    http.StatusUnauthorized,
			wantBody:      unauthorizedBody,
			wantNoStore:   true,
		},
		{
			name:          "workflow validation rejects duplicate authorization",
			method:        http.MethodPost,
			target:        localAPIWorkflowValidationPath,
			authorization: []string{validAuthorization, validAuthorization},
			wantStatus:    http.StatusUnauthorized,
			wantBody:      unauthorizedBody,
			wantNoStore:   true,
		},
		{
			name:        "workflow validation query token does not authenticate",
			method:      http.MethodPost,
			target:      localAPIWorkflowValidationPath + "?token=valid-local-session",
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:        "unmatched route is protected",
			method:      http.MethodGet,
			target:      localAPIBasePath + "/missing",
			wantStatus:  http.StatusUnauthorized,
			wantBody:    unauthorizedBody,
			wantNoStore: true,
		},
		{
			name:          "authenticated unmatched route",
			method:        http.MethodGet,
			target:        localAPIBasePath + "/missing",
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
	request := httptest.NewRequest(http.MethodGet, localAPIRuntimeInfoPath, nil)
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

func TestWorkflowValidationRouterIntegration(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	router := newTestRouter(&output)
	body := `{"editorGraph":{"schemaVersion":1,"entryNodeId":"private_graph_value",` +
		`"nodes":[{"id":"private_graph_value","type":"flow.start","config":{},"bindings":{},` +
		`"errorPolicy":"STOP","editor":{"x":0,"y":0}},{"id":"end","type":"flow.end",` +
		`"config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":1,"y":0}}],` +
		`"edges":[{"id":"edge","sourceNodeId":"private_graph_value","sourcePort":"success",` +
		`"targetNodeId":"end"}]}}`
	request := httptest.NewRequest(http.MethodPost, localAPIWorkflowValidationPath, strings.NewReader(body))
	request.Header.Set("Authorization", validAuthorization)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != `{"status":"VALID","errors":[],"warnings":[],"truncated":false}` {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != constants.CacheControlNoStore {
		t.Fatalf("Cache-Control = %q", got)
	}
	if strings.Contains(output.String(), "private_graph_value") {
		t.Fatal("request log contains workflow content")
	}

	unsupported := httptest.NewRequest(http.MethodGet, localAPIWorkflowValidationPath, nil)
	unsupported.Header.Set("Authorization", validAuthorization)
	unsupportedResponse := httptest.NewRecorder()
	router.ServeHTTP(unsupportedResponse, unsupported)
	if unsupportedResponse.Code != http.StatusNotFound || unsupportedResponse.Body.String() != notFoundBody {
		t.Fatalf("unsupported method status=%d body=%s", unsupportedResponse.Code, unsupportedResponse.Body.String())
	}
}

func TestWorkflowCompilationRoute(t *testing.T) {
	t.Parallel()
	const schema = `{"type":"object","properties":{},"required":[],"additionalProperties":false}`
	const graph = `{"schemaVersion":1,"entryNodeId":"start","nodes":[{"id":"start","type":"flow.start","config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":0,"y":0}},{"id":"end","type":"flow.end","config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":0,"y":0}}],"edges":[{"id":"e1","sourceNodeId":"start","sourcePort":"success","targetNodeId":"end"}]}`
	body := `{"editorGraph":` + graph + `,"inputSchema":` + schema + `}`
	cases := []struct {
		name, body, auth string
		status           int
		hasIR            bool
	}{
		{"valid", body, validAuthorization, http.StatusOK, true},
		{"unauthenticated", body, "", http.StatusUnauthorized, false},
		{"unknown field", strings.TrimSuffix(body, "}") + `,"projectId":"private"}`, validAuthorization, http.StatusBadRequest, false},
		{"duplicate field", strings.TrimSuffix(body, "}") + `,"inputSchema":{}` + "}", validAuthorization, http.StatusBadRequest, false},
		{"missing schema", `{"editorGraph":{}}`, validAuthorization, http.StatusBadRequest, false},
		{"invalid source", `{"editorGraph":{},"inputSchema":` + schema + `}`, validAuthorization, http.StatusOK, false},
		{"oversize graph", `{"editorGraph":{"padding":"` + strings.Repeat("a", constants.EditorGraphMaxContentBytes) + `"},"inputSchema":` + schema + `}`, validAuthorization, http.StatusRequestEntityTooLarge, false},
		{"oversize envelope", strings.Repeat(" ", constants.LocalAPIWorkflowCompilationBodyMaxBytes) + body, validAuthorization, http.StatusRequestEntityTooLarge, false},
	}
	router := newTestRouter(io.Discard)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, localAPIBasePath+"/workflow-compilation", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			if tc.auth != "" {
				request.Header.Set("Authorization", tc.auth)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body)
			}
			if tc.status == http.StatusOK {
				var result validationservice.CompilationResult
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.RuntimeIR) == 0 || (string(result.RuntimeIR) != "null") != tc.hasIR {
					t.Fatalf("unexpected runtimeIr: %s", result.RuntimeIR)
				}
			}
		})
	}
}

func TestWorkflowPortCountParity(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../validation/testdata/port-edge-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name        string
		EditorGraph json.RawMessage
		InputSchema json.RawMessage
		Validation  json.RawMessage
		RuntimeIR   json.RawMessage
	}
	if err := json.Unmarshal(contents, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty port parity fixture")
	}

	router := newTestRouter(io.Discard)
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			for _, operation := range []struct {
				path     string
				body     map[string]json.RawMessage
				expected json.RawMessage
			}{
				{"/workflow-validation", map[string]json.RawMessage{"editorGraph": tc.EditorGraph}, tc.Validation},
				{"/workflow-compilation", map[string]json.RawMessage{"editorGraph": tc.EditorGraph, "inputSchema": tc.InputSchema}, nil},
			} {
				t.Run(operation.path[1:], func(t *testing.T) {
					body, err := json.Marshal(operation.body)
					if err != nil {
						t.Fatal(err)
					}
					request := httptest.NewRequest(http.MethodPost, localAPIBasePath+operation.path, bytes.NewReader(body))
					request.Header.Set("Authorization", validAuthorization)
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					if response.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", response.Code, response.Body)
					}
					var actual, expected any
					if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
						t.Fatal(err)
					}
					expectedJSON := operation.expected
					if expectedJSON == nil {
						expectedJSON, err = json.Marshal(map[string]json.RawMessage{"validation": tc.Validation, "runtimeIr": tc.RuntimeIR})
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := json.Unmarshal(expectedJSON, &expected); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(actual, expected) {
						t.Fatalf("got %s, want %s", response.Body, expectedJSON)
					}
				})
			}
		})
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
	return newTestRouterWithInventory(output, services.NewDeviceInventory(&inventorySource{}))
}

func newTestRouterWithInventory(output io.Writer, inventory devices.Inventory) *gin.Engine {
	logger := slog.New(slog.NewJSONHandler(output, nil))
	info := buildinfo.Info{Version: "1.2.3"}
	registry, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		panic(err)
	}
	workflowValidation := workflowvalidation.NewHandler(validationservice.New(registry))

	return newRouter(info, exactAuthorizationVerifier{}, workflowValidation, devices.NewHandler(inventory), logger)
}
