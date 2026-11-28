package keyenvelope

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// WithContentKey lends the recovered key synchronously, then clears its buffer.
// The caller must verify the lease first and borrow PrivateKey through J5 Material.Use.
// The callback must not retain the key or start asynchronous work with it.
func WithContentKey(ctx context.Context, request Request, use func(context.Context, []byte) error) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("open_product_content_key", err)
	}
	if !validExpected(request.Expected) || use == nil {
		return configuration("open_product_content_key", "caller_invalid")
	}
	ciphertext, err := decode(ctx, request.Envelope, request.Expected)
	if err != nil {
		return err
	}
	if err := matchKey(request.PrivateKey, request.Expected.RuntimeKeyThumbprint); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("open_product_content_key", err)
	}
	// OAEP is bounded but cannot be interrupted. Keep it inside the caller's borrow.
	plaintext, err := rsa.DecryptOAEP(sha256.New(), nil, request.PrivateKey, ciphertext, nil)
	defer clear(plaintext)
	if contextErr := ctx.Err(); contextErr != nil {
		return xerror.WithOperation("open_product_content_key", contextErr)
	}
	if err != nil {
		return invalid("open_product_content_key", "opening_failed")
	}
	if len(plaintext) != constants.ProductContentKeyBytes {
		return invalid("open_product_content_key", "plaintext_length_invalid")
	}
	err = use(ctx, plaintext[:len(plaintext):len(plaintext)])
	if contextErr := ctx.Err(); contextErr != nil {
		return xerror.WithOperation("use_product_content_key", contextErr)
	}
	return xerror.WithOperation("use_product_content_key", err)
}

func matchKey(key *rsa.PrivateKey, thumbprint string) error {
	if key == nil || key.N == nil || key.D == nil || key.N.Sign() <= 0 || key.D.Sign() <= 0 ||
		key.N.BitLen() != constants.ProductSessionRSAKeyBits || key.E != constants.ProductSessionRSAExponent ||
		len(key.Primes) != 2 || key.Primes[0] == nil || key.Primes[1] == nil ||
		key.Primes[0].Sign() <= 0 || key.Primes[1].Sign() <= 0 {
		return configuration("match_product_key_binding", "key_invalid")
	}
	if key.Validate() != nil {
		return configuration("match_product_key_binding", "key_invalid")
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return configuration("match_product_key_binding", "key_invalid")
	}
	digest := sha256.Sum256(der)
	if hex.EncodeToString(digest[:]) != thumbprint {
		return configuration("match_product_key_binding", "key_binding_invalid")
	}
	return nil
}
