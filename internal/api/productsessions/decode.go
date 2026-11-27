package productsessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func decodeRequest(ctx *gin.Context) (PrepareRequest, bool) {
	r := ctx.Request
	types := r.Header.Values("Content-Type")
	if r.URL.RawQuery != "" || r.URL.ForceQuery || len(types) != 1 || !validContentType(types[0]) {
		return invalidRequest(ctx, false, "headers_or_query_invalid")
	}
	for _, value := range r.Header.Values("Content-Encoding") {
		if strings.TrimSpace(value) != "" {
			return invalidRequest(ctx, false, "content_encoding_invalid")
		}
	}
	if r.ContentLength > constants.ProductSessionRequestMaxBytes {
		return invalidRequest(ctx, true, "body_too_large")
	}
	if r.Body == nil {
		return invalidRequest(ctx, false, "body_invalid")
	}
	r.Body = http.MaxBytesReader(ctx.Writer, r.Body, constants.ProductSessionRequestMaxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if r.Context().Err() != nil {
			prepareFailure(ctx, r.Context().Err())
			return PrepareRequest{}, false
		}
		var limit *http.MaxBytesError
		return invalidRequest(ctx, errors.As(err, &limit), "body_read_failed")
	}
	root, ok := object(body, "requestId", "purpose", "runGroupId", "package")
	if !ok {
		return invalidRequest(ctx, false, "body_invalid")
	}
	selection, ok := object(root["package"], "packageRef", "packageHash", "launcherVersion")
	if !ok || len(selection) != 3 {
		return invalidRequest(ctx, false, "package_fields_invalid")
	}
	var request PrepareRequest
	for _, field := range []struct {
		raw    json.RawMessage
		target *string
	}{
		{root["requestId"], &request.RequestID}, {selection["packageRef"], &request.Package.PackageRef},
		{selection["packageHash"], &request.Package.PackageHash}, {selection["launcherVersion"], &request.Package.LauncherVersion},
	} {
		if json.Unmarshal(field.raw, field.target) != nil || *field.target == "" {
			return invalidRequest(ctx, false, "fields_invalid")
		}
	}
	if json.Unmarshal(root["purpose"], &request.Purpose) != nil {
		return invalidRequest(ctx, false, "purpose_invalid")
	}
	switch request.Purpose {
	case constants.ProductSessionStart:
		if len(root) != 3 {
			return invalidRequest(ctx, false, "start_fields_invalid")
		}
	case constants.ProductSessionReauthorize:
		var id string
		if len(root) != 4 || json.Unmarshal(root["runGroupId"], &id) != nil || id == "" {
			return invalidRequest(ctx, false, "reauthorize_fields_invalid")
		}
		request.RunGroupID = &id
	default:
		return invalidRequest(ctx, false, "purpose_invalid")
	}
	return request, true
}

// The two closed objects need duplicate-aware decoding, including escaped names.
func object(raw []byte, allowed ...string) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for d.More() {
		token, err := d.Token()
		name, isString := token.(string)
		_, duplicate := fields[name]
		if err != nil || !isString || duplicate || !slices.Contains(allowed, name) {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	_, err = d.Token()
	return fields, err == nil
}

func validContentType(value string) bool {
	media, parameters, err := mime.ParseMediaType(value)
	if err != nil || media != "application/json" {
		return false
	}
	for name, value := range parameters {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func invalidRequest(ctx *gin.Context, large bool, reason string) (PrepareRequest, bool) {
	status, code, message := http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest
	if large {
		status, code, message = http.StatusRequestEntityTooLarge, constants.APIErrorCodePayloadTooLarge, constants.APIErrorMessagePayloadTooLarge
	}
	apiresponse.AbortWithCause(ctx, status, apiresponse.Error{Code: code, Message: message},
		&xerror.OperationError{Operation: "decode_product_session_request", Detail: reason, Err: xerror.ErrProductSessionInvalid})
	return PrepareRequest{}, false
}
