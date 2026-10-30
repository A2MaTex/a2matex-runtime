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
)

type Handler struct {
	service *validationservice.Service
}

func NewHandler(service *validationservice.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Validate(ctx *gin.Context) {
	if ctx.Request.URL.RawQuery != "" || !validContentType(ctx.Request.Header.Get("Content-Type")) || hasContentEncoding(ctx.Request.Header.Values("Content-Encoding")) {
		invalidRequest(ctx)
		return
	}
	if ctx.Request.ContentLength > constants.LocalAPIWorkflowValidationBodyMaxBytes {
		payloadTooLarge(ctx)
		return
	}

	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, constants.LocalAPIWorkflowValidationBodyMaxBytes)
	body, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			payloadTooLarge(ctx)
			return
		}
		invalidRequest(ctx)
		return
	}
	if !utf8.Valid(body) {
		invalidRequest(ctx)
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
		panic(err)
	}
	ctx.JSON(http.StatusOK, result)
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
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	seenGraph := false
	var graph json.RawMessage
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		field, ok := token.(string)
		if !ok || field != "editorGraph" || seenGraph {
			return nil, false
		}
		seenGraph = true
		if err := decoder.Decode(&graph); err != nil {
			return nil, false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || !seenGraph {
		return nil, false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	trimmed := bytes.TrimSpace(graph)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return nil, false
	}
	return graph, true
}

func invalidRequest(ctx *gin.Context) {
	apiresponse.Abort(ctx, http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest)
}

func payloadTooLarge(ctx *gin.Context) {
	apiresponse.Abort(ctx, http.StatusRequestEntityTooLarge, constants.APIErrorCodePayloadTooLarge, constants.APIErrorMessagePayloadTooLarge)
}
