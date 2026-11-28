package keyenvelope

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Accepted public test material from a2matex-plan/components/a2matex-runtime/
// j_protected_execution/fixtures/v3.
// No keys are generated or fixture bytes rewritten by the normal test run.
const fixtureHash = "87141b2bd409b53da2c971c9ee9dc2e3a2e789972592e0aaa36f50d54f143e72"

type vectors struct {
	TestOnly      bool
	Request       struct{ ReleaseID, RuntimeSessionID, RuntimeKeyThumbprint string }
	Authorization struct{ KeyEnvelope json.RawMessage }
	TestKeys      struct{ RuntimePrivateKeyPkcs8Pem, ContentKeyBase64 string }
	Cases         []struct {
		Name, Expected string
		Envelope       json.RawMessage
	}
}

func fixture(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/v3/protocol-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile("testdata/v3/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version, CaseCount int
		Files              []struct{ Path, SHA256 string }
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if m.Version != 3 || m.CaseCount != 11 || len(m.Files) != 1 || m.Files[0].Path != "protocol-vectors.json" || m.Files[0].SHA256 != fixtureHash || hex.EncodeToString(digest[:]) != fixtureHash {
		t.Fatal("accepted Backend v3 fixture changed")
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if !v.TestOnly || len(v.Cases) != m.CaseCount {
		t.Fatal("fixture metadata")
	}
	return v
}

func request(t *testing.T) (Request, []byte) {
	t.Helper()
	v := fixture(t)
	block, _ := pem.Decode([]byte(v.TestKeys.RuntimePrivateKeyPkcs8Pem))
	if block == nil {
		t.Fatal("fixture private PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal("fixture private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		t.Fatal("fixture RSA type")
	}
	content, err := base64.StdEncoding.DecodeString(v.TestKeys.ContentKeyBase64)
	if err != nil || len(content) != constants.ProductContentKeyBytes {
		t.Fatal("fixture content key")
	}
	return Request{Envelope: v.Authorization.KeyEnvelope, PrivateKey: key, Expected: Expected{
		ReleaseID: v.Request.ReleaseID, RuntimeSessionID: v.Request.RuntimeSessionID,
		RuntimeKeyThumbprint: v.Request.RuntimeKeyThumbprint,
	}}, content
}

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func edit(t *testing.T, raw []byte, change func(map[string]any)) []byte {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	change(fields)
	return encoded(t, fields)
}

func assertFailure(t *testing.T, r Request, want error) {
	t.Helper()
	called := false
	err := WithContentKey(context.Background(), r, func(context.Context, []byte) error { called = true; return nil })
	if !errors.Is(err, want) || called {
		t.Fatalf("wrong failure or callback invoked: %v", err)
	}
	var detail *xerror.OperationError
	if !errors.As(err, &detail) || detail.Operation == "" || detail.Detail == "" {
		t.Fatal("missing safe error context")
	}
	protectedValues := []string{"PRIVATE", string(r.Envelope)}
	if len(r.Expected.RuntimeKeyThumbprint) == sha256.Size*2 {
		protectedValues = append(protectedValues, r.Expected.RuntimeKeyThumbprint)
	}
	for _, protected := range protectedValues {
		if protected != "" && strings.Contains(err.Error()+detail.Detail, protected) {
			t.Fatal("error exposed protected input")
		}
	}
}

func TestBackendV3Envelopes(t *testing.T) {
	for _, sample := range fixture(t).Cases {
		t.Run(sample.Name, func(t *testing.T) {
			r, content := request(t)
			r.Envelope = sample.Envelope
			if sample.Expected != "OPEN" {
				assertFailure(t, r, xerror.ErrProductKeyEnvelopeInvalid)
				return
			}
			before := x509.MarshalPKCS1PrivateKey(r.PrivateKey)
			var borrowed []byte
			calls := 0
			err := WithContentKey(context.Background(), r, func(_ context.Context, key []byte) error {
				calls++
				borrowed = key
				if !bytes.Equal(key, content) || cap(key) != len(key) {
					t.Fatal("wrong key borrow")
				}
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("opening: %v", err)
			}
			if !bytes.Equal(borrowed, make([]byte, constants.ProductContentKeyBytes)) {
				t.Fatal("key not cleared")
			}
			if !bytes.Equal(before, x509.MarshalPKCS1PrivateKey(r.PrivateKey)) {
				t.Fatal("borrowed RSA key changed")
			}
		})
	}
}

func TestStrictEnvelopeJSON(t *testing.T) {
	r, _ := request(t)
	raw := string(r.Envelope)
	add := func(field string) []byte {
		return []byte(strings.TrimSuffix(strings.TrimSpace(raw), "}") + "," + field + "}")
	}
	cases := map[string][]byte{
		"empty": nil, "whitespace": []byte(" \n"), "null": []byte("null"), "array": []byte("[" + raw + "]"),
		"malformed": []byte("{"), "trailing": []byte(raw + " {}"),
		"BOM":                 append([]byte{0xef, 0xbb, 0xbf}, r.Envelope...),
		"invalid UTF8":        []byte("{\"x\":\"\xff\"}"),
		"unknown":             add(`"extra":"PRIVATE"`),
		"duplicate":           add(`"releaseId":"PRIVATE"`),
		"escaped duplicate":   add(`"release\u0049d":"PRIVATE"`),
		"missing":             edit(t, r.Envelope, func(m map[string]any) { delete(m, "algorithm") }),
		"wrong case":          edit(t, r.Envelope, func(m map[string]any) { m["Algorithm"] = m["algorithm"]; delete(m, "algorithm") }),
		"replacement unknown": edit(t, r.Envelope, func(m map[string]any) { delete(m, "algorithm"); m["extra"] = "PRIVATE" }),
	}
	for _, value := range []any{nil, false, 32, map[string]any{}, []any{}} {
		cases[fmt.Sprintf("type %T", value)] = edit(t, r.Envelope, func(m map[string]any) { m["algorithm"] = value })
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			changed := r
			changed.Envelope = value
			assertFailure(t, changed, xerror.ErrProductKeyEnvelopeInvalid)
		})
	}
	// JSON escaping and field order are allowed; duplicate decoded names are not.
	r.Envelope = []byte(strings.Replace(raw, `"releaseId"`, `"release\u0049d"`, 1))
	if err := WithContentKey(context.Background(), r, func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeByteAndEncodingBounds(t *testing.T) {
	r, _ := request(t)
	r.Envelope = append(r.Envelope, bytes.Repeat([]byte(" "), constants.ProductKeyEnvelopeMaxBytes-len(r.Envelope))...)
	if err := WithContentKey(context.Background(), r, func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	r.Envelope = append(r.Envelope, ' ')
	assertFailure(t, r, xerror.ErrProductKeyEnvelopeInvalid)
	r, _ = request(t)
	var fields map[string]string
	if err := json.Unmarshal(r.Envelope, &fields); err != nil {
		t.Fatal(err)
	}
	s := fields["encryptedKey"]
	url := strings.NewReplacer("+", "-", "/", "_").Replace(s)
	if url == s {
		t.Fatal("fixture must distinguish the Base64 alphabets")
	}
	for name, value := range map[string]string{
		"short": s[:len(s)-1], "long": s + "A", "padding": s + "=", "padded short": s[:len(s)-2] + "==",
		"URL alphabet": url, "newline": s[:16] + "\n" + s[16:], "CR": s[:16] + "\r" + s[16:],
		"space": s[:16] + " " + s[17:], "invalid": "!" + s[1:],
	} {
		t.Run(name, func(t *testing.T) {
			changed := r
			changed.Envelope = edit(t, r.Envelope, func(m map[string]any) { m["encryptedKey"] = value })
			assertFailure(t, changed, xerror.ErrProductKeyEnvelopeInvalid)
		})
	}
}

func TestTrustedCallerAndKeyValidation(t *testing.T) {
	r, _ := request(t)
	for name, change := range map[string]func(*Request){
		"missing expected":   func(r *Request) { r.Expected = Expected{} },
		"uppercase UUID":     func(r *Request) { r.Expected.ReleaseID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" },
		"wrong UUID version": func(r *Request) { r.Expected.RuntimeSessionID = "aaaaaaaa-aaaa-1aaa-8aaa-aaaaaaaaaaaa" },
		"wrong UUID variant": func(r *Request) { r.Expected.RuntimeSessionID = "aaaaaaaa-aaaa-4aaa-7aaa-aaaaaaaaaaaa" },
		"uppercase hash":     func(r *Request) { r.Expected.RuntimeKeyThumbprint = strings.Repeat("A", 64) },
		"short hash":         func(r *Request) { r.Expected.RuntimeKeyThumbprint = "a" },
		"nil key":            func(r *Request) { r.PrivateKey = nil },
		"missing N":          func(r *Request) { r.PrivateKey.N = nil },
		"missing D":          func(r *Request) { r.PrivateKey.D = nil },
		"wrong exponent":     func(r *Request) { r.PrivateKey.E = 3 },
		"small modulus":      func(r *Request) { r.PrivateKey.N = big.NewInt(17) },
		"missing primes":     func(r *Request) { r.PrivateKey.Primes = nil },
		"missing P":          func(r *Request) { r.PrivateKey.Primes[0] = nil },
		"missing Q":          func(r *Request) { r.PrivateKey.Primes[1] = nil },
		"multiprime":         func(r *Request) { r.PrivateKey.Primes = append(r.PrivateKey.Primes, big.NewInt(3)) },
		"invalid D":          func(r *Request) { r.PrivateKey.D = big.NewInt(1) },
		"zero P":             func(r *Request) { r.PrivateKey.Primes[0] = new(big.Int) },
	} {
		t.Run(name, func(t *testing.T) {
			changed, _ := request(t)
			change(&changed)
			assertFailure(t, changed, xerror.ErrProductKeyEnvelopeConfiguration)
		})
	}
	if err := WithContentKey(context.Background(), r, nil); !errors.Is(err, xerror.ErrProductKeyEnvelopeConfiguration) {
		t.Fatal("nil callback classification")
	}
	other, err := rsa.GenerateKey(rand.Reader, constants.ProductSessionRSAKeyBits)
	if err != nil {
		t.Fatal(err)
	}
	r.PrivateKey = other
	assertFailure(t, r, xerror.ErrProductKeyEnvelopeConfiguration)
}

func TestOpenedKeyLength(t *testing.T) {
	r, _ := request(t)
	for _, size := range []int{0, 31, 33} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &r.PrivateKey.PublicKey, make([]byte, size), nil)
			if err != nil {
				t.Fatal(err)
			}
			changed := r
			changed.Envelope = edit(t, r.Envelope, func(m map[string]any) { m["encryptedKey"] = base64.StdEncoding.EncodeToString(ciphertext) })
			assertFailure(t, changed, xerror.ErrProductKeyEnvelopeInvalid)
		})
	}
}

func TestCallbackCleanupAndCancellation(t *testing.T) {
	for _, mode := range []string{"success", "error", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			r, content := request(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := errors.New("synthetic callback failure")
			var borrowed []byte
			calls := 0
			panicked := false
			err := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						panicked = true
					}
				}()
				return WithContentKey(ctx, r, func(got context.Context, key []byte) error {
					calls++
					borrowed = key
					if got != ctx || !bytes.Equal(key, content) {
						t.Fatal("callback contract")
					}
					switch mode {
					case "error":
						return want
					case "cancel":
						cancel()
					case "panic":
						panic("synthetic callback panic")
					}
					return nil
				})
			}()
			if calls != 1 || !bytes.Equal(borrowed, make([]byte, constants.ProductContentKeyBytes)) {
				t.Fatal("callback or wipe contract")
			}
			switch mode {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "error":
				if !errors.Is(err, want) {
					t.Fatal("callback error lost")
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost")
				}
			case "panic":
				if !panicked {
					t.Fatal("panic swallowed")
				}
			}
		})
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := error(context.Canceled)
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		r, _ := request(t)
		called := false
		err := WithContentKey(ctx, r, func(context.Context, []byte) error { called = true; return nil })
		cancel()
		if !errors.Is(err, want) || called {
			t.Fatal("cancelled call reached callback")
		}
	}
}

func TestIndependentConcurrentOpenings(t *testing.T) {
	const count = 6
	requests := make([]Request, count)
	var content []byte
	for i := range count {
		requests[i], content = request(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var entered atomic.Int32
	ready, failedDone := make(chan struct{}), make(chan struct{})
	failures := make(chan error, count)
	want := errors.New("one callback failed")
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 0 {
				defer close(failedDone)
			}
			var borrowed []byte
			err := WithContentKey(ctx, requests[i], func(_ context.Context, key []byte) error {
				borrowed = key
				if entered.Add(1) == count {
					close(ready)
				}
				select {
				case <-ready:
				case <-ctx.Done():
					return ctx.Err()
				}
				if i == 0 {
					return want
				}
				select {
				case <-failedDone:
				case <-ctx.Done():
					return ctx.Err()
				}
				if !bytes.Equal(key, content) {
					return errors.New("another call changed this key")
				}
				return nil
			})
			if (i == 0 && !errors.Is(err, want)) || (i != 0 && err != nil) || !bytes.Equal(borrowed, make([]byte, constants.ProductContentKeyBytes)) {
				failures <- errors.New("independent opening or cleanup failed")
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestPrivateRequestSerialization(t *testing.T) {
	r, _ := request(t)
	for _, value := range []any{r, r.Expected} {
		if string(encoded(t, value)) != "{}" {
			t.Fatal("private request serialized")
		}
	}
}

// Generated once using the accepted public v3 SPKI/K with OpenSSL pkeyutl:
// -encrypt -pubin -keyform DER -pkeyopt rsa_padding_mode:oaep
// -pkeyopt rsa_oaep_md:sha256 -pkeyopt rsa_mgf1_md:sha1.
// Unlike the Backend wrong-hash vector, only MGF1 differs. Tests need no OpenSSL.
func TestWrongMGFHash(t *testing.T) {
	const ciphertext = "jclnzOVPbQRe2EJbZT9l0oofX0Ni6vDP8YDnoKvX1bWFyJBN6dlzNaHfTo9uL8vTsBu/alcwXJDwFR2VPYnqnLtmi4ptexM9mZm0TAsmuCsOXnpDJzMiIUE6br+ygNJteXB9QIND4Ked5grinE2Wn3bFjS2fzHb37pVVhhM6vAMbuMugc6Mo48C3nsGOr0C7QVptP/ZRXa15s2OQkUvdonhJwGiwcBzCJOICJkrePzgHh9EKJzFsxebNJ9SURss+/hBhfMgaBtoeZTxe3kXe6iB8wV+QeGnjWmDXo0xcK8umDEpncKonzU9ckyCKoLtvuVa1AMZS1GcTol2y4s1+08wOHymn0XlbaCgxQ9tfeX2Q2vZiMZrOgwJaV099ChqotmZgEIYnfIoo+hpe4wuLu8usoNwk4pFzQOlqB+J6jOin7y6SQxsDo4zDRRLppYR2hz87f8hd3RUN6dax9N0h4VVw6Q6JdAmYik14F/w11txoq6rH0NicTMTVjP3Z2v41"
	r, _ := request(t)
	r.Envelope = edit(t, r.Envelope, func(m map[string]any) { m["encryptedKey"] = ciphertext })
	assertFailure(t, r, xerror.ErrProductKeyEnvelopeInvalid)
}
