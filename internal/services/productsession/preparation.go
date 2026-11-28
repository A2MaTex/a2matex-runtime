package productsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	reader "github.com/A2MaTex/a2matex-runtime/internal/platform/productpackage"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Request struct {
	RequestID                                string
	Purpose                                  constants.ProductSessionPurpose
	RunGroupID                               string
	DirectoryHeaderValues                    []string
	PackageRef, PackageHash, LauncherVersion string
}

type Prepared struct {
	Replayed              bool                            `json:"-"`
	RequestID             string                          `json:"requestId"`
	Purpose               constants.ProductSessionPurpose `json:"purpose"`
	ExecutionID           string                          `json:"executionId"`
	RuntimeSessionID      string                          `json:"runtimeSessionId"`
	RuntimePublicKey      string                          `json:"runtimePublicKey"`
	RuntimeKeyThumbprint  string                          `json:"runtimeKeyThumbprint"`
	PreviousLeaseFamilyID *string                         `json:"previousLeaseFamilyId"`
	Package               packagevalidation.PublicPackage `json:"package"`
	ExpiresAt             string                          `json:"expiresAt"`
}

func (s *Service) Prepare(ctx context.Context, request Request) (Prepared, error) {
	work, stop, err := s.workContext(ctx)
	if err != nil {
		return Prepared{}, xerror.WithOperation("prepare_product_session", err)
	}
	defer stop()
	request.DirectoryHeaderValues = append([]string(nil), request.DirectoryHeaderValues...)
	read, location, fingerprint, err := selection(request)
	if err != nil {
		return Prepared{}, err
	}
	key := receiptKey{purpose: request.Purpose, requestID: request.RequestID}
	s.mu.Lock()
	s.expireLocked()
	if err = s.gateLocked(); err == nil {
		err = work.Err()
	}
	if err != nil {
		s.mu.Unlock()
		return Prepared{}, xerror.WithOperation("prepare_product_session", err)
	}
	if previous := s.receipts[key]; previous != nil {
		if previous.fingerprint != fingerprint {
			s.mu.Unlock()
			return Prepared{}, failure("prepare_product_session", "request_changed", xerror.ErrProductSessionConflict)
		}
		state, id := previous.state, previous.sessionID
		if state != constants.ProductSessionPending {
			s.mu.Unlock()
			cause := xerror.ErrProductSessionUnavailable
			if state == constants.ProductSessionPreparing {
				cause = xerror.ErrProductSessionBusy
			}
			return Prepared{}, failure("prepare_product_session", "request_not_pending", cause)
		}
		s.mu.Unlock()
		if _, err = s.pendingReceipt(work, id); err != nil {
			return Prepared{}, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err = s.pendingLocked(work, id, previous); err != nil {
			return Prepared{}, err
		}
		prepared := copyPrepared(previous.prepared)
		prepared.Replayed = true
		return prepared, nil
	}
	if s.pending >= constants.ProductSessionPendingLimit || len(s.receipts) >= constants.ProductSessionReceiptLimit || s.preparing >= constants.ProductSessionPreparationLimit {
		s.mu.Unlock()
		return Prepared{}, failure("prepare_product_session", "resource_busy", xerror.ErrProductSessionBusy)
	}
	r := &receipt{fingerprint: fingerprint, state: constants.ProductSessionPreparing, created: s.now()}
	s.receipts[key] = r
	s.pending++
	s.preparing++
	s.inFlight++
	s.mu.Unlock()
	var candidate *Material
	defer func() {
		if candidate != nil {
			candidate.Close()
		}
		s.mu.Lock()
		if r.state == constants.ProductSessionPreparing {
			s.retireLocked(r, constants.ProductSessionUnavailable)
		}
		s.preparing--
		s.inFlight--
		s.finishLocked()
		s.mu.Unlock()
	}()
	target, err := s.target(work, request.Purpose, request.RunGroupID)
	if err != nil {
		return Prepared{}, err
	}
	verified, err := s.verifier.Verify(work, packagevalidation.Request{ReadRequest: read, PackageHash: request.PackageHash, LauncherVersion: request.LauncherVersion})
	if err != nil {
		return Prepared{}, verificationError(err)
	}
	if err := s.contextError(work); err != nil {
		return Prepared{}, xerror.WithOperation("verify_session_package", err)
	}
	if verified == nil || verified.Snapshot == nil || verified.Snapshot.Location != location ||
		verified.Package.ProductID != read.ProductID || !validID(verified.Package.ReleaseID) ||
		verified.Package.PackageHash != request.PackageHash || !ir.ValidVersion(verified.Package.Version) {
		return Prepared{}, failure("verify_session_package", "result_inconsistent", xerror.ErrProductSessionConfiguration)
	}
	if request.Purpose == constants.ProductSessionReauthorize && verified.Package != target.Package {
		return Prepared{}, failure("verify_session_package", "reauthorization_package_changed", xerror.ErrProductPackageInvalid)
	}
	executionID := target.ExecutionID
	if request.Purpose == constants.ProductSessionStart {
		executionID, err = s.allocateID()
	}
	if err != nil {
		return Prepared{}, err
	}
	sessionID, err := s.allocateID()
	if err != nil {
		return Prepared{}, err
	}
	if executionID == sessionID {
		return Prepared{}, failure("prepare_product_session", "identity_collision", xerror.ErrProductSessionConfiguration)
	}
	if err := s.contextError(work); err != nil {
		return Prepared{}, xerror.WithOperation("generate_product_session_key", err)
	}
	privateKey, err := s.generate()
	candidate = &Material{owner: s.owner, key: privateKey, snapshot: verified}
	if err := s.contextError(work); err != nil {
		return Prepared{}, xerror.WithOperation("generate_product_session_key", err)
	}
	if err != nil {
		return Prepared{}, failure("generate_product_session_key", "generator_failed", xerror.ErrProductSessionKeyGeneration)
	}
	public, thumbprint, err := publicKey(privateKey)
	if err != nil {
		return Prepared{}, err
	}
	if request.Purpose == constants.ProductSessionReauthorize {
		current, err := s.target(work, request.Purpose, request.RunGroupID)
		if err != nil {
			return Prepared{}, err
		}
		if current != target {
			return Prepared{}, failure("prepare_product_session", "target_changed", xerror.ErrExecutionControlState)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if err = s.gateLocked(); err == nil {
		err = work.Err()
	}
	if err != nil {
		return Prepared{}, xerror.WithOperation("prepare_product_session", err)
	}
	if s.receipts[key] != r || r.state != constants.ProductSessionPreparing {
		return Prepared{}, failure("prepare_product_session", "preparation_expired", xerror.ErrProductSessionUnavailable)
	}
	if s.identityUsedLocked(sessionID) || (request.Purpose == constants.ProductSessionStart && s.identityUsedLocked(executionID)) {
		return Prepared{}, failure("prepare_product_session", "identity_collision", xerror.ErrProductSessionConfiguration)
	}
	binding := Binding{OwnerGeneration: s.generation, Purpose: request.Purpose, RequestID: request.RequestID,
		ExecutionID: executionID, RuntimeSessionID: sessionID, Location: location, Package: verified.Package,
		RuntimeKeyThumbprint: thumbprint, PreviousLeaseFamilyID: target.PreviousLeaseFamilyID,
		RuntimeIRHash: target.RuntimeIRHash, TargetRevision: target.Revision}
	prepared := Prepared{RequestID: request.RequestID, Purpose: request.Purpose, ExecutionID: executionID,
		RuntimeSessionID: sessionID, RuntimePublicKey: public, RuntimeKeyThumbprint: thumbprint, Package: verified.Package,
		ExpiresAt: r.created.Add(constants.ProductSessionPendingLifetime).UTC().Format(constants.ExecutionTimestampLayout)}
	if target.PreviousLeaseFamilyID != "" {
		family := target.PreviousLeaseFamilyID
		prepared.PreviousLeaseFamilyID = &family
	}
	r.binding, r.prepared, r.material = binding, prepared, candidate
	r.executionID, r.sessionID, r.state = executionID, sessionID, constants.ProductSessionPending
	s.sessions[sessionID] = r
	candidate = nil
	return copyPrepared(prepared), nil
}

func selection(r Request) (reader.ReadRequest, reader.Location, [sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if !validID(r.RequestID) || !validHash(r.PackageHash) || !ir.ValidVersion(r.LauncherVersion) ||
		(r.Purpose != constants.ProductSessionStart && r.Purpose != constants.ProductSessionReauthorize) ||
		(r.Purpose == constants.ProductSessionStart && r.RunGroupID != "") || (r.Purpose == constants.ProductSessionReauthorize && !validID(r.RunGroupID)) {
		return reader.ReadRequest{}, reader.Location{}, empty, failure("prepare_product_session", "selection_invalid", xerror.ErrProductSessionInvalid)
	}
	parts := strings.SplitN(r.PackageRef, "/", 2)
	if len(parts) != 2 {
		return reader.ReadRequest{}, reader.Location{}, empty, failure("decode_package_location", "reference_invalid", xerror.ErrProductPackageLocation)
	}
	read := reader.ReadRequest{DirectoryHeaderValues: r.DirectoryHeaderValues, PackageRef: r.PackageRef, ProductID: parts[0]}
	location, err := reader.ResolveLocation(read)
	if err != nil {
		return read, location, empty, xerror.WithOperation("decode_package_location", err)
	}
	// A fixed-field struct gives deterministic ordering without a canonicalizer.
	encoded, err := json.Marshal(struct {
		Purpose                                            constants.ProductSessionPurpose
		Root, Reference, Hash, LauncherVersion, RunGroupID string
	}{r.Purpose, location.Directory, location.PackageRef, r.PackageHash, r.LauncherVersion, r.RunGroupID})
	if err != nil {
		return read, location, empty, failure("prepare_product_session", "fingerprint_failed", xerror.ErrProductSessionConfiguration)
	}
	return read, location, sha256.Sum256(encoded), nil
}

func (s *Service) allocateID() (string, error) {
	id, err := s.newID()
	if err != nil || !validID(id) {
		return "", failure("prepare_product_session", "identity_generation_failed", xerror.ErrProductSessionConfiguration)
	}
	return id, nil
}

func (s *Service) identityUsedLocked(id string) bool {
	if id == s.generation {
		return true
	}
	for _, r := range s.receipts {
		if id == r.executionID || id == r.sessionID {
			return true
		}
	}
	return false
}

func copyPrepared(p Prepared) Prepared {
	if p.PreviousLeaseFamilyID != nil {
		family := *p.PreviousLeaseFamilyID
		p.PreviousLeaseFamilyID = &family
	}
	return p
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == value
}

func validHash(value string) bool {
	if len(value) != hex.EncodedLen(sha256.Size) {
		return false
	}
	for _, b := range value {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func verificationError(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, xerror.ErrProductPackageSelection, xerror.ErrProductPackageLocation,
		xerror.ErrProductPackageNotFound, xerror.ErrProductPackageInvalid, xerror.ErrProductPackageUnavailable, xerror.ErrProductPackageIncompatible, xerror.ErrProductPackageVerifierConfiguration} {
		if errors.Is(err, known) {
			return xerror.WithOperation("verify_session_package", err)
		}
	}
	return failure("verify_session_package", "dependency_failed", xerror.ErrProductSessionConfiguration)
}
