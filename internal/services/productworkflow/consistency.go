package productworkflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
)

func validExpected(expected packagevalidation.PublicPackage) bool {
	for _, value := range []string{expected.ProductID, expected.ReleaseID} {
		id, err := uuid.Parse(value)
		if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != value {
			return false
		}
	}
	if !ir.ValidVersion(expected.Version) || len(expected.PackageHash) != sha256.Size*2 {
		return false
	}
	for i := range len(expected.PackageHash) {
		c := expected.PackageHash[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func matchesCapture(p *packagevalidation.VerifiedPackage, expected packagevalidation.PublicPackage) bool {
	return p.Package == expected && p.Manifest.ProductID == expected.ProductID &&
		p.Manifest.ReleaseID == expected.ReleaseID && p.Manifest.Version == expected.Version &&
		bytes.Equal(p.InputSchema, p.Snapshot.InputSchemaBytes)
}

func checkConsistency(p *packagevalidation.VerifiedPackage, document *ir.Document) error {
	if document.EntryNodeID != p.Manifest.EntryNodeID || !slices.Equal(document.RequiredCapabilities, p.Manifest.RequiredCapabilities) {
		return invalid("match_product_workflow", "requirements_mismatch")
	}
	comparison, valid := ir.CompareVersions(p.Manifest.MinimumRuntimeVersion, document.MinimumRuntimeVersion)
	if !valid || comparison < 0 {
		return invalid("match_product_workflow", "requirements_mismatch")
	}
	var public, embedded any
	if json.Unmarshal(p.InputSchema, &public) != nil || json.Unmarshal(document.InputSchema, &embedded) != nil || !reflect.DeepEqual(public, embedded) {
		return invalid("match_product_workflow", "schema_mismatch")
	}
	return nil
}
