// Package keyenvelope opens session-bound keys after the caller verifies authorization.
package keyenvelope

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Expected is projected from a verified lease matched against the live session.
type Expected struct {
	ReleaseID            string `json:"-"`
	RuntimeSessionID     string `json:"-"`
	RuntimeKeyThumbprint string `json:"-"`
}

type Request struct {
	Envelope   json.RawMessage `json:"-"`
	Expected   Expected        `json:"-"`
	PrivateKey *rsa.PrivateKey `json:"-"`
}

func decode(ctx context.Context, raw []byte, expected Expected) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, invalid("parse_product_key_envelope", "object_invalid") }
	if len(raw) == 0 || len(raw) > constants.ProductKeyEnvelopeMaxBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return bad()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return bad()
	}
	fields := make(map[string]string, constants.ProductKeyEnvelopeFieldCount)
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, xerror.WithOperation("parse_product_key_envelope", err)
		}
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || len(fields) >= constants.ProductKeyEnvelopeFieldCount {
			return bad()
		}
		switch name {
		case "releaseId", "algorithm", "runtimeSessionId", "runtimeKeyThumbprint", "encryptedKey":
		default:
			return bad()
		}
		if _, duplicate := fields[name]; duplicate {
			return bad()
		}
		token, err = decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return bad()
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != constants.ProductKeyEnvelopeFieldCount {
		return bad()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return bad()
	}
	if fields["algorithm"] != constants.ProductKeyEnvelopeAlgorithm || fields["releaseId"] != expected.ReleaseID ||
		fields["runtimeSessionId"] != expected.RuntimeSessionID || fields["runtimeKeyThumbprint"] != expected.RuntimeKeyThumbprint {
		return nil, invalid("match_product_key_binding", "binding_mismatch")
	}
	encoded := fields["encryptedKey"]
	if len(encoded) != constants.ProductKeyCiphertextBase64Bytes {
		return nil, invalid("parse_product_key_envelope", "encoding_invalid")
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(ciphertext) != constants.ProductKeyCiphertextBytes || base64.StdEncoding.EncodeToString(ciphertext) != encoded {
		return nil, invalid("parse_product_key_envelope", "encoding_invalid")
	}
	return ciphertext, nil
}

func validExpected(expected Expected) bool {
	for _, value := range []string{expected.ReleaseID, expected.RuntimeSessionID} {
		id, err := uuid.Parse(value)
		if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != value {
			return false
		}
	}
	if len(expected.RuntimeKeyThumbprint) != sha256.Size*2 {
		return false
	}
	for i := range len(expected.RuntimeKeyThumbprint) {
		c := expected.RuntimeKeyThumbprint[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func invalid(operation, reason string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrProductKeyEnvelopeInvalid}
}

func configuration(operation, reason string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrProductKeyEnvelopeConfiguration}
}
