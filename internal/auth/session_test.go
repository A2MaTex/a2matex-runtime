package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestNewSession(t *testing.T) {
	t.Parallel()

	first, err := newSession(bytes.NewReader(bytes.Repeat([]byte{0x11}, constants.SessionTokenBytes)))
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}
	second, err := newSession(bytes.NewReader(bytes.Repeat([]byte{0x22}, constants.SessionTokenBytes)))
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}

	if first.ConnectionToken() == second.ConnectionToken() {
		t.Fatal("newSession() produced the same credential for different random input")
	}
	if len(first.ConnectionToken()) != constants.SessionTokenEncodedLength {
		t.Fatalf("encoded token length = %d, want %d", len(first.ConnectionToken()), constants.SessionTokenEncodedLength)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first.ConnectionToken())
	if err != nil {
		t.Fatalf("decode generated token: %v", err)
	}
	if len(decoded) != constants.SessionTokenBytes {
		t.Fatalf("decoded token length = %d, want %d", len(decoded), constants.SessionTokenBytes)
	}
}

func TestNewSessionRejectsRandomnessFailure(t *testing.T) {
	t.Parallel()

	_, err := newSession(errorReader{})
	if !errors.Is(err, xerror.ErrGenerateLocalAuthSession) {
		t.Fatalf("newSession() error = %v, want %v", err, xerror.ErrGenerateLocalAuthSession)
	}
}

func TestSessionVerifyAuthorization(t *testing.T) {
	t.Parallel()

	session, err := newSession(bytes.NewReader(bytes.Repeat([]byte{0x33}, constants.SessionTokenBytes)))
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}
	token := session.ConnectionToken()
	wrongToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, constants.SessionTokenBytes))

	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "exact credential", values: []string{"Bearer " + token}, want: true},
		{name: "case insensitive scheme", values: []string{"bEaReR " + token}, want: true},
		{name: "missing header"},
		{name: "duplicate header", values: []string{"Bearer " + token, "Bearer " + token}},
		{name: "missing credential", values: []string{"Bearer "}},
		{name: "wrong scheme", values: []string{"Basic " + token}},
		{name: "leading whitespace", values: []string{" Bearer " + token}},
		{name: "two spaces", values: []string{"Bearer  " + token}},
		{name: "trailing whitespace", values: []string{"Bearer " + token + " "}},
		{name: "invalid base64", values: []string{"Bearer " + token[:len(token)-1] + "*"}},
		{name: "short credential", values: []string{"Bearer " + token[:len(token)-1]}},
		{name: "wrong credential", values: []string{"Bearer " + wrongToken}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := session.VerifyAuthorization(test.values); got != test.want {
				t.Fatalf("VerifyAuthorization() = %t, want %t", got, test.want)
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("randomness unavailable")
}
