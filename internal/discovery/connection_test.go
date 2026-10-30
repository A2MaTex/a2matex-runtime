package discovery

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestEncodeConnectionDocument(t *testing.T) {
	t.Parallel()

	session, err := auth.NewSession()
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}

	encoded, err := encodeConnectionDocument("127.0.0.1:6810", session)
	if err != nil {
		t.Fatalf("encodeConnectionDocument() error = %v", err)
	}

	var document map[string]string
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode connection document: %v", err)
	}
	if len(document) != 2 {
		t.Fatalf("connection document field count = %d, want 2", len(document))
	}
	if got := document["apiBaseUrl"]; got != "http://127.0.0.1:6810/api/v1" {
		t.Fatalf("apiBaseUrl = %q", got)
	}
	if got := document["sessionToken"]; got != session.ConnectionToken() {
		t.Fatal("sessionToken does not match the current session")
	}
}

func TestEncodeConnectionDocumentRejectsInvalidAddress(t *testing.T) {
	t.Parallel()

	session, err := auth.NewSession()
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}

	_, err = encodeConnectionDocument("0.0.0.0:6810", session)
	if !errors.Is(err, xerror.ErrEncodeLocalAuthConnection) {
		t.Fatalf("encodeConnectionDocument() error = %v, want %v", err, xerror.ErrEncodeLocalAuthConnection)
	}
}

func TestEncodeConnectionDocumentUsesCanonicalIPv6URL(t *testing.T) {
	t.Parallel()

	session, err := auth.NewSession()
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}

	encoded, err := encodeConnectionDocument("[::1]:6810", session)
	if err != nil {
		t.Fatalf("encodeConnectionDocument() error = %v", err)
	}

	var document connectionDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode connection document: %v", err)
	}
	if document.APIBaseURL != "http://[::1]:6810/api/v1" {
		t.Fatalf("apiBaseUrl = %q", document.APIBaseURL)
	}
}
