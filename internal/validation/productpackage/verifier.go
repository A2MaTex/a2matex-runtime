// Package productpackage verifies captured packages before public schema use.
package productpackage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	reader "github.com/A2MaTex/a2matex-runtime/internal/platform/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Request struct {
	ReadRequest     reader.ReadRequest
	PackageHash     string
	LauncherVersion string
}

type PublicPackage struct {
	ProductID   string `json:"productId"`
	ReleaseID   string `json:"releaseId"`
	Version     string `json:"version"`
	PackageHash string `json:"packageHash"`
}

type Manifest struct {
	ProductID              string             `json:"productId"`
	ReleaseID              string             `json:"releaseId"`
	Version                string             `json:"version"`
	MinimumLauncherVersion string             `json:"minimumLauncherVersion"`
	MinimumRuntimeVersion  string             `json:"minimumRuntimeVersion"`
	EntryNodeID            string             `json:"entryNodeId"`
	Workflow               WorkflowDescriptor `json:"workflow"`
	InputSchema            EntryDescriptor    `json:"inputSchema"`
	RequiredCapabilities   []string           `json:"requiredCapabilities"`
}

type EntryDescriptor struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type WorkflowDescriptor struct {
	Path       string             `json:"path"`
	SHA256     string             `json:"sha256"`
	Encryption WorkflowEncryption `json:"encryption"`
}

type WorkflowEncryption struct {
	Algorithm              string `json:"algorithm"`
	Nonce                  string `json:"nonce"`
	AuthenticationTagBytes int    `json:"authenticationTagBytes"`
}

// VerifiedPackage owns an immutable captured package, not execution permission.
// Transport handlers must build explicit public DTOs instead of serializing it.
type VerifiedPackage struct {
	Package     PublicPackage    `json:"-"`
	Manifest    Manifest         `json:"-"`
	InputSchema json.RawMessage  `json:"-"`
	Nonce       []byte           `json:"-"`
	Snapshot    *reader.Snapshot `json:"-"`
}

type Verifier struct {
	runtimeVersion string
	capabilities   map[string]struct{}
}

func New(definitions *nodecontract.Registry, handlers *registry.Registry, runtimeVersion string) (*Verifier, error) {
	if definitions == nil || handlers == nil {
		return nil, failure("create_package_verifier", "dependencies_missing", xerror.ErrProductPackageVerifierConfiguration)
	}
	capabilities := make(map[string]struct{})
	for _, node := range handlers.SupportedNodes() {
		definition, known := definitions.CompilationDefinition(node.Type)
		_, enabled := handlers.Lookup(node.Type, node.RuntimeHandler)
		if !known || !enabled || definition.RuntimeHandler != node.RuntimeHandler || definition.Capabilities == nil {
			return nil, failure("create_package_verifier", "dependencies_inconsistent", xerror.ErrProductPackageVerifierConfiguration)
		}
		for _, capability := range definition.Capabilities {
			if !ir.ValidCode(capability) {
				return nil, failure("create_package_verifier", "dependencies_inconsistent", xerror.ErrProductPackageVerifierConfiguration)
			}
			capabilities[capability] = struct{}{}
		}
	}
	return &Verifier{runtimeVersion: runtimeVersion, capabilities: capabilities}, nil
}

// Verify reads one snapshot and verifies those exact bytes, without retaining it.
func (v *Verifier) Verify(ctx context.Context, request Request) (*VerifiedPackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("decode_package_selection", err)
	}
	if !validHash(request.PackageHash) || !ir.ValidVersion(request.LauncherVersion) {
		return nil, failure("decode_package_selection", "selection_invalid", xerror.ErrProductPackageSelection)
	}
	snapshot, err := reader.Read(ctx, request.ReadRequest)
	if err != nil {
		return nil, err
	}
	return v.verifySnapshot(ctx, request, snapshot)
}

func (v *Verifier) verifySnapshot(ctx context.Context, request Request, snapshot *reader.Snapshot) (*VerifiedPackage, error) {
	hash, err := hashBytes(ctx, snapshot.ZIPBytes)
	if err != nil {
		return nil, xerror.WithOperation("verify_package_hash", err)
	}
	if hash != request.PackageHash {
		return nil, failure("verify_package_hash", "package_hash_mismatch", xerror.ErrProductPackageInvalid)
	}
	manifest, nonce, err := decodeManifest(ctx, snapshot.ManifestBytes)
	if err != nil {
		return nil, err
	}
	if manifest.ProductID != request.ReadRequest.ProductID || manifest.ReleaseID != request.ReadRequest.ReleaseID {
		return nil, failure("verify_package_identity", "identity_mismatch", xerror.ErrProductPackageInvalid)
	}
	for _, entry := range []struct {
		data             []byte
		expected, reason string
	}{
		{snapshot.WorkflowBytes, manifest.Workflow.SHA256, "workflow_hash_mismatch"},
		{snapshot.InputSchemaBytes, manifest.InputSchema.SHA256, "schema_hash_mismatch"},
	} {
		digest, err := hashBytes(ctx, entry.data)
		if err != nil {
			return nil, xerror.WithOperation("verify_package_entries", err)
		}
		if digest != entry.expected {
			return nil, failure("verify_package_entries", entry.reason, xerror.ErrProductPackageInvalid)
		}
	}
	if len(snapshot.WorkflowBytes) < constants.ProductPackageGCMTagBytes {
		return nil, failure("verify_package_entries", "workflow_tag_missing", xerror.ErrProductPackageInvalid)
	}
	if err := validateSchema(ctx, snapshot.InputSchemaBytes); err != nil {
		return nil, err
	}
	if err := v.compatibility(ctx, manifest, request.LauncherVersion); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_package_compatibility", err)
	}
	return &VerifiedPackage{
		Package:  PublicPackage{ProductID: manifest.ProductID, ReleaseID: manifest.ReleaseID, Version: manifest.Version, PackageHash: hash},
		Manifest: manifest, InputSchema: snapshot.InputSchemaBytes, Nonce: nonce, Snapshot: snapshot,
	}, nil
}

func decodeManifest(ctx context.Context, raw []byte) (Manifest, []byte, error) {
	invalid := func() (Manifest, []byte, error) {
		return Manifest{}, nil, failure("decode_package_manifest", "manifest_invalid", xerror.ErrProductPackageInvalid)
	}
	valid, err := validJSONObject(ctx, raw)
	if err != nil {
		return Manifest{}, nil, xerror.WithOperation("decode_package_manifest", err)
	}
	if !valid {
		return invalid()
	}
	object, ok := exactObject(raw, "productId", "releaseId", "version", "minimumLauncherVersion",
		"minimumRuntimeVersion", "entryNodeId", "workflow", "inputSchema", "requiredCapabilities")
	if !ok {
		return invalid()
	}
	workflow, ok := exactObject(object["workflow"], "path", "sha256", "encryption")
	if !ok {
		return invalid()
	}
	if _, ok := exactObject(workflow["encryption"], "algorithm", "nonce", "authenticationTagBytes"); !ok {
		return invalid()
	}
	if _, ok := exactObject(object["inputSchema"], "path", "sha256"); !ok {
		return invalid()
	}
	var manifest Manifest
	if json.Unmarshal(raw, &manifest) != nil || !canonicalID(manifest.ProductID) || !canonicalID(manifest.ReleaseID) ||
		!ir.ValidVersion(manifest.Version) || !ir.ValidVersion(manifest.MinimumRuntimeVersion) ||
		!ir.ValidVersion(manifest.MinimumLauncherVersion) || !ir.ValidIdentifier(manifest.EntryNodeID) ||
		manifest.Workflow.Path != constants.ProductPackageWorkflowEntry || manifest.InputSchema.Path != constants.ProductPackageSchemaEntry ||
		!validHash(manifest.Workflow.SHA256) || !validHash(manifest.InputSchema.SHA256) ||
		manifest.Workflow.Encryption.Algorithm != constants.ProductPackageEncryptionAlgorithm ||
		manifest.Workflow.Encryption.AuthenticationTagBytes != constants.ProductPackageGCMTagBytes || manifest.RequiredCapabilities == nil {
		return invalid()
	}
	seen := make(map[string]struct{}, len(manifest.RequiredCapabilities))
	for _, capability := range manifest.RequiredCapabilities {
		if err := ctx.Err(); err != nil {
			return Manifest{}, nil, xerror.WithOperation("decode_package_manifest", err)
		}
		if _, duplicate := seen[capability]; duplicate || !ir.ValidCode(capability) {
			return invalid()
		}
		seen[capability] = struct{}{}
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(manifest.Workflow.Encryption.Nonce)
	if err != nil || len(nonce) != constants.ProductPackageNonceBytes || base64.StdEncoding.EncodeToString(nonce) != manifest.Workflow.Encryption.Nonce {
		return invalid()
	}
	return manifest, nonce, nil
}

func validateSchema(ctx context.Context, raw []byte) error {
	valid, err := validJSONObject(ctx, raw)
	if err != nil {
		return xerror.WithOperation("validate_package_schema", err)
	}
	if !valid {
		return failure("validate_package_schema", "schema_invalid", xerror.ErrProductPackageInvalid)
	}
	invalid := false
	schema, err := nodecontract.DecodeWorkflowInputs(ctx, raw, validation.Path{"inputSchema"}, func(validation.Diagnostic) { invalid = true })
	if err != nil {
		return xerror.WithOperation("validate_package_schema", err)
	}
	if schema == nil || invalid {
		return failure("validate_package_schema", "schema_invalid", xerror.ErrProductPackageInvalid)
	}
	return nil
}

func (v *Verifier) compatibility(ctx context.Context, manifest Manifest, launcherVersion string) error {
	for _, check := range []struct{ actual, minimum, reason string }{
		{v.runtimeVersion, manifest.MinimumRuntimeVersion, "runtime_version_unsupported"},
		{launcherVersion, manifest.MinimumLauncherVersion, "launcher_version_unsupported"},
	} {
		comparison, valid := ir.CompareVersions(check.actual, check.minimum)
		if !valid || comparison < 0 {
			return failure("check_package_compatibility", check.reason, xerror.ErrProductPackageIncompatible)
		}
	}
	for _, capability := range manifest.RequiredCapabilities {
		if err := ctx.Err(); err != nil {
			return xerror.WithOperation("check_package_compatibility", err)
		}
		if _, available := v.capabilities[capability]; !available {
			return failure("check_package_compatibility", "capability_unavailable", xerror.ErrProductPackageIncompatible)
		}
	}
	return nil
}

func hashBytes(ctx context.Context, raw []byte) (string, error) {
	hash := sha256.New()
	for len(raw) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n := min(len(raw), constants.ProductPackageReadChunkBytes)
		_, _ = hash.Write(raw[:n])
		raw = raw[n:]
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validHash(value string) bool {
	if len(value) != 2*sha256.Size || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == value
}

func exactObject(raw []byte, names ...string) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil || len(object) != len(names) {
		return nil, false
	}
	for _, name := range names {
		if _, exists := object[name]; !exists {
			return nil, false
		}
	}
	return object, true
}

// The iterative key check mirrors the existing runs decoder, without importing
// HTTP transport. json.Valid first guarantees balanced delimiters and one value.
func validJSONObject(ctx context.Context, raw []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !utf8.Valid(raw) || !json.Valid(raw) || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return false, nil
	}
	type frame struct {
		keys      map[string]bool
		expectKey bool
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var stack []frame
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			return len(stack) == 0, nil
		}
		if err != nil {
			return false, nil
		}
		if token == json.Delim('}') || token == json.Delim(']') {
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 {
			parent := &stack[len(stack)-1]
			if parent.keys != nil {
				if parent.expectKey {
					key, ok := token.(string)
					if !ok || parent.keys[key] {
						return false, nil
					}
					parent.keys[key], parent.expectKey = true, false
					continue
				}
				parent.expectKey = true
			}
		}
		switch token {
		case json.Delim('{'):
			stack = append(stack, frame{keys: make(map[string]bool), expectKey: true})
		case json.Delim('['):
			stack = append(stack, frame{})
		}
	}
}

func failure(operation, reason string, err error) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: err}
}
