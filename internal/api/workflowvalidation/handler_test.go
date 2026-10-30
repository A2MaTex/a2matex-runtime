package workflowvalidation

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const validGraph = `{"schemaVersion":1,"entryNodeId":"start","nodes":[{"id":"start","type":"flow.start","config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":0,"y":0}},{"id":"end","type":"flow.end","config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":1,"y":0}}],"edges":[{"id":"edge","sourceNodeId":"start","sourcePort":"success","targetNodeId":"end"}]}`
const validEnvelope = `{"editorGraph":` + validGraph + `}`
const payloadTooLargeBody = `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"request payload is too large","retryable":false}}`
const workflowValidationPath = "/api/v1/workflow-validation"

func TestHandlerTransportContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		target      string
		contentType string
		encoding    string
		body        string
		wantStatus  int
		wantBody    string
	}{
		{name: "valid", target: workflowValidationPath, contentType: "application/json", body: validEnvelope, wantStatus: http.StatusOK, wantBody: `{"status":"VALID","errors":[],"warnings":[],"truncated":false}`},
		{name: "UTF-8 charset", target: workflowValidationPath, contentType: "application/json; charset=utf-8", body: validEnvelope, wantStatus: http.StatusOK},
		{name: "query", target: workflowValidationPath + "?x=", contentType: "application/json", body: validEnvelope, wantStatus: http.StatusBadRequest},
		{name: "missing content type", target: workflowValidationPath, body: validEnvelope, wantStatus: http.StatusBadRequest},
		{name: "wrong content type", target: workflowValidationPath, contentType: "text/plain", body: validEnvelope, wantStatus: http.StatusBadRequest},
		{name: "wrong charset", target: workflowValidationPath, contentType: "application/json; charset=utf-16", body: validEnvelope, wantStatus: http.StatusBadRequest},
		{name: "extra media parameter", target: workflowValidationPath, contentType: "application/json; profile=x", body: validEnvelope, wantStatus: http.StatusBadRequest},
		{name: "content encoding", target: workflowValidationPath, contentType: "application/json", encoding: "gzip", body: validEnvelope, wantStatus: http.StatusBadRequest},
		{name: "empty body", target: workflowValidationPath, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "null envelope", target: workflowValidationPath, contentType: "application/json", body: "null", wantStatus: http.StatusBadRequest},
		{name: "array envelope", target: workflowValidationPath, contentType: "application/json", body: "[]", wantStatus: http.StatusBadRequest},
		{name: "scalar envelope", target: workflowValidationPath, contentType: "application/json", body: `"value"`, wantStatus: http.StatusBadRequest},
		{name: "missing graph", target: workflowValidationPath, contentType: "application/json", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "null graph", target: workflowValidationPath, contentType: "application/json", body: `{"editorGraph":null}`, wantStatus: http.StatusBadRequest},
		{name: "malformed JSON", target: workflowValidationPath, contentType: "application/json", body: `{"editorGraph":`, wantStatus: http.StatusBadRequest},
		{name: "unknown envelope field", target: workflowValidationPath, contentType: "application/json", body: `{"editorGraph":{},"unknown":true}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate envelope field", target: workflowValidationPath, contentType: "application/json", body: `{"editorGraph":{},"editorGraph":{}}`, wantStatus: http.StatusBadRequest},
		{name: "non-object graph", target: workflowValidationPath, contentType: "application/json", body: `{"editorGraph":[]}`, wantStatus: http.StatusBadRequest},
		{name: "trailing value", target: workflowValidationPath, contentType: "application/json", body: validEnvelope + `{}`, wantStatus: http.StatusBadRequest},
		{name: "graph contract failure is completed validation", target: workflowValidationPath, contentType: "application/json", body: `{"editorGraph":{"unknown":true}}`, wantStatus: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.encoding != "" {
				request.Header.Set("Content-Encoding", test.encoding)
			}
			response := serve(t, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantBody != "" && response.Body.String() != test.wantBody {
				t.Fatalf("body = %s, want %s", response.Body.String(), test.wantBody)
			}
		})
	}
}

func TestHandlerInvalidUTF8AndMisleadingContentLength(t *testing.T) {
	t.Parallel()

	invalidUTF8 := httptest.NewRequest(http.MethodPost, workflowValidationPath, bytes.NewReader([]byte{'{', 0xff, '}'}))
	invalidUTF8.Header.Set("Content-Type", "application/json")
	if response := serve(t, invalidUTF8); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 status = %d, want 400", response.Code)
	}

	overflow := httptest.NewRequest(http.MethodPost, workflowValidationPath, strings.NewReader(strings.Repeat(" ", constants.LocalAPIWorkflowValidationBodyMaxBytes+1)))
	overflow.Header.Set("Content-Type", "application/json")
	overflow.ContentLength = 1
	if response := serve(t, overflow); response.Code != http.StatusRequestEntityTooLarge || response.Body.String() != payloadTooLargeBody {
		t.Fatalf("misleading Content-Length status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlerCombinesSafeValidationFindingsAndIsDeterministic(t *testing.T) {
	t.Parallel()

	graph := strings.Replace(validGraph, `],"edges"`, `,{"id":"unreachable","type":"unknown.node","config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":2,"y":0}}],"edges"`, 1)
	body := `{"editorGraph":` + graph + `}`
	var firstBody string
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, workflowValidationPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := serve(t, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":"NODE_UNREACHABLE"`) || !strings.Contains(response.Body.String(), `"code":"NODE_TYPE_NOT_ACTIVE"`) {
			t.Fatalf("combined validation status=%d body=%s", response.Code, response.Body.String())
		}
		if attempt == 0 {
			firstBody = response.Body.String()
		} else if response.Body.String() != firstBody {
			t.Fatalf("identical requests differed\nfirst:  %s\nsecond: %s", firstBody, response.Body.String())
		}
	}

	unsupported := strings.Replace(validEnvelope, `"schemaVersion":1`, `"schemaVersion":2`, 1)
	request := httptest.NewRequest(http.MethodPost, workflowValidationPath, strings.NewReader(unsupported))
	request.Header.Set("Content-Type", "application/json")
	response := serve(t, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":"GRAPH_SCHEMA_VERSION_UNSUPPORTED"`) {
		t.Fatalf("unsupported-version response status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlerPayloadLimits(t *testing.T) {
	t.Parallel()

	exactGraph := "{" + strings.Repeat(" ", validation.EditorGraphMaxContentBytes-len(validGraph)) + validGraph[1:]
	exactBody := `{"editorGraph":` + exactGraph + `}`
	exactBody += strings.Repeat(" ", constants.LocalAPIWorkflowValidationBodyMaxBytes-len(exactBody))
	exactRequest := httptest.NewRequest(http.MethodPost, workflowValidationPath, strings.NewReader(exactBody))
	exactRequest.Header.Set("Content-Type", "application/json")
	if response := serve(t, exactRequest); response.Code != http.StatusOK {
		t.Fatalf("exact limits status = %d, want 200; body=%s", response.Code, response.Body.String())
	}

	rawGraph := "{" + strings.Repeat(" ", validation.EditorGraphMaxContentBytes-1) + "}"
	rawLimitRequest := httptest.NewRequest(http.MethodPost, workflowValidationPath, strings.NewReader(`{"editorGraph":`+rawGraph+`}`))
	rawLimitRequest.Header.Set("Content-Type", "application/json")
	if response := serve(t, rawLimitRequest); response.Code != http.StatusRequestEntityTooLarge || response.Body.String() != payloadTooLargeBody {
		t.Fatalf("raw graph overflow status=%d body=%s", response.Code, response.Body.String())
	}

	bodyOverflow := strings.NewReader(strings.Repeat(" ", constants.LocalAPIWorkflowValidationBodyMaxBytes+1))
	chunkedRequest := httptest.NewRequest(http.MethodPost, workflowValidationPath, bodyOverflow)
	chunkedRequest.Header.Set("Content-Type", "application/json")
	chunkedRequest.ContentLength = -1
	if response := serve(t, chunkedRequest); response.Code != http.StatusRequestEntityTooLarge || response.Body.String() != payloadTooLargeBody {
		t.Fatalf("body overflow status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDecodeEnvelopePreservesNestedDuplicateFields(t *testing.T) {
	t.Parallel()

	raw, ok := decodeEnvelope([]byte(`{"editorGraph":{"schemaVersion":2,"schemaVersion":1}}`))
	if !ok || !strings.Contains(string(raw), `"schemaVersion":2,"schemaVersion":1`) {
		t.Fatalf("raw graph = %s, ok=%t", raw, ok)
	}
}

func serve(t *testing.T, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	registry, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(validationservice.New(registry))
	router := gin.New()
	router.POST(workflowValidationPath, handler.Validate)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
