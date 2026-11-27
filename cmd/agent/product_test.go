package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func productTestTrust(t *testing.T) map[string]string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Agent.Test": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
}

func TestProductTrustComposition(t *testing.T) {
	cfg := config.Config{Production: config.ProductionConfig{ClockSkew: 30 * time.Second}}
	if verifier, err := newProductLeaseVerifier(cfg); err != nil || verifier != nil {
		t.Fatal("disabled trust", err)
	}
	cfg.Production.AuthorizationEnabled = true
	if _, err := newProductLeaseVerifier(cfg); !errors.Is(err, xerror.ErrExecutionLeaseConfiguration) {
		t.Fatal("empty enabled trust", err)
	}
	cfg.Production.VerificationKeys = productTestTrust(t)
	if verifier, err := newProductLeaseVerifier(cfg); err != nil || verifier == nil {
		t.Fatal("valid trust", err)
	}
	cfg.Production.VerificationKeys["Agent.Test"] = "PRIVATE invalid key"
	if _, err := newProductLeaseVerifier(cfg); !errors.Is(err, xerror.ErrExecutionLeaseConfiguration) {
		t.Fatal("bad enabled trust", err)
	}
}
