package productsession

import (
	"context"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	reader "github.com/A2MaTex/a2matex-runtime/internal/platform/productpackage"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type ReauthorizationSource interface {
	Resolve(context.Context, string) (ReauthorizationTarget, error)
}

type ReauthorizationTarget struct {
	ExecutionID, PreviousLeaseFamilyID string
	Package                            packagevalidation.PublicPackage
	RuntimeIRHash                      string
	Revision                           uint64
}

// Binding is a comparable, private snapshot. It is not permission to decrypt.
type Binding struct {
	OwnerGeneration       string                          `json:"-"`
	Purpose               constants.ProductSessionPurpose `json:"-"`
	RequestID             string                          `json:"-"`
	ExecutionID           string                          `json:"-"`
	RuntimeSessionID      string                          `json:"-"`
	Location              reader.Location                 `json:"-"`
	Package               packagevalidation.PublicPackage `json:"-"`
	RuntimeKeyThumbprint  string                          `json:"-"`
	PreviousLeaseFamilyID string                          `json:"-"`
	RuntimeIRHash         string                          `json:"-"`
	TargetRevision        uint64                          `json:"-"`
}

func (s *Service) Lookup(ctx context.Context, sessionID string) (Binding, error) {
	work, stop, err := s.workContext(ctx)
	if err != nil {
		return Binding{}, xerror.WithOperation("lookup_product_session", err)
	}
	defer stop()
	r, err := s.pendingReceipt(work, sessionID)
	if err != nil {
		return Binding{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pendingLocked(work, sessionID, r); err != nil {
		return Binding{}, err
	}
	return r.binding, nil
}

func (s *Service) Consume(ctx context.Context, expected Binding) (*Material, error) {
	if !validBinding(expected) {
		return nil, failure("consume_product_session", "binding_invalid", xerror.ErrProductSessionInvalid)
	}
	work, stop, err := s.workContext(ctx)
	if err != nil {
		return nil, xerror.WithOperation("consume_product_session", err)
	}
	defer stop()
	r, err := s.pendingReceipt(work, expected.RuntimeSessionID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pendingLocked(work, expected.RuntimeSessionID, r); err != nil {
		return nil, err
	}
	if r.binding != expected {
		return nil, failure("consume_product_session", "binding_changed", xerror.ErrProductSessionUnavailable)
	}
	material := r.material
	r.material = nil // Transfer, rather than dispose, private ownership.
	s.retireLocked(r, constants.ProductSessionConsumed)
	return material, nil
}

func (s *Service) pendingReceipt(ctx context.Context, id string) (*receipt, error) {
	if !validID(id) {
		return nil, failure("lookup_product_session", "session_id_invalid", xerror.ErrProductSessionInvalid)
	}
	s.mu.Lock()
	r := s.sessions[id]
	if err := s.pendingLocked(ctx, id, r); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	binding := r.binding
	s.mu.Unlock()
	if binding.Purpose == constants.ProductSessionReauthorize {
		target, err := s.target(ctx, binding.Purpose, binding.ExecutionID)
		if err := s.contextError(ctx); err != nil {
			return nil, xerror.WithOperation("lookup_product_session", err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if currentErr := s.pendingLocked(ctx, id, r); currentErr != nil {
			return nil, currentErr
		}
		if err != nil || target != (ReauthorizationTarget{ExecutionID: binding.ExecutionID, PreviousLeaseFamilyID: binding.PreviousLeaseFamilyID, Package: binding.Package, RuntimeIRHash: binding.RuntimeIRHash, Revision: binding.TargetRevision}) {
			s.retireLocked(r, constants.ProductSessionUnavailable)
			return nil, failure("lookup_product_session", "target_changed", xerror.ErrProductSessionUnavailable)
		}
	}
	return r, nil
}

func (s *Service) pendingLocked(ctx context.Context, id string, r *receipt) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("lookup_product_session", err)
	}
	if err := s.gateLocked(); err != nil {
		return xerror.WithOperation("lookup_product_session", err)
	}
	s.expireLocked()
	if r == nil || s.sessions[id] != r || r.state != constants.ProductSessionPending {
		return failure("lookup_product_session", "session_not_pending", xerror.ErrProductSessionUnavailable)
	}
	return nil
}

func (s *Service) target(ctx context.Context, purpose constants.ProductSessionPurpose, id string) (ReauthorizationTarget, error) {
	if err := s.contextError(ctx); err != nil {
		return ReauthorizationTarget{}, xerror.WithOperation("resolve_reauthorization_target", err)
	}
	if purpose == constants.ProductSessionStart {
		return ReauthorizationTarget{}, nil
	}
	if s.reauthorization == nil {
		return ReauthorizationTarget{}, failure("resolve_reauthorization_target", "source_unavailable", xerror.ErrExecutionControlState)
	}
	target, err := s.reauthorization.Resolve(ctx, id)
	if err := s.contextError(ctx); err != nil {
		return ReauthorizationTarget{}, xerror.WithOperation("resolve_reauthorization_target", err)
	}
	if err != nil {
		for _, known := range []error{context.Canceled, context.DeadlineExceeded, xerror.ErrExecutionNotFound, xerror.ErrExecutionControlState} {
			if errors.Is(err, known) {
				return ReauthorizationTarget{}, xerror.WithOperation("resolve_reauthorization_target", known)
			}
		}
		return ReauthorizationTarget{}, failure("resolve_reauthorization_target", "source_failed", xerror.ErrProductSessionConfiguration)
	}
	if target.ExecutionID != id || !validID(target.ExecutionID) || !validID(target.PreviousLeaseFamilyID) || !validPackage(target.Package) || !validHash(target.RuntimeIRHash) || target.Revision == 0 {
		return ReauthorizationTarget{}, failure("resolve_reauthorization_target", "source_inconsistent", xerror.ErrProductSessionConfiguration)
	}
	return target, nil
}

func validPackage(p packagevalidation.PublicPackage) bool {
	return validID(p.ProductID) && validID(p.ReleaseID) && ir.ValidVersion(p.Version) && validHash(p.PackageHash)
}

func validBinding(b Binding) bool {
	if !validID(b.OwnerGeneration) || !validID(b.RequestID) || !validID(b.ExecutionID) || !validID(b.RuntimeSessionID) || !validPackage(b.Package) || !validHash(b.RuntimeKeyThumbprint) || b.Location.Directory == "" || b.Location.PackageRef == "" {
		return false
	}
	switch b.Purpose {
	case constants.ProductSessionStart:
		return b.PreviousLeaseFamilyID == "" && b.RuntimeIRHash == "" && b.TargetRevision == 0
	case constants.ProductSessionReauthorize:
		return validID(b.PreviousLeaseFamilyID) && validHash(b.RuntimeIRHash) && b.TargetRevision > 0
	default:
		return false
	}
}
