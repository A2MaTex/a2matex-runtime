package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Session owns the current Runtime process's local authentication credential.
type Session struct {
	token [constants.SessionTokenBytes]byte
}

// NewSession creates one process-local credential from the operating system's
// cryptographic random source.
func NewSession() (*Session, error) {
	return newSession(rand.Reader)
}

func newSession(random io.Reader) (*Session, error) {
	session := &Session{}
	if _, err := io.ReadFull(random, session.token[:]); err != nil {
		return nil, xerror.ErrGenerateLocalAuthSession
	}

	return session, nil
}

// VerifyAuthorization reports whether values contain exactly one strict bearer
// credential for this Runtime process.
func (s *Session) VerifyAuthorization(values []string) bool {
	// Accept exactly one header and one space between scheme and credential.
	// This removes ambiguity between clients and future HTTP middleware.
	if len(values) != 1 {
		return false
	}

	header := values[0]
	if len(header) != constants.BearerPrefixLength+constants.SessionTokenEncodedLength ||
		header[constants.BearerPrefixLength-1] != ' ' ||
		!strings.EqualFold(header[:constants.BearerPrefixLength-1], "Bearer") {
		return false
	}

	candidate, err := base64.RawURLEncoding.DecodeString(header[constants.BearerPrefixLength:])
	if err != nil || len(candidate) != constants.SessionTokenBytes {
		return false
	}

	// Comparing decoded fixed-length bytes avoids leaking which token prefix
	// matched through ordinary short-circuit string comparison.
	return subtle.ConstantTimeCompare(candidate, s.token[:]) == 1
}

// ConnectionToken returns the encoded credential written to the local
// connection document. Callers must never log or otherwise persist it.
func (s *Session) ConnectionToken() string {
	return base64.RawURLEncoding.EncodeToString(s.token[:])
}
