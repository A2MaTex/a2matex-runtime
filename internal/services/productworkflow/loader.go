package productworkflow

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Request struct {
	Package      *packagevalidation.VerifiedPackage `json:"-"`
	Expected     packagevalidation.PublicPackage    `json:"-"`
	ContentKey   []byte                             `json:"-"`
	DeviceInputs []json.RawMessage                  `json:"-"`
}

type Loader struct{ validator *runtimeir.Validator }

func New(validator *runtimeir.Validator) (*Loader, error) {
	if validator == nil {
		return nil, configuration("configure_product_workflow", "validator_missing")
	}
	return &Loader{validator: validator}, nil
}

// Load borrows captured material inside J7's key callback. It does not authorize or admit.
func (l *Loader) Load(ctx context.Context, request Request) (*Loaded, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("load_product_workflow", err)
	}
	if l == nil || l.validator == nil || request.Package == nil || request.Package.Snapshot == nil ||
		!validExpected(request.Expected) || len(request.ContentKey) != constants.ProductContentKeyBytes {
		return nil, configuration("load_product_workflow", "caller_invalid")
	}
	p := request.Package
	if !matchesCapture(p, request.Expected) {
		return nil, invalid("load_product_workflow", "package_binding_mismatch")
	}
	if p.Manifest.Workflow.Encryption.Algorithm != constants.ProductPackageEncryptionAlgorithm ||
		p.Manifest.Workflow.Encryption.AuthenticationTagBytes != constants.ProductPackageGCMTagBytes {
		return nil, invalid("decrypt_product_workflow", "gcm_invalid")
	}
	if len(p.Snapshot.WorkflowBytes) < constants.ProductPackageGCMTagBytes {
		return nil, invalid("decrypt_product_workflow", "gcm_invalid")
	}
	size := len(p.Snapshot.WorkflowBytes) - constants.ProductPackageGCMTagBytes
	if size > constants.RuntimeIRMaxContentBytes {
		return nil, invalid("load_product_workflow", "plaintext_too_large")
	}
	remaining := constants.RunRequestMaxContentBytes - size
	for _, inputs := range request.DeviceInputs {
		if err := ctx.Err(); err != nil {
			return nil, xerror.WithOperation("load_product_workflow", err)
		}
		if len(inputs) > remaining {
			return nil, invalid("load_product_workflow", "inputs_too_large")
		}
		remaining -= len(inputs)
	}
	plaintext, err := decrypt(ctx, request.ContentKey, p.Nonce, p.Snapshot.WorkflowBytes)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	checked, err := l.validator.Validate(ctx, plaintext, request.DeviceInputs)
	defer clear(checked.RawIR)
	candidate := &Loaded{Document: checked.Document, inputs: checked.DeviceInputs}
	transferred := false
	defer func() {
		if !transferred {
			candidate.Close()
		}
	}()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, xerror.WithOperation("validate_product_workflow", err)
		}
		if errors.Is(err, xerror.ErrRuntimeIRPayloadTooLarge) {
			return nil, invalid("validate_product_workflow", "content_too_large")
		}
		return nil, configuration("validate_product_workflow", "validator_failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("validate_product_workflow", err)
	}
	if checked.Validation.Status == validation.StatusInvalid {
		return nil, invalid("validate_product_workflow", "ir_invalid")
	}
	if (checked.Validation.Status != validation.StatusValid && checked.Validation.Status != validation.StatusValidWithWarnings) ||
		len(checked.Validation.Errors) != 0 || checked.FailureCode != "" || checked.Document == nil || len(checked.DeviceInputs) != len(request.DeviceInputs) {
		return nil, configuration("validate_product_workflow", "validator_result_invalid")
	}
	for _, inputs := range checked.DeviceInputs {
		if inputs == nil {
			return nil, configuration("validate_product_workflow", "validator_result_invalid")
		}
	}
	if err := checkConsistency(p, checked.Document); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(plaintext)
	candidate.RuntimeIRHash = hex.EncodeToString(digest[:])
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("load_product_workflow", err)
	}
	transferred = true
	return candidate, nil
}

// decrypt clears its full allocation on failure and transfers it only on success.
func decrypt(ctx context.Context, key, nonce, ciphertext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("decrypt_product_workflow", err)
	}
	if len(key) != constants.ProductContentKeyBytes {
		return nil, configuration("decrypt_product_workflow", "key_invalid")
	}
	if len(nonce) != constants.ProductPackageNonceBytes || len(ciphertext) < constants.ProductPackageGCMTagBytes {
		return nil, invalid("decrypt_product_workflow", "gcm_invalid")
	}
	size := len(ciphertext) - constants.ProductPackageGCMTagBytes
	if size > constants.RuntimeIRMaxContentBytes {
		return nil, invalid("decrypt_product_workflow", "plaintext_too_large")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, configuration("decrypt_product_workflow", "cipher_invalid")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, configuration("decrypt_product_workflow", "cipher_invalid")
	}
	buffer := make([]byte, 0, size)
	transferred := false
	defer func() {
		if !transferred {
			clear(buffer[:cap(buffer)])
		}
	}()
	plaintext, err := gcm.Open(buffer, nonce, ciphertext, nil)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, xerror.WithOperation("decrypt_product_workflow", contextErr)
	}
	if err != nil {
		return nil, invalid("decrypt_product_workflow", "gcm_invalid")
	}
	transferred = true
	return plaintext, nil
}

func invalid(operation, reason string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrProductPackageInvalid}
}

func configuration(operation, reason string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrProductWorkflowConfiguration}
}
