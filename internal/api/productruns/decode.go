package productruns

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	reader "github.com/A2MaTex/a2matex-runtime/internal/platform/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productrun"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
)

type StartRequest struct {
	RequestID        string                   `json:"requestId"`
	RuntimeSessionID string                   `json:"runtimeSessionId"`
	Authorization    productrun.Authorization `json:"authorization"`
	Devices          []DeviceInput            `json:"devices"`
}
type DeviceInput struct {
	ADBSerial string          `json:"adbSerial"`
	Inputs    json.RawMessage `json:"inputs"`
}
type ActionRequest struct {
	Action string `json:"action"`
}

func readBody(ctx *gin.Context, maxBytes int) ([]byte, error) {
	r := ctx.Request
	values := r.Header.Values("Content-Type")
	if len(values) != 1 || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, xerror.ErrProductSessionInvalid
	}
	media, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || media != "application/json" {
		return nil, xerror.ErrProductSessionInvalid
	}
	for k, v := range parameters {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return nil, xerror.ErrProductSessionInvalid
		}
	}
	for _, v := range r.Header.Values("Content-Encoding") {
		if strings.TrimSpace(v) != "" {
			return nil, xerror.ErrProductSessionInvalid
		}
	}
	if r.ContentLength > int64(maxBytes) {
		return nil, &http.MaxBytesError{Limit: int64(maxBytes)}
	}
	if r.Body == nil {
		return nil, xerror.ErrProductSessionInvalid
	}
	r.Body = http.MaxBytesReader(ctx.Writer, r.Body, int64(maxBytes))
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) || !json.Valid(raw) || !uniqueKeys(raw) {
		return nil, xerror.ErrProductSessionInvalid
	}
	return raw, nil
}
func object(raw []byte, names ...string) bool {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return false
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := values[name]; !ok {
			return false
		}
	}
	return true
}
func uniqueKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func() bool
	value = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					name, ok := key.(string)
					if err != nil || !ok || seen[name] {
						return false
					}
					seen[name] = true
					if !value() {
						return false
					}
				}
			case '[':
				for decoder.More() {
					if !value() {
						return false
					}
				}
			default:
				return false
			}
			_, err = decoder.Token()
			return err == nil
		}
		return true
	}
	if !value() {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}
func decodeStart(ctx *gin.Context) (productrun.Request, error) {
	directory, err := reader.NormalizeDirectory(ctx.Request.Header.Values(constants.ProductPackageDirectoryHeader))
	if err != nil {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	raw, err := readBody(ctx, constants.RunRequestMaxContentBytes)
	if err != nil {
		return productrun.Request{}, err
	}
	if !object(raw, "requestId", "runtimeSessionId", "authorization", "devices") {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	var r StartRequest
	if json.Unmarshal(raw, &r) != nil || !execution.ValidID(r.RequestID) || !execution.ValidID(r.RuntimeSessionID) || len(r.Devices) == 0 {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(raw, &root)
	if !object(root["authorization"], "lease", "keyEnvelope", "serverTime") {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	if !object(r.Authorization.KeyEnvelope, "releaseId", "algorithm", "runtimeSessionId", "runtimeKeyThumbprint", "encryptedKey") {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	var envelope map[string]string
	if json.Unmarshal(r.Authorization.KeyEnvelope, &envelope) != nil {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	if r.Authorization.Lease == "" {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	if _, err := time.Parse(constants.ExecutionTimestampLayout, r.Authorization.ServerTime); err != nil {
		return productrun.Request{}, xerror.ErrProductSessionInvalid
	}
	var devices []json.RawMessage
	_ = json.Unmarshal(root["devices"], &devices)
	request := productrun.Request{RequestID: r.RequestID, RuntimeSessionID: r.RuntimeSessionID, Directory: directory, Authorization: r.Authorization, Devices: make([]runadmission.DeviceInput, len(r.Devices))}
	seen := map[string]bool{}
	for i, d := range r.Devices {
		if !object(devices[i], "adbSerial", "inputs") || !adb.ValidSerial(d.ADBSerial) || seen[d.ADBSerial] || !bytes.HasPrefix(bytes.TrimSpace(d.Inputs), []byte("{")) {
			return productrun.Request{}, xerror.ErrProductSessionInvalid
		}
		seen[d.ADBSerial] = true
		request.Devices[i] = runadmission.DeviceInput{ADBSerial: d.ADBSerial, Inputs: d.Inputs}
	}
	return request, nil
}
func readRequest(ctx *gin.Context) error {
	r := ctx.Request
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength > 0 {
		return xerror.ErrProductSessionInvalid
	}
	if r.Body != nil {
		var b [1]byte
		if _, err := r.Body.Read(b[:]); err != io.EOF {
			return xerror.ErrProductSessionInvalid
		}
	}
	return optionalDirectory(ctx)
}
func optionalDirectory(ctx *gin.Context) error {
	if values := ctx.Request.Header.Values(constants.ProductPackageDirectoryHeader); len(values) > 0 {
		if _, err := reader.NormalizeDirectory(values); err != nil {
			return xerror.ErrProductSessionInvalid
		}
	}
	return nil
}
