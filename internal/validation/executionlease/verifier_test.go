package executionlease

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type vectors struct {
	FixedClock       time.Time
	VerificationKeys map[string]string
	Claims           map[string]any
	Cases            []struct {
		Name, Lease, Expected string
		ClockOffsetSeconds    int64
		Setup                 struct{ CurrentLease string }
	}
}

func fixture(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/v2/protocol-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile("testdata/v2/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version, CaseCount int
		Files              []struct{ Path, SHA256 string }
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	const acceptedHash = "6ff448a7578d9bcb0aee22e200f5f60dcc68e0ad4265f103170380bbbe8521cb"
	if manifest.Version != 2 || manifest.CaseCount != 22 || len(manifest.Files) != 1 || manifest.Files[0].Path != "protocol-vectors.json" || manifest.Files[0].SHA256 != acceptedHash || hex.EncodeToString(digest[:]) != acceptedHash {
		t.Fatal("accepted Backend fixture changed")
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Cases) != manifest.CaseCount {
		t.Fatal("fixture case count")
	}
	return v
}

func fixedVerifier(t *testing.T, keys map[string]string, skew time.Duration) (*Verifier, *time.Duration) {
	t.Helper()
	v, err := New(Options{VerificationKeys: keys, ClockSkew: skew})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := new(time.Duration)
	v.anchor = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	v.since = func(time.Time) time.Duration { return *elapsed }
	return v, elapsed
}

func expected(m map[string]any) Expected {
	return Expected{ExecutionID: m["executionId"].(string), ProductID: m["productId"].(string), ReleaseID: m["releaseId"].(string), PackageHash: m["packageHash"].(string), RuntimeSessionID: m["runtimeSessionId"].(string), RuntimeKeyThumbprint: m["runtimeKeyThumbprint"].(string)}
}

func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func publicPEM(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func signing(t *testing.T) (*Verifier, *time.Duration, ed25519.PrivateKey, map[string]any) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v, elapsed := fixedVerifier(t, map[string]string{"test-key": publicPEM(t, public)}, 30*time.Second)
	return v, elapsed, key, fixture(t).Claims
}

func signRaw(t *testing.T, key ed25519.PrivateKey, header, payload []byte) string {
	t.Helper()
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func sign(t *testing.T, key ed25519.PrivateKey, m map[string]any) string {
	t.Helper()
	return signRaw(t, key, []byte(`{"alg":"EdDSA","kid":"test-key","typ":"a2matex-execution-lease+jws"}`), jsonBytes(t, m))
}

func initial(t *testing.T, v *Verifier, key ed25519.PrivateKey, m map[string]any) *Verified {
	t.Helper()
	result, err := v.Verify(context.Background(), Request{Compact: sign(t, key, m), Mode: constants.LeaseVerificationInitial, Expected: expected(m)})
	if err != nil || result.Grant == nil || result.Replayed {
		t.Fatalf("initial grant: %+v %v", result, err)
	}
	return result.Grant
}

func assertFailure(t *testing.T, result Result, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || result != (Result{}) {
		t.Fatalf("result=%+v err=%v want=%v", result, err, want)
	}
	var detail *xerror.OperationError
	if !errors.As(err, &detail) || detail.Operation == "" || strings.Contains(err.Error()+detail.Detail, "PRIVATE") {
		t.Fatalf("unsafe/missing operation: %v", err)
	}
}

func claimMap(c Claims) map[string]any {
	return map[string]any{"leaseId": c.LeaseID, "leaseFamilyId": c.LeaseFamilyID, "executionId": c.ExecutionID, "sequence": c.Sequence, "entitlementId": c.EntitlementID, "userId": c.UserID, "productId": c.ProductID, "releaseId": c.ReleaseID, "packageHash": c.PackageHash, "runtimeSessionId": c.RuntimeSessionID, "runtimeKeyThumbprint": c.RuntimeKeyThumbprint, "issuedAt": c.IssuedAt, "notBefore": c.NotBefore, "refreshAfter": c.RefreshAfter, "expiresAt": c.ExpiresAt}
}

func TestBackendVectors(t *testing.T) {
	f := fixture(t)
	initialToken, renewalToken := "", ""
	for _, c := range f.Cases {
		if c.Name == "valid-initial" {
			initialToken = c.Lease
		}
		if c.Name == "valid-renewal" {
			renewalToken = c.Lease
		}
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			v, elapsed := fixedVerifier(t, f.VerificationKeys, 30*time.Second)
			r := Request{Compact: c.Lease, Mode: constants.LeaseVerificationInitial, Expected: expected(f.Claims)}
			if c.Setup.CurrentLease != "" {
				setup, err := v.Verify(context.Background(), Request{Compact: initialToken, Mode: constants.LeaseVerificationInitial, Expected: r.Expected})
				if err != nil {
					t.Fatal(err)
				}
				r.Current = setup.Grant
				r.Mode = constants.LeaseVerificationRenew
				if c.Name != "wrong-user" {
					*elapsed = 1200 * time.Second
				}
				if c.Name != "wrong-user" && c.Name != "valid-renewal" {
					setup, err = v.Verify(context.Background(), Request{Compact: renewalToken, Mode: r.Mode, Expected: r.Expected, Current: r.Current})
					if err != nil {
						t.Fatal(err)
					}
					r.Current = setup.Grant
					if c.Setup.CurrentLease != renewalToken {
						t.Fatal("fixture setup differs")
					}
				} else if c.Setup.CurrentLease != initialToken {
					t.Fatal("fixture setup differs")
				}
			} else {
				*elapsed = time.Duration(c.ClockOffsetSeconds) * time.Second
			}
			result, err := v.Verify(context.Background(), r)
			if c.Expected == "REJECT" {
				assertFailure(t, result, err, xerror.ErrExecutionLeaseInvalid)
				return
			}
			if err != nil || result.Grant == nil {
				t.Fatalf("accepted Backend grant: %v", err)
			}
			var actual, want map[string]any
			if err := json.Unmarshal(jsonBytes(t, claimMap(result.Grant.Claims())), &actual); err != nil {
				t.Fatal(err)
			}
			payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(c.Lease, ".")[1])
			if err := json.Unmarshal(payload, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatal("authenticated claims changed")
			}
			if result.Replayed != (c.Name == "identical-renewal-replay") {
				t.Fatal("replay classification")
			}
			if result.Replayed && (result.Grant != r.Current || result.Grant.ExpiresAt() != r.Current.ExpiresAt()) {
				t.Fatal("replay moved deadline")
			}
		})
	}
}

func TestTrustConfiguration(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := publicPEM(t, public)
	other, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, kid, value string
		skew             time.Duration
	}{
		{"empty kid", "", encoded, 0}, {"bad kid", "PRIVATE/key", encoded, 0}, {"long kid", strings.Repeat("a", 65), encoded, 0},
		{"empty key", "k", "", 0}, {"bad PEM", "k", "PRIVATE", 0}, {"prefix", "k", "PRIVATE\n" + encoded, 0}, {"suffix", "k", encoded + "PRIVATE", 0},
		{"multiple PEM", "k", encoded + encoded, 0}, {"skipped malformed PEM", "k", "-----BEGIN PUBLIC KEY-----\nPRIVATE\n" + encoded, 0},
		{"RSA", "k", publicPEM(t, &other.PublicKey), 0}, {"private", "k", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})), 0},
		{"certificate", "k", strings.ReplaceAll(strings.ReplaceAll(encoded, "BEGIN PUBLIC KEY", "BEGIN CERTIFICATE"), "END PUBLIC KEY", "END CERTIFICATE"), 0},
		{"PEM headers", "k", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Headers: map[string]string{"PRIVATE": "value"}, Bytes: []byte{1}})), 0},
		{"negative skew", "k", encoded, -time.Nanosecond}, {"large skew", "k", encoded, 30*time.Second + time.Nanosecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := New(Options{VerificationKeys: map[string]string{c.kid: c.value}, ClockSkew: c.skew})
			if v != nil || !errors.Is(err, xerror.ErrExecutionLeaseConfiguration) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("trust: %v", err)
			}
		})
	}
	if v, err := New(Options{}); v != nil || !errors.Is(err, xerror.ErrExecutionLeaseConfiguration) {
		t.Fatal("empty trust accepted")
	}
	keys := map[string]string{"test-key": encoded}
	v, _ := fixedVerifier(t, keys, 0)
	keys["test-key"] = "PRIVATE"
	delete(keys, "test-key")
	initial(t, v, key, fixture(t).Claims)
}

func TestCompactAndClosedObjects(t *testing.T) {
	v, _, key, m := signing(t)
	payload := jsonBytes(t, m)
	token := sign(t, key, m)
	parts := strings.Split(token, ".")
	cases := map[string]string{
		"empty": "", "size limit": strings.Repeat("a", constants.ExecutionLeaseMaxBytes), "above size limit": strings.Repeat("a", constants.ExecutionLeaseMaxBytes+1),
		"two segments": parts[0] + "." + parts[1], "four segments": token + ".x", "empty segment": "." + parts[1] + "." + parts[2],
		"padding": parts[0] + "=." + parts[1] + "." + parts[2], "newline": token + "\n", "whitespace": " " + token,
		"standard alphabet": parts[0] + "." + parts[1] + ".+/", "signature length": parts[0] + "." + parts[1] + ".AA",
	}
	for name, header := range map[string]string{
		"array": `[]`, "null": `null`, "duplicate": `{"alg":"EdDSA","alg":"EdDSA","typ":"a2matex-execution-lease+jws"}`,
		"escaped duplicate": `{"alg":"EdDSA","\u0061lg":"EdDSA","typ":"a2matex-execution-lease+jws"}`,
		"unknown":           `{"alg":"EdDSA","jwk":"PRIVATE","typ":"a2matex-execution-lease+jws"}`,
		"wrong typ":         `{"alg":"EdDSA","kid":"test-key","typ":"JWT"}`,
		"wrong alg":         `{"alg":"none","kid":"test-key","typ":"a2matex-execution-lease+jws"}`,
		"unknown kid":       `{"alg":"EdDSA","kid":"untrusted","typ":"a2matex-execution-lease+jws"}`,
		"wrong type":        `{"alg":"EdDSA","kid":1,"typ":"a2matex-execution-lease+jws"}`,
		"case":              `{"Alg":"EdDSA","kid":"test-key","typ":"a2matex-execution-lease+jws"}`,
		"trailing":          `{"alg":"EdDSA","kid":"test-key","typ":"a2matex-execution-lease+jws"} {}`,
	} {
		cases["header "+name] = signRaw(t, key, []byte(header), payload)
	}
	header := []byte(`{"alg":"EdDSA","kid":"test-key","typ":"a2matex-execution-lease+jws"}`)
	for name, raw := range map[string]string{
		"null": `null`, "array": `[]`, "trailing": string(payload) + ` {}`,
		"duplicate":         strings.TrimSuffix(string(payload), "}") + `,"sequence":1}`,
		"escaped duplicate": strings.TrimSuffix(string(payload), "}") + `,"\u0073equence":1}`,
		"unknown":           strings.TrimSuffix(string(payload), "}") + `,"PRIVATE":1}`,
		"case":              strings.Replace(string(payload), `"userId"`, `"UserId"`, 1),
		"missing":           strings.Replace(string(payload), `"sequence":1,`, "", 1),
		"UTF8":              strings.TrimSuffix(string(payload), "}") + ",\"PRIVATE\":\"\xff\"}",
	} {
		cases["payload "+name] = signRaw(t, key, header, []byte(raw))
	}
	// Ed25519's last base64url character has unused bits; changing those bits
	// must not create a second accepted encoding of the same signature bytes.
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, parts[2][len(parts[2])-1])
	cases["trailing bits"] = token[:len(token)-1] + string(alphabet[last+1])
	for name, compact := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := v.Verify(context.Background(), Request{Compact: compact, Mode: constants.LeaseVerificationInitial, Expected: expected(m)})
			assertFailure(t, r, err, xerror.ErrExecutionLeaseInvalid)
		})
	}
	// Correctly signed boundary tokens isolate the byte cap from parse failures.
	for _, size := range []int{constants.ExecutionLeaseMaxBytes, constants.ExecutionLeaseMaxBytes + 1} {
		var compact string
		for padding := range 3 {
			h := append([]byte(strings.Repeat(" ", padding)), header...)
			encodedSize := size - base64.RawURLEncoding.EncodedLen(len(h)) - base64.RawURLEncoding.EncodedLen(ed25519.SignatureSize) - 2
			rawSize := base64.RawURLEncoding.DecodedLen(encodedSize)
			if base64.RawURLEncoding.EncodedLen(rawSize) == encodedSize {
				compact = signRaw(t, key, h, []byte(string(payload)+strings.Repeat(" ", rawSize-len(payload))))
				break
			}
		}
		if len(compact) != size {
			t.Fatal("test token size")
		}
		r, err := v.Verify(context.Background(), Request{Compact: compact, Mode: constants.LeaseVerificationInitial, Expected: expected(m)})
		if size > constants.ExecutionLeaseMaxBytes {
			assertFailure(t, r, err, xerror.ErrExecutionLeaseInvalid)
		} else if err != nil {
			t.Fatalf("exact byte cap: %v", err)
		}
	}
	// Key order and whitespace are not signature canonicalization requirements.
	spaced := append([]byte("\n "), payload...)
	r, err := v.Verify(context.Background(), Request{Compact: signRaw(t, key, header, spaced), Mode: constants.LeaseVerificationInitial, Expected: expected(m)})
	if err != nil || r.Grant == nil {
		t.Fatalf("valid received signing bytes: %v", err)
	}
}

func TestClaimScalarsAndBindings(t *testing.T) {
	v, _, key, original := signing(t)
	cases := []struct {
		name, field string
		value       any
	}{
		{"fraction", "sequence", 1.5}, {"exponent", "sequence", json.RawMessage(`1e0`)}, {"string", "sequence", "1"}, {"null", "sequence", nil}, {"boolean", "sequence", true}, {"nested", "sequence", map[string]int{"PRIVATE": 1}},
		{"zero", "sequence", 0}, {"overflow", "sequence", 2147483648}, {"int64 overflow", "issuedAt", json.RawMessage(`9223372036854775808`)},
		{"negative time", "issuedAt", -1}, {"timestamp range", "expiresAt", 253402300800}, {"milliseconds", "expiresAt", 1795696200000},
		{"UUID version", "leaseId", "77777777-7777-5777-8777-777777777777"}, {"UUID variant", "leaseId", "77777777-7777-4777-0777-777777777777"},
		{"UUID case", "leaseId", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}, {"UUID alternate", "leaseId", "77777777777747778777777777777777"},
		{"hash case", "packageHash", strings.Repeat("A", 64)}, {"hash short", "packageHash", "ab"},
	}
	for _, field := range []string{"executionId", "productId", "releaseId", "runtimeSessionId"} {
		cases = append(cases, struct {
			name, field string
			value       any
		}{"binding " + field, field, uuid.NewString()})
	}
	for _, field := range []string{"packageHash", "runtimeKeyThumbprint"} {
		cases = append(cases, struct {
			name, field string
			value       any
		}{"binding " + field, field, strings.Repeat("b", 64)})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := make(map[string]any, len(original))
			for k, value := range original {
				m[k] = value
			}
			m[c.field] = c.value
			r, err := v.Verify(context.Background(), Request{Compact: sign(t, key, m), Mode: constants.LeaseVerificationInitial, Expected: expected(original)})
			assertFailure(t, r, err, xerror.ErrExecutionLeaseInvalid)
		})
	}
}
