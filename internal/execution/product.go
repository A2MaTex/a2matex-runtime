package execution

import (
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"time"
)

// ProductProvenance contains safe durable identity, never execution material.
type ProductProvenance struct {
	Package          packagevalidation.PublicPackage `json:"-"`
	RuntimeIRHash    string                          `json:"-"`
	RootHash         string                          `json:"-"`
	RuntimeSessionID string                          `json:"-"`
	LeaseFamilyID    string                          `json:"-"`
	Sequence         int64                           `json:"-"`
	RefreshAfter     time.Time                       `json:"-"`
	ExpiresAt        time.Time                       `json:"-"`
}

// ValidProductProvenance validates service-owned metadata before SQL writes.
func ValidProductProvenance(p *ProductProvenance) bool {
	if p == nil {
		return false
	}
	hash := func(s string) bool {
		if len(s) != 64 {
			return false
		}
		for _, c := range s {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	return ValidID(p.Package.ProductID) && ValidID(p.Package.ReleaseID) && p.Package.Version != "" && hash(p.Package.PackageHash) && hash(p.RuntimeIRHash) && hash(p.RootHash) && ValidID(p.RuntimeSessionID) && ValidID(p.LeaseFamilyID) && p.Sequence > 0 && !p.RefreshAfter.IsZero() && p.ExpiresAt.After(p.RefreshAfter)
}
