package executionlease

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/validation/keyenvelope"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func TestBackendV3LeaseToEnvelopeHandoff(t *testing.T) {
	// Keep this test in executionlease so fixed-clock acceptance needs no public
	// test hooks or fabricated Verified value in the envelope package.
	raw, err := os.ReadFile("../keyenvelope/testdata/v3/protocol-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != "87141b2bd409b53da2c971c9ee9dc2e3a2e789972592e0aaa36f50d54f143e72" {
		t.Fatal("accepted Backend v3 fixture changed")
	}
	var f struct {
		FixedClock       time.Time
		VerificationKeys map[string]string
		Request          struct{ ExecutionID, ProductID, ReleaseID, PackageHash, RuntimeSessionID, RuntimeKeyThumbprint string }
		Authorization    struct{ Lease string }
		TestKeys         struct{ RuntimePrivateKeyPkcs8Pem, ContentKeyBase64 string }
		Cases            []struct {
			Name, Expected string
			Envelope       json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(f.TestKeys.RuntimePrivateKeyPkcs8Pem))
	if block == nil {
		t.Fatal("fixture PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal("fixture RSA")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		t.Fatal("fixture key type")
	}
	content, err := base64.StdEncoding.DecodeString(f.TestKeys.ContentKeyBase64)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "tampered", "expired"} {
		t.Run(mode, func(t *testing.T) {
			v, err := New(Options{VerificationKeys: f.VerificationKeys})
			if err != nil {
				t.Fatal(err)
			}
			v.anchor = f.FixedClock
			elapsed := time.Duration(0)
			v.since = func(time.Time) time.Duration { return elapsed }
			compact := f.Authorization.Lease
			if mode == "tampered" {
				compact += "invalid"
			}
			if mode == "expired" {
				elapsed = 1800 * time.Second
			}
			result, err := v.Verify(context.Background(), Request{Compact: compact, Mode: constants.LeaseVerificationInitial, Expected: Expected{
				ExecutionID: f.Request.ExecutionID, ProductID: f.Request.ProductID, ReleaseID: f.Request.ReleaseID,
				PackageHash: f.Request.PackageHash, RuntimeSessionID: f.Request.RuntimeSessionID, RuntimeKeyThumbprint: f.Request.RuntimeKeyThumbprint,
			}})
			opened := 0
			if err == nil {
				claims := result.Grant.Claims()
				expected := keyenvelope.Expected{ReleaseID: claims.ReleaseID, RuntimeSessionID: claims.RuntimeSessionID, RuntimeKeyThumbprint: claims.RuntimeKeyThumbprint}
				for _, sample := range f.Cases {
					if sample.Expected != "OPEN" {
						continue
					}
					var alias []byte
					err := keyenvelope.WithContentKey(context.Background(), keyenvelope.Request{Envelope: sample.Envelope, Expected: expected, PrivateKey: key}, func(_ context.Context, k []byte) error {
						opened++
						alias = k
						if !bytes.Equal(k, content) {
							t.Fatal("Backend key differs after actual J6 acceptance")
						}
						return nil
					})
					if err != nil || !bytes.Equal(alias, make([]byte, constants.ProductContentKeyBytes)) {
						t.Fatalf("handoff or cleanup failed: %v", err)
					}
				}
			}
			if mode == "valid" && (err != nil || opened != 2) {
				t.Fatalf("valid handoff failed: %v", err)
			}
			if mode != "valid" && (err == nil || opened != 0 || result.Grant != nil) {
				t.Fatal("rejected lease reached opening")
			}
		})
	}
}
