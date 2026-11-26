// Package productsession owns pending authorization sessions, not execution permission.
package productsession

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"

	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type PackageVerifier interface {
	Verify(context.Context, packagevalidation.Request) (*packagevalidation.VerifiedPackage, error)
}

type receiptKey struct {
	purpose   constants.ProductSessionPurpose
	requestID string
}

type receipt struct {
	fingerprint            [sha256.Size]byte
	state                  constants.ProductSessionState
	created, terminal      time.Time
	executionID, sessionID string
	binding                Binding
	prepared               Prepared
	material               *Material
}

// Service is one process generation's bounded, memory-only session store.
type Service struct {
	mu                                      sync.Mutex
	verifier                                PackageVerifier
	reauthorization                         ReauthorizationSource
	logger                                  *slog.Logger
	now                                     func() time.Time
	since                                   func(time.Time) time.Duration
	generate                                func() (*rsa.PrivateKey, error)
	newID                                   func() (string, error)
	started, closed, reaperDone, doneClosed bool
	owner                                   context.Context
	cancel                                  context.CancelFunc
	generation                              string
	receipts                                map[receiptKey]*receipt
	sessions                                map[string]*receipt
	pending, preparing, inFlight            int
	done                                    chan struct{}
}

func New(verifier PackageVerifier, reauthorization ReauthorizationSource, logger *slog.Logger) (*Service, error) {
	if nilDependency(verifier) || logger == nil {
		return nil, failure("create_product_sessions", "dependencies_missing", xerror.ErrProductSessionConfiguration)
	}
	if nilDependency(reauthorization) {
		reauthorization = nil
	}
	return &Service{
		verifier: verifier, reauthorization: reauthorization, logger: logger,
		now: time.Now, since: time.Since, generate: generateKey,
		newID:      func() (string, error) { id, err := uuid.NewRandom(); return id.String(), err },
		reaperDone: true, receipts: make(map[receiptKey]*receipt), sessions: make(map[string]*receipt), done: make(chan struct{}),
	}, nil
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return failure("start_product_sessions", "already_started", xerror.ErrProductSessionConfiguration)
	}
	s.started = true
	if err := ctx.Err(); err != nil {
		s.closeLocked()
		s.finishLocked()
		s.mu.Unlock()
		return xerror.WithOperation("start_product_sessions", err)
	}
	s.inFlight++
	s.mu.Unlock()
	generation, err := s.newID()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if err != nil || !validID(generation) {
		err = xerror.ErrProductSessionConfiguration
	}
	if s.closed && err == nil {
		err = xerror.ErrProductSessionUnavailable
	}
	if err != nil {
		s.closeLocked()
		s.finishLocked()
		return xerror.WithOperation("start_product_sessions", err)
	}
	s.generation = generation
	s.owner, s.cancel = context.WithCancel(ctx)
	s.reaperDone = false
	go s.reap()
	return nil
}

func (s *Service) Wait() error { <-s.done; return nil }

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closeLocked()
	s.finishLocked()
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return xerror.WithOperation("shutdown_product_sessions", ctx.Err())
	}
}

func (s *Service) reap() {
	ticker := time.NewTicker(constants.ProductSessionSweepInterval)
	defer ticker.Stop()
	defer func() {
		s.mu.Lock()
		s.closeLocked()
		s.reaperDone = true
		s.finishLocked()
		s.mu.Unlock()
	}()
	for {
		select {
		case <-s.owner.Done():
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

func (s *Service) sweep() {
	s.mu.Lock()
	expired := s.expireLocked()
	s.mu.Unlock()
	if expired > 0 {
		s.logger.Debug("product authorization sessions expired", "component", "product_sessions", "event", "product_sessions_expired", "count", expired)
	}
}

func (s *Service) expireLocked() int {
	expired := 0
	for key, r := range s.receipts {
		if (r.state == constants.ProductSessionPreparing || r.state == constants.ProductSessionPending) && s.since(r.created) >= constants.ProductSessionPendingLifetime {
			s.retireLocked(r, constants.ProductSessionUnavailable)
			expired++
		} else if (r.state == constants.ProductSessionConsumed || r.state == constants.ProductSessionUnavailable) && s.since(r.terminal) >= constants.ProductSessionReceiptLifetime {
			delete(s.receipts, key)
		}
	}
	return expired
}

func (s *Service) retireLocked(r *receipt, state constants.ProductSessionState) {
	if r.state != constants.ProductSessionPreparing && r.state != constants.ProductSessionPending {
		return
	}
	s.pending--
	delete(s.sessions, r.sessionID)
	if r.material != nil {
		r.material.Close()
		r.material = nil
	}
	r.state, r.terminal = state, s.now()
	r.prepared, r.binding = Prepared{}, Binding{}
}

func (s *Service) closeLocked() {
	if !s.closed {
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		for _, r := range s.receipts {
			s.retireLocked(r, constants.ProductSessionUnavailable)
		}
	}
}

func (s *Service) finishLocked() {
	if s.closed && s.reaperDone && s.inFlight == 0 && !s.doneClosed {
		close(s.done)
		s.doneClosed = true
	}
}

func (s *Service) gateLocked() error {
	if s.closed || s.owner == nil {
		return xerror.ErrProductSessionUnavailable
	}
	if err := s.owner.Err(); err != nil {
		s.closeLocked()
		return err
	}
	return nil
}

// workContext belongs to one call; published sessions instead retain s.owner.
func (s *Service) workContext(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	err := s.gateLocked()
	owner := s.owner
	s.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(owner, cancel)
	if owner.Err() != nil {
		cancel()
	}
	return work, func() { stop(); cancel() }, nil
}

func failure(operation, reason string, cause error) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: cause}
}

// Interface dependencies can contain typed nil pointers or function adapters.
func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan:
		return reflected.IsNil()
	default:
		return false
	}
}

func (s *Service) contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.owner.Err()
}
