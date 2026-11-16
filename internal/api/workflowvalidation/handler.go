package workflowvalidation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Handler struct {
	service *validationservice.Service
}

func NewHandler(service *validationservice.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Validate(ctx *gin.Context) {
	body, ok := readBody(ctx, constants.LocalAPIWorkflowValidationBodyMaxBytes)
	if !ok {
		return
	}

	rawGraph, ok := decodeEnvelope(body)
	if !ok {
		invalidRequest(ctx)
		return
	}
	if len(rawGraph) > validation.EditorGraphMaxContentBytes {
		payloadTooLarge(ctx)
		return
	}

	result, err := h.service.Validate(rawGraph)
	if errors.Is(err, validationservice.ErrPayloadTooLarge) {
		payloadTooLarge(ctx)
		return
	}
	if err != nil {
		internalFailure(ctx, "validate_workflow", err)
		return
	}
	ctx.JSON(http.StatusOK, result)
}

func (h *Handler) Compile(ctx *gin.Context) {
	body, ok := readBody(ctx, constants.LocalAPIWorkflowCompilationBodyMaxBytes)
	if !ok {
		return
	}
	fields, ok := decodeObjects(body, "editorGraph", "inputSchema")
	if !ok {
		invalidRequest(ctx)
		return
	}
	result, err := h.service.Compile(fields["editorGraph"], fields["inputSchema"])
	if errors.Is(err, validationservice.ErrPayloadTooLarge) {
		payloadTooLarge(ctx)
		return
	}
	if err != nil {
		internalFailure(ctx, "compile_workflow", err)
		return
	}
	ctx.JSON(http.StatusOK, result)
}

func readBody(ctx *gin.Context, limit int64) ([]byte, bool) {
	if ctx.Request.URL.RawQuery != "" || !validContentType(ctx.Request.Header.Get("Content-Type")) || hasContentEncoding(ctx.Request.Header.Values("Content-Encoding")) {
		invalidRequest(ctx)
		return nil, false
	}
	if ctx.Request.ContentLength > limit {
		payloadTooLarge(ctx)
		return nil, false
	}

	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
	body, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			payloadTooLarge(ctx)
			return nil, false
		}
		invalidRequest(ctx)
		return nil, false
	}
	if !utf8.Valid(body) {
		invalidRequest(ctx)
		return nil, false
	}
	return body, true
}

func validContentType(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return false
	}
	for name, value := range parameters {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func hasContentEncoding(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func decodeEnvelope(body []byte) (json.RawMessage, bool) {
	objects, ok := decodeObjects(body, "editorGraph")
	return objects["editorGraph"], ok
}

func decodeObjects(body []byte, required ...string) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(required))
	for _, field := range required {
		fields[field] = nil
	}
	for decoder.More() {
		token, err := decoder.Token()
		field, fieldOK := token.(string)
		existing, expected := fields[field]
		if err != nil || !fieldOK || !expected || existing != nil {
			return nil, false
		}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, false
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) < 2 || trimmed[0] != '{' {
			return nil, false
		}
		fields[field] = raw
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, false
	}
	for _, value := range fields {
		if value == nil {
			return nil, false
		}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return fields, true
}

func invalidRequest(ctx *gin.Context) {
	apiresponse.Abort(ctx, http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest)
}

func payloadTooLarge(ctx *gin.Context) {
	apiresponse.Abort(ctx, http.StatusRequestEntityTooLarge, constants.APIErrorCodePayloadTooLarge, constants.APIErrorMessagePayloadTooLarge)
}

func internalFailure(ctx *gin.Context, operation string, cause error) {
	apiresponse.AbortWithCause(ctx, http.StatusInternalServerError, apiresponse.Error{
		Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal,
	}, xerror.WithOperation(operation, cause))
}
