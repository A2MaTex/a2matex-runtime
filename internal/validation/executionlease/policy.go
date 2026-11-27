package executionlease

import (
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func (c Claims) expected() Expected {
	return Expected{ExecutionID: c.ExecutionID, ProductID: c.ProductID, ReleaseID: c.ReleaseID,
		PackageHash: c.PackageHash, RuntimeSessionID: c.RuntimeSessionID, RuntimeKeyThumbprint: c.RuntimeKeyThumbprint}
}

func (v *Verifier) checkRequest(r Request) error {
	if v == nil || len(v.keys) == 0 || v.since == nil || v.anchor.IsZero() {
		return configuration("verify_execution_lease", "verifier_invalid")
	}
	for _, id := range []string{r.Expected.ExecutionID, r.Expected.ProductID, r.Expected.ReleaseID, r.Expected.RuntimeSessionID} {
		if !validID(id) {
			return configuration("match_execution_binding", "expected_invalid")
		}
	}
	if !validHash(r.Expected.PackageHash) || !validHash(r.Expected.RuntimeKeyThumbprint) {
		return configuration("match_execution_binding", "expected_invalid")
	}
	switch r.Mode {
	case constants.LeaseVerificationInitial:
		if r.Current == nil {
			return nil
		}
	case constants.LeaseVerificationRenew, constants.LeaseVerificationReauthorize:
		if r.Current != nil && r.Current.verifier == v {
			return nil
		}
	}
	return configuration("check_execution_sequence", "current_or_mode_invalid")
}

// Add monotonic elapsed time to the startup wall sample, never a new wall sample.
func (v *Verifier) now() time.Time { return v.anchor.Add(v.since(v.anchor)) }

func (v *Verifier) checkTime(c Claims, now time.Time) error {
	if c.IssuedAt > c.NotBefore || c.NotBefore >= c.RefreshAfter || c.RefreshAfter >= c.ExpiresAt ||
		c.ExpiresAt-c.IssuedAt > constants.ExecutionLeaseMaxSeconds {
		return invalid("validate_execution_claims", "time_invalid")
	}
	future := now.UTC().Add(v.skew)
	if time.Unix(c.IssuedAt, 0).After(future) || time.Unix(c.NotBefore, 0).After(future) ||
		!now.UTC().Before(time.Unix(c.ExpiresAt, 0)) {
		return invalid("validate_execution_claims", "time_invalid")
	}
	return nil
}

func checkSequence(r Request, candidate Claims, kid string, now time.Time) (bool, error) {
	bad := func() (bool, error) { return false, invalid("check_execution_sequence", "grant_conflict") }
	if r.Mode == constants.LeaseVerificationInitial {
		if candidate.Sequence != constants.ExecutionLeaseInitialSequence {
			return bad()
		}
		return false, nil
	}
	current := r.Current.claims
	expired := !now.Before(r.Current.expiresAt)
	if r.Mode == constants.LeaseVerificationRenew {
		if expired {
			return bad()
		}
		if candidate == current && kid == r.Current.kid {
			return true, nil
		}
		if candidate.RuntimeSessionID != current.RuntimeSessionID || candidate.RuntimeKeyThumbprint != current.RuntimeKeyThumbprint {
			return bad()
		}
	} else if !expired || candidate.RuntimeSessionID == current.RuntimeSessionID || candidate.RuntimeKeyThumbprint == current.RuntimeKeyThumbprint {
		return bad()
	}
	if candidate.ExecutionID != current.ExecutionID || candidate.LeaseFamilyID != current.LeaseFamilyID ||
		candidate.UserID != current.UserID || candidate.EntitlementID != current.EntitlementID ||
		candidate.ProductID != current.ProductID || candidate.ReleaseID != current.ReleaseID || candidate.PackageHash != current.PackageHash ||
		candidate.LeaseID == current.LeaseID || candidate.Sequence <= current.Sequence ||
		candidate.IssuedAt < current.IssuedAt || candidate.NotBefore < current.NotBefore ||
		candidate.RefreshAfter <= current.RefreshAfter || candidate.ExpiresAt <= current.ExpiresAt {
		return bad()
	}
	return false, nil
}
