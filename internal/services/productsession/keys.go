package productsession

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"sync"

	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func generateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, constants.ProductSessionRSAKeyBits)
}

func publicKey(key *rsa.PrivateKey) (string, string, error) {
	if key == nil || key.N == nil || key.D == nil || key.N.BitLen() != constants.ProductSessionRSAKeyBits || key.E != constants.ProductSessionRSAExponent || len(key.Primes) < 2 {
		return "", "", failure("generate_product_session_key", "key_shape_invalid", xerror.ErrProductSessionKeyGeneration)
	}
	for _, prime := range key.Primes {
		if prime == nil {
			return "", "", failure("generate_product_session_key", "key_shape_invalid", xerror.ErrProductSessionKeyGeneration)
		}
	}
	if key.Validate() != nil {
		return "", "", failure("generate_product_session_key", "key_invalid", xerror.ErrProductSessionKeyGeneration)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", failure("generate_product_session_key", "public_encoding_failed", xerror.ErrProductSessionKeyGeneration)
	}
	hash := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(der), hex.EncodeToString(hash[:]), nil
}

// Material is an exclusive, non-serializable transfer to the trusted loader.
// Close must be deferred immediately after successful Consume.
type Material struct {
	mu       sync.Mutex
	owner    context.Context
	key      *rsa.PrivateKey
	snapshot *packagevalidation.VerifiedPackage
}

func (m *Material) Use(ctx context.Context, operation func(*rsa.PrivateKey, *packagevalidation.VerifiedPackage) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("use_product_session_material", err)
	}
	if m.key == nil || m.snapshot == nil {
		return failure("use_product_session_material", "material_closed", xerror.ErrProductSessionUnavailable)
	}
	if err := m.owner.Err(); err != nil {
		return xerror.WithOperation("use_product_session_material", err)
	}
	if operation == nil {
		return failure("use_product_session_material", "operation_missing", xerror.ErrProductSessionInvalid)
	}
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.owner, cancel)
	defer func() { stop(); cancel() }()
	if m.owner.Err() != nil {
		cancel()
	}
	if err := work.Err(); err != nil {
		return xerror.WithOperation("use_product_session_material", err)
	}
	err := operation(m.key, m.snapshot)
	if ownerErr := m.owner.Err(); ownerErr != nil {
		err = ownerErr
	} else if work.Err() != nil {
		err = work.Err()
	}
	return xerror.WithOperation("use_product_session_material", err)
}

func (m *Material) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	wipeKey(m.key)
	m.key, m.snapshot = nil, nil
}

// Best effort only: Go and its crypto backend can retain unobservable copies.
func wipeKey(key *rsa.PrivateKey) {
	if key == nil {
		return
	}
	wipeInt(key.D)
	for _, prime := range key.Primes {
		wipeInt(prime)
	}
	wipeInt(key.Precomputed.Dp)
	wipeInt(key.Precomputed.Dq)
	wipeInt(key.Precomputed.Qinv)
	for _, value := range key.Precomputed.CRTValues {
		wipeInt(value.Exp)
		wipeInt(value.Coeff)
		wipeInt(value.R)
	}
	*key = rsa.PrivateKey{}
}

func wipeInt(value *big.Int) {
	if value != nil {
		clear(value.Bits())
		value.SetInt64(0)
	}
}
