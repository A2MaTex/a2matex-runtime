package runs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
)

func readStart(ctx *gin.Context) (StartRequest, bool) {
	r := ctx.Request
	if !validJSONRequest(r) {
		startError(ctx, nil)
		return StartRequest{}, false
	}
	if r.ContentLength > constants.RunRequestMaxContentBytes {
		startPayloadTooLarge(ctx)
		return StartRequest{}, false
	}
	r.Body = http.MaxBytesReader(ctx.Writer, r.Body, constants.RunRequestMaxContentBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			startPayloadTooLarge(ctx)
		} else {
			startError(ctx, xerror.WithOperation("read_start_request", xerror.ErrRunAdmissionInvalid))
		}
		return StartRequest{}, false
	}
	request, ok := decodeStart(body)
	if !ok {
		startError(ctx, nil)
		return StartRequest{}, false
	}
	if len(request.RuntimeIR) > constants.RuntimeIRMaxContentBytes {
		startPayloadTooLarge(ctx)
		return StartRequest{}, false
	}
	return request, true
}

func validJSONRequest(r *http.Request) bool {
	media, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	valid := err == nil && media == "application/json" && r.URL.RawQuery == "" && r.Body != nil
	for name, value := range parameters {
		valid = valid && name == "charset" && strings.EqualFold(value, "utf-8")
	}
	for _, encoding := range r.Header.Values("Content-Encoding") {
		valid = valid && strings.TrimSpace(encoding) == ""
	}
	return valid
}

func decodeStart(body []byte) (StartRequest, bool) {
	if !utf8.Valid(body) || !json.Valid(body) || !uniqueKeys(body) {
		return StartRequest{}, false
	}
	root, ok := objectFields(body, "requestId", "runtimeIr", "devices")
	var request StartRequest
	if !ok || !stringField(root["requestId"], &request.RequestID) || !execution.ValidID(request.RequestID) ||
		!objectValue(root["runtimeIr"]) || !bytes.HasPrefix(bytes.TrimSpace(root["devices"]), []byte("[")) {
		return StartRequest{}, false
	}
	request.RuntimeIR = root["runtimeIr"]
	var devices []json.RawMessage
	if json.Unmarshal(root["devices"], &devices) != nil {
		return StartRequest{}, false
	}
	request.Devices = make([]StartDeviceInput, len(devices))
	for i, raw := range devices {
		fields, ok := objectFields(raw, "adbSerial", "inputs")
		if !ok || !stringField(fields["adbSerial"], &request.Devices[i].ADBSerial) || !objectValue(fields["inputs"]) {
			return StartRequest{}, false
		}
		request.Devices[i].Inputs = fields["inputs"]
	}
	return request, true
}

func objectValue(raw []byte) bool { return bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) }

func stringField(raw []byte, value *string) bool {
	return bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) && json.Unmarshal(raw, value) == nil
}

func objectFields(raw []byte, names ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if !objectValue(raw) || json.Unmarshal(raw, &fields) != nil || len(fields) != len(names) {
		return nil, false
	}
	for _, name := range names {
		if _, exists := fields[name]; !exists {
			return nil, false
		}
	}
	return fields, true
}

// JSON syntax/depth is checked first. This stack rejects duplicates, including
// escaped-equivalent keys, without recursion or conversion of numeric inputs.
func uniqueKeys(body []byte) bool {
	type frame struct {
		keys      map[string]bool
		expectKey bool
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var stack []frame
	for {
		token, err := d.Token()
		if err == io.EOF {
			return len(stack) == 0
		}
		if err != nil {
			return false
		}
		if token == json.Delim('}') || token == json.Delim(']') {
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 {
			parent := &stack[len(stack)-1]
			if parent.keys != nil {
				if parent.expectKey {
					key, ok := token.(string)
					if !ok || parent.keys[key] {
						return false
					}
					parent.keys[key], parent.expectKey = true, false
					continue
				}
				parent.expectKey = true
			}
		}
		switch token {
		case json.Delim('{'):
			stack = append(stack, frame{keys: make(map[string]bool), expectKey: true})
		case json.Delim('['):
			stack = append(stack, frame{})
		}
	}
}
