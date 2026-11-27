package productpackages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Inspector interface {
	Inspect(context.Context, packagevalidation.InspectionRequest) (*packagevalidation.Inspection, error)
}

type Handler struct{ inspector Inspector }

func NewHandler(inspector Inspector) *Handler { return &Handler{inspector: inspector} }

func (h *Handler) InputSchema(ctx *gin.Context) {
	request, ok := decodeRequest(ctx)
	if !ok {
		return
	}
	result, err := h.inspector.Inspect(ctx.Request.Context(), packagevalidation.InspectionRequest{
		DirectoryHeaderValues: ctx.Request.Header.Values(constants.ProductPackageDirectoryHeader),
		PackageRef:            request.PackageRef, LauncherVersion: request.LauncherVersion,
	})
	if err != nil {
		inspectionFailure(ctx, err)
		return
	}
	if result == nil {
		inspectionFailure(ctx, xerror.ErrProductPackageVerifierConfiguration)
		return
	}
	ctx.JSON(http.StatusOK, InputSchemaResponse{Package: result.Package, InputSchema: result.InputSchema})
}

func decodeRequest(ctx *gin.Context) (InputSchemaRequest, bool) {
	r := ctx.Request
	contentTypes := r.Header.Values("Content-Type")
	if r.URL.RawQuery != "" || r.URL.ForceQuery || len(contentTypes) != 1 || !validContentType(contentTypes[0]) {
		requestFailure(ctx, false, "headers_or_query_invalid")
		return InputSchemaRequest{}, false
	}
	for _, encoding := range r.Header.Values("Content-Encoding") {
		if strings.TrimSpace(encoding) != "" {
			requestFailure(ctx, false, "content_encoding_invalid")
			return InputSchemaRequest{}, false
		}
	}
	if r.ContentLength > constants.ProductPackageSchemaRequestMaxBytes {
		requestFailure(ctx, true, "body_too_large")
		return InputSchemaRequest{}, false
	}
	if r.Body == nil {
		requestFailure(ctx, false, "body_invalid")
		return InputSchemaRequest{}, false
	}
	r.Body = http.MaxBytesReader(ctx.Writer, r.Body, constants.ProductPackageSchemaRequestMaxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var limit *http.MaxBytesError
		requestFailure(ctx, errors.As(err, &limit), "body_read_failed")
		return InputSchemaRequest{}, false
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		requestFailure(ctx, false, "body_invalid")
		return InputSchemaRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		requestFailure(ctx, false, "body_invalid")
		return InputSchemaRequest{}, false
	}
	values := make(map[string]string, 2)
	for decoder.More() {
		key, err := decoder.Token()
		name, isString := key.(string)
		_, duplicate := values[name]
		if err != nil || !isString || duplicate || name != "packageRef" && name != "launcherVersion" {
			requestFailure(ctx, false, "fields_invalid")
			return InputSchemaRequest{}, false
		}
		token, err := decoder.Token()
		value, isString := token.(string)
		if err != nil || !isString || value == "" {
			requestFailure(ctx, false, "fields_invalid")
			return InputSchemaRequest{}, false
		}
		values[name] = value
	}
	if values["packageRef"] == "" || values["launcherVersion"] == "" {
		requestFailure(ctx, false, "fields_invalid")
		return InputSchemaRequest{}, false
	}
	return InputSchemaRequest{PackageRef: values["packageRef"], LauncherVersion: values["launcherVersion"]}, true
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

func requestFailure(ctx *gin.Context, tooLarge bool, reason string) {
	status, code, message := http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest
	if tooLarge {
		status, code, message = http.StatusRequestEntityTooLarge, constants.APIErrorCodePayloadTooLarge, constants.APIErrorMessagePayloadTooLarge
	}
	apiresponse.AbortWithCause(ctx, status, apiresponse.Error{Code: code, Message: message}, &xerror.OperationError{
		Operation: "decode_package_schema_request", Detail: reason, Err: xerror.ErrProductPackageSelection,
	})
}

func inspectionFailure(ctx *gin.Context, cause error) {
	status := http.StatusInternalServerError
	failure := apiresponse.Error{Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal}
	switch {
	case errors.Is(cause, xerror.ErrProductPackageLocation), errors.Is(cause, xerror.ErrProductPackageSelection):
		status, failure.Code, failure.Message = http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest
	case errors.Is(cause, xerror.ErrProductPackageNotFound):
		status, failure.Code, failure.Message = http.StatusNotFound, constants.APIErrorCodeResourceNotFound, constants.APIErrorMessageResourceNotFound
	case errors.Is(cause, xerror.ErrProductPackageInvalid):
		status, failure.Code, failure.Message = http.StatusUnprocessableEntity, constants.ValidationFailed, constants.APIErrorMessageProductPackageInvalid
	case errors.Is(cause, xerror.ErrProductPackageIncompatible):
		status, failure.Code, failure.Message = http.StatusUnprocessableEntity, constants.RuntimeIncompatible, constants.APIErrorMessageProductPackageIncompatible
	case errors.Is(cause, xerror.ErrProductPackageUnavailable), errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		status, failure.Message, failure.Retryable = http.StatusServiceUnavailable, constants.APIErrorMessageProductPackageUnavailable, true
	}
	apiresponse.AbortWithCause(ctx, status, failure, xerror.WithOperation("inspect_product_package", cause))
}
