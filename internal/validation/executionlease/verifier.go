// Package executionlease verifies Backend authorization without admitting execution.
package executionlease

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Options struct {
	VerificationKeys map[string]string `json:"-"`
	ClockSkew        time.Duration     `json:"-"`
}

type Expected struct {
	ExecutionID          string `json:"-"`
	ProductID            string `json:"-"`
	ReleaseID            string `json:"-"`
	PackageHash          string `json:"-"`
	RuntimeSessionID     string `json:"-"`
	RuntimeKeyThumbprint string `json:"-"`
}

type Request struct {
	Compact  string                          `json:"-"`
	Mode     constants.LeaseVerificationMode `json:"-"`
	Expected Expected                        `json:"-"`
	Current  *Verified                       `json:"-"`
}

type Result struct {
	Grant    *Verified `json:"-"`
	Replayed bool      `json:"-"`
}

// Verified can only be produced by successful verification in this process.
type Verified struct {
	verifier             *Verifier
	claims               Claims
	kid                  string
	refreshAt, expiresAt time.Time
}

func (g *Verified) Claims() Claims       { return g.claims }
func (g *Verified) SigningKeyID() string { return g.kid }
func (g *Verified) RefreshAt() time.Time { return g.refreshAt }
func (g *Verified) ExpiresAt() time.Time { return g.expiresAt }

type Verifier struct {
	keys   map[string]ed25519.PublicKey
	skew   time.Duration
	anchor time.Time
	since  func(time.Time) time.Duration
}

func New(options Options) (*Verifier, error) {
	v := &Verifier{anchor: time.Now(), since: time.Since, skew: options.ClockSkew}
	if len(options.VerificationKeys) == 0 || v.skew < 0 || v.skew > constants.ExecutionLeaseMaxClockSkew {
		return nil, configuration("create_execution_lease_verifier", "trust_invalid")
	}
	v.keys = make(map[string]ed25519.PublicKey, len(options.VerificationKeys))
	for kid, encoded := range options.VerificationKeys {
		if !validKeyID(kid) {
			return nil, configuration("create_execution_lease_verifier", "trust_invalid")
		}
		raw := bytes.TrimSpace([]byte(encoded))
		block, rest := pem.Decode(raw)
		if !bytes.HasPrefix(raw, []byte("-----BEGIN PUBLIC KEY-----")) || block == nil ||
			block.Type != "PUBLIC KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 ||
			bytes.Count(raw, []byte("-----BEGIN ")) != 1 {
			return nil, configuration("create_execution_lease_verifier", "trust_invalid")
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		key, ok := parsed.(ed25519.PublicKey)
		if err != nil || !ok || len(key) != ed25519.PublicKeySize {
			return nil, configuration("create_execution_lease_verifier", "trust_invalid")
		}
		v.keys[kid] = append(ed25519.PublicKey(nil), key...)
	}
	return v, nil
}

func (v *Verifier) Verify(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, xerror.WithOperation("verify_execution_lease", err)
	}
	if err := v.checkRequest(request); err != nil {
		return Result{}, err
	}
	kid, payload, err := v.verifySignature(ctx, request.Compact)
	if err != nil {
		return Result{}, xerror.WithOperation("verify_execution_lease", err)
	}
	claims, err := decodeClaims(ctx, payload)
	if err != nil {
		return Result{}, xerror.WithOperation("validate_execution_claims", err)
	}
	if claims.expected() != request.Expected {
		return Result{}, invalid("match_execution_binding", "binding_mismatch")
	}
	now := v.now()
	if err := v.checkTime(claims, now); err != nil {
		return Result{}, err
	}
	replayed, err := checkSequence(request, claims, kid, now)
	if err != nil {
		return Result{}, err
	}
	grant := request.Current
	if !replayed {
		// Freshness and lifetime checks bound these differences before conversion.
		grant = &Verified{verifier: v, claims: claims, kid: kid,
			refreshAt: now.Add(time.Unix(claims.RefreshAfter, 0).Sub(now.UTC())),
			expiresAt: now.Add(time.Unix(claims.ExpiresAt, 0).Sub(now.UTC())),
		}
	}
	final := v.now()
	if err := v.checkTime(claims, final); err != nil {
		return Result{}, err
	}
	if _, err := checkSequence(request, claims, kid, final); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, xerror.WithOperation("verify_execution_lease", err)
	}
	return Result{Grant: grant, Replayed: replayed}, nil
}

func (v *Verifier) verifySignature(ctx context.Context, compact string) (string, []byte, error) {
	bad := func() (string, []byte, error) {
		return "", nil, invalid("verify_execution_signature", "signature_invalid")
	}
	if len(compact) == 0 || len(compact) > constants.ExecutionLeaseMaxBytes {
		return bad()
	}
	parts := strings.SplitN(compact, ".", constants.ExecutionLeaseSegments+1)
	if len(parts) != constants.ExecutionLeaseSegments {
		return bad()
	}
	header, ok := decodeSegment(parts[0])
	if !ok {
		return bad()
	}
	payload, ok := decodeSegment(parts[1])
	if !ok {
		return bad()
	}
	signature, ok := decodeSegment(parts[2])
	if !ok || len(signature) != ed25519.SignatureSize {
		return bad()
	}
	fields, err := flatObject(ctx, header, constants.ExecutionLeaseHeaderFields)
	if err != nil {
		return "", nil, err
	}
	kid, ok := fields["kid"].(string)
	if !ok || !validKeyID(kid) || fields["alg"] != constants.ExecutionLeaseAlgorithm || fields["typ"] != constants.ExecutionLeaseType {
		return bad()
	}
	key, trusted := v.keys[kid]
	if !trusted || !ed25519.Verify(key, []byte(compact[:strings.LastIndexByte(compact, '.')]), signature) {
		return bad()
	}
	return kid, payload, nil
}

func decodeSegment(s string) ([]byte, bool) {
	if s == "" {
		return nil, false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, false
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return raw, err == nil && base64.RawURLEncoding.EncodeToString(raw) == s
}

func validKeyID(s string) bool {
	if len(s) == 0 || len(s) > constants.ExecutionLeaseKeyIDMaxBytes {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func invalid(operation, reason string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrExecutionLeaseInvalid}
}

func configuration(operation, reason string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrExecutionLeaseConfiguration}
}
