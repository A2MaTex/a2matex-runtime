package executionlease

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// Claims is an authenticated value copy, never a public API projection.
type Claims struct {
	LeaseID              string `json:"-"`
	LeaseFamilyID        string `json:"-"`
	ExecutionID          string `json:"-"`
	Sequence             int64  `json:"-"`
	EntitlementID        string `json:"-"`
	UserID               string `json:"-"`
	ProductID            string `json:"-"`
	ReleaseID            string `json:"-"`
	PackageHash          string `json:"-"`
	RuntimeSessionID     string `json:"-"`
	RuntimeKeyThumbprint string `json:"-"`
	IssuedAt             int64  `json:"-"`
	NotBefore            int64  `json:"-"`
	RefreshAfter         int64  `json:"-"`
	ExpiresAt            int64  `json:"-"`
}

func decodeClaims(ctx context.Context, raw []byte) (Claims, error) {
	fields, err := flatObject(ctx, raw, constants.ExecutionLeaseClaimFields)
	if err != nil {
		return Claims{}, err
	}
	text := func(key string) string { s, _ := fields[key].(string); return s }
	c := Claims{
		LeaseID: text("leaseId"), LeaseFamilyID: text("leaseFamilyId"), ExecutionID: text("executionId"),
		EntitlementID: text("entitlementId"), UserID: text("userId"), ProductID: text("productId"),
		ReleaseID: text("releaseId"), PackageHash: text("packageHash"),
		RuntimeSessionID: text("runtimeSessionId"), RuntimeKeyThumbprint: text("runtimeKeyThumbprint"),
	}
	for _, id := range []string{c.LeaseID, c.LeaseFamilyID, c.ExecutionID, c.EntitlementID, c.UserID, c.ProductID, c.ReleaseID, c.RuntimeSessionID} {
		if !validID(id) {
			return Claims{}, invalid("validate_execution_claims", "identity_invalid")
		}
	}
	if !validHash(c.PackageHash) || !validHash(c.RuntimeKeyThumbprint) {
		return Claims{}, invalid("validate_execution_claims", "hash_invalid")
	}
	for _, field := range []struct {
		key      string
		target   *int64
		min, max int64
	}{
		{"sequence", &c.Sequence, constants.ExecutionLeaseInitialSequence, constants.ExecutionLeaseMaxSequence},
		{"issuedAt", &c.IssuedAt, 0, constants.ExecutionLeaseMaxTimestamp},
		{"notBefore", &c.NotBefore, 0, constants.ExecutionLeaseMaxTimestamp},
		{"refreshAfter", &c.RefreshAfter, 0, constants.ExecutionLeaseMaxTimestamp},
		{"expiresAt", &c.ExpiresAt, 0, constants.ExecutionLeaseMaxTimestamp},
	} {
		number, ok := fields[field.key].(json.Number)
		if !ok || !decimalInteger(string(number)) {
			return Claims{}, invalid("validate_execution_claims", "integer_invalid")
		}
		value, err := strconv.ParseInt(string(number), 10, 64)
		if err != nil || value < field.min || value > field.max {
			return Claims{}, invalid("validate_execution_claims", "integer_invalid")
		}
		*field.target = value
	}
	return c, nil
}

// Both protocol objects are flat and closed. Checking decoded keys before storing
// them also catches duplicate escaped spellings, without struct-name folding.
func flatObject(ctx context.Context, raw []byte, count int) (map[string]json.Token, error) {
	bad := func() (map[string]json.Token, error) { return nil, invalid("parse_execution_lease", "object_invalid") }
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return bad()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return bad()
	}
	fields := make(map[string]json.Token, count)
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || len(fields) >= count {
			return bad()
		}
		if _, exists := fields[key]; exists {
			return bad()
		}
		value, err := decoder.Token()
		if err != nil {
			return bad()
		}
		switch value.(type) {
		case string, json.Number:
			fields[key] = value
		default:
			return bad()
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != count {
		return bad()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return bad()
	}
	return fields, nil
}

func decimalInteger(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == s
}

func validHash(s string) bool {
	if len(s) != constants.ExecutionLeaseHashBytes {
		return false
	}
	for i := range len(s) {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}
