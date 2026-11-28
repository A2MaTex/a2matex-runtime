// Package productrun composes protected admission without implementing another engine.
package productrun

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/keyenvelope"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Repository interface {
	LookupRequest(context.Context, string) (string, error)
	Admit(context.Context, execution.Admission) (execution.AdmissionResult, error)
	SnapshotByID(context.Context, string) (execution.Snapshot, error)
	ActiveSnapshot(context.Context) (execution.Snapshot, error)
}
type Authorization struct {
	Lease       string          `json:"lease"`
	KeyEnvelope json.RawMessage `json:"keyEnvelope"`
	ServerTime  string          `json:"serverTime"`
}
type Request struct {
	RequestID        string
	RuntimeSessionID string
	Directory        string
	Authorization    Authorization
	Devices          []runadmission.DeviceInput
}
type Result struct {
	Snapshot Snapshot
	Replayed bool
}
type Service struct {
	sessions       *productsession.Service
	verifier       *executionlease.Verifier
	loader         *productworkflow.Loader
	admission      *runadmission.Service
	repository     Repository
	owner          *executionlifecycle.Service
	mu             sync.Mutex
	inFlight, root string
}

func New(sessions *productsession.Service, verifier *executionlease.Verifier, loader *productworkflow.Loader, admission *runadmission.Service, repository Repository, owner *executionlifecycle.Service) *Service {
	return &Service{sessions: sessions, verifier: verifier, loader: loader, admission: admission, repository: repository, owner: owner}
}
func RootHash(directory string) string {
	h := sha256.Sum256([]byte(directory))
	return hex.EncodeToString(h[:])
}
func (s *Service) Start(ctx context.Context, r Request) (Result, error) {
	rootHash := RootHash(r.Directory)
	if id, err := s.repository.LookupRequest(ctx, r.RequestID); err == nil {
		snap, err := s.repository.SnapshotByID(ctx, id)
		if err != nil {
			return Result{}, err
		}
		if snap.Product == nil || snap.Product.RootHash != rootHash {
			return Result{}, xerror.ErrProductRunConflict
		}
		public, err := s.project(ctx, snap)
		return Result{Snapshot: public, Replayed: true}, err
	} else if !errors.Is(err, xerror.ErrExecutionNotFound) {
		return Result{}, err
	}
	if s.verifier == nil {
		return Result{}, xerror.ErrProductRunUnavailable
	}
	s.mu.Lock()
	if s.inFlight != "" {
		conflict := s.inFlight == r.RequestID && s.root != rootHash
		s.mu.Unlock()
		if conflict {
			return Result{}, xerror.ErrProductRunConflict
		}
		return Result{}, xerror.ErrProductSessionBusy
	}
	s.inFlight, s.root = r.RequestID, rootHash
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.inFlight, s.root = "", ""; s.mu.Unlock() }()
	work, release, err := s.owner.BeginStart(ctx)
	if err != nil {
		return Result{}, err
	}
	defer release()
	binding, err := s.sessions.Lookup(work, r.RuntimeSessionID)
	if err != nil {
		return Result{}, err
	}
	if binding.Purpose != constants.ProductSessionStart {
		return Result{}, xerror.ErrProductSessionUnavailable
	}
	if binding.Location.Directory != r.Directory || binding.RequestID == r.RequestID {
		return Result{}, xerror.ErrProductSessionInvalid
	}
	consumed := false
	defer func() {
		if !consumed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(work), constants.CheckpointTimeout)
			defer cancel()
			_ = s.sessions.Invalidate(cleanup, binding)
		}
	}()
	verified, err := s.verifier.Verify(work, executionlease.Request{Compact: r.Authorization.Lease, Mode: constants.LeaseVerificationInitial, Expected: executionlease.Expected{ExecutionID: binding.ExecutionID, ProductID: binding.Package.ProductID, ReleaseID: binding.Package.ReleaseID, PackageHash: binding.Package.PackageHash, RuntimeSessionID: binding.RuntimeSessionID, RuntimeKeyThumbprint: binding.RuntimeKeyThumbprint}})
	if err != nil {
		return Result{}, err
	}
	material, err := s.sessions.Consume(work, binding)
	if err != nil {
		return Result{}, err
	}
	consumed = true
	defer material.Close()
	rawInputs := make([]json.RawMessage, len(r.Devices))
	for i, d := range r.Devices {
		rawInputs[i] = d.Inputs
	}
	var loaded *productworkflow.Loaded
	err = material.Use(work, func(key *rsa.PrivateKey, capture *packagevalidation.VerifiedPackage) error {
		return keyenvelope.WithContentKey(work, keyenvelope.Request{PrivateKey: key, Envelope: r.Authorization.KeyEnvelope, Expected: keyenvelope.Expected{ReleaseID: binding.Package.ReleaseID, RuntimeSessionID: binding.RuntimeSessionID, RuntimeKeyThumbprint: binding.RuntimeKeyThumbprint}}, func(ctx context.Context, key []byte) error {
			var err error
			loaded, err = s.loader.Load(ctx, productworkflow.Request{Package: capture, Expected: binding.Package, ContentKey: key, DeviceInputs: rawInputs})
			return err
		})
	})
	material.Close()
	if err != nil {
		loaded.Close()
		return Result{}, err
	}
	transferred := false
	defer func() {
		if !transferred {
			loaded.Close()
		}
	}()
	admission, prepared, rejection, err := s.admission.PrepareChecked(work, binding.ExecutionID, r.RequestID, r.Devices, loaded)
	if err != nil {
		return Result{}, err
	}
	if rejection != nil {
		return Result{}, &DeviceRejection{Code: rejection.FailureCode}
	}
	// PrepareChecked transfers inputs out of Loaded; the candidate owns their cleanup.
	candidate, err := s.owner.PrepareProduct(binding.ExecutionID, loaded, prepared.Runner, prepared.Devices, verified.Grant)
	if err != nil {
		for _, d := range prepared.Devices {
			for _, raw := range d.WorkflowInputs {
				clear(raw)
			}
		}
		return Result{}, err
	}
	defer func() {
		if !transferred {
			candidate.Abort()
		}
	}()
	var timeout int64
	for _, n := range loaded.Document.Nodes {
		if n.ID == loaded.Document.EntryNodeID {
			timeout = n.TimeoutMS
			break
		}
	}
	if err := candidate.CheckInitial(work, timeout); err != nil {
		return Result{}, err
	}
	claims := verified.Grant.Claims()
	admission.Product = &execution.ProductProvenance{Package: binding.Package, RuntimeIRHash: loaded.RuntimeIRHash, RootHash: rootHash, RuntimeSessionID: binding.RuntimeSessionID, LeaseFamilyID: claims.LeaseFamilyID, Sequence: claims.Sequence, RefreshAfter: time.Unix(claims.RefreshAfter, 0).UTC(), ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC()}
	admitted, err := s.repository.Admit(work, admission)
	if err != nil {
		if errors.Is(err, xerror.ErrExecutionCommitUnknown) {
			s.owner.Fail(err)
		}
		return Result{}, err
	}
	switch admitted.Disposition {
	case constants.AdmissionBusy:
		return Result{}, xerror.ErrProductSessionBusy
	case constants.AdmissionExisting:
		snap, err := s.repository.SnapshotByID(ctx, admitted.RunGroupID)
		if err != nil {
			return Result{}, err
		}
		if snap.Product == nil || snap.Product.RootHash != rootHash {
			return Result{}, xerror.ErrProductRunConflict
		}
		public, err := s.project(ctx, snap)
		return Result{Snapshot: public, Replayed: true}, err
	case constants.AdmissionCreated:
		if admitted.RunGroupID != binding.ExecutionID {
			s.owner.Fail(xerror.ErrRunAdmissionContract)
			return Result{}, xerror.ErrRunAdmissionContract
		}
	default:
		s.owner.Fail(xerror.ErrRunAdmissionContract)
		return Result{}, xerror.ErrRunAdmissionContract
	}
	if err := s.owner.DispatchProduct(candidate); err != nil {
		s.owner.Fail(err)
		return Result{}, err
	}
	// The process owner now owns these references. A slow/cancelled HTTP
	// snapshot read must not retain a second graph-bearing prepared-work root.
	prepared.Runner, prepared.Devices = nil, nil
	prepared = nil
	transferred = true
	release()
	snapshot, err := s.ByID(ctx, binding.ExecutionID)
	return Result{Snapshot: snapshot}, err
}

type DeviceRejection struct{ Code string }

func (e *DeviceRejection) Error() string { return "selected Android device is unavailable" }
func (s *Service) ByID(ctx context.Context, id string) (Snapshot, error) {
	raw, err := s.repository.SnapshotByID(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	return s.project(ctx, raw)
}
func (s *Service) Active(ctx context.Context) (Snapshot, error) {
	raw, err := s.repository.ActiveSnapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return s.project(ctx, raw)
}
