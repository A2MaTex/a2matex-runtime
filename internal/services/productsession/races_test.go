package productsession

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not finish")
		var zero T
		return zero
	}
}

func TestPreparationPermitCancellationExpiryAndShutdown(t *testing.T) {
	for _, mode := range []string{"caller cancellation", "expiry", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			r, v, _ := packageRequest(t)
			s, clock := service(t, v, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			t.Cleanup(unblock)
			generator := s.generate
			var key *rsa.PrivateKey
			var calls atomic.Int32
			s.generate = func() (*rsa.PrivateKey, error) {
				calls.Add(1)
				close(entered)
				<-release
				var err error
				key, err = generator()
				return key, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				p, err := s.Prepare(ctx, r)
				if p.RuntimeSessionID != "" {
					err = errors.New("cancelled/expired preparation published")
				}
				result <- err
			}()
			receive(t, entered)
			expectFailure(t, s, r, xerror.ErrProductSessionBusy)
			changed := r
			changed.LauncherVersion = "0.2.0"
			expectFailure(t, s, changed, xerror.ErrProductSessionConflict)
			other := r
			other.RequestID = uuid.NewString()
			expectFailure(t, s, other, xerror.ErrProductSessionBusy)
			switch mode {
			case "caller cancellation":
				cancel()
			case "expiry":
				clock.advance(constants.ProductSessionPendingLifetime)
				s.sweep()
				expectFailure(t, s, other, xerror.ErrProductSessionBusy)
			case "shutdown":
				deadline, stop := context.WithCancel(context.Background())
				stop()
				if err := s.Shutdown(deadline); !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown claimed drain: %v", err)
				}
				expectFailure(t, s, other, xerror.ErrProductSessionUnavailable)
			}
			unblock()
			err := receive(t, result)
			if !errors.Is(err, context.Canceled) && !errors.Is(err, xerror.ErrProductSessionUnavailable) {
				t.Fatal(err)
			}
			if calls.Load() != 1 || key == nil || key.D != nil {
				t.Fatal("permit duplicated or late key retained")
			}
			s.mu.Lock()
			pending, inflight, preparing := s.pending, s.inFlight, s.preparing
			s.mu.Unlock()
			if pending != 0 || inflight != 0 || preparing != 0 {
				t.Fatal("preparation resources leaked")
			}
			if mode != "shutdown" {
				s.generate = generator
				prepare(t, s, other)
			}
		})
	}
}

func TestPreparationCancellationStagesAndOwnerLoss(t *testing.T) {
	r, v, _ := packageRequest(t)
	for _, stage := range []string{"verification", "identity"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := service(t, v, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			generator := s.generate
			var calls atomic.Int32
			s.generate = func() (*rsa.PrivateKey, error) { calls.Add(1); return generator() }
			if stage == "verification" {
				s.verifier = verifierFunc(func(ctx context.Context, r packagevalidation.Request) (*packagevalidation.VerifiedPackage, error) {
					p, err := v.Verify(ctx, r)
					cancel()
					return p, err
				})
			} else {
				s.newID = func() (string, error) { cancel(); return uuid.NewString(), nil }
			}
			if _, err := s.Prepare(ctx, r); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("cancelled stage generated a key")
			}
		})
	}
	owner, stop := context.WithCancel(context.Background())
	s, err := New(v, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	k := testKey(t)
	der := x509.MarshalPKCS1PrivateKey(k)
	wipeKey(k)
	s.generate = func() (*rsa.PrivateKey, error) { return x509.ParsePKCS1PrivateKey(der) }
	if err := s.Start(owner); err != nil {
		t.Fatal(err)
	}
	p := prepare(t, s, r)
	s.mu.Lock()
	key := s.sessions[p.RuntimeSessionID].material.key
	s.mu.Unlock()
	stop()
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if key.D != nil {
		t.Fatal("owner loss retained private material")
	}
	if _, err := s.Lookup(context.Background(), p.RuntimeSessionID); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
}

func TestConcurrentConsumeAndMaterialClose(t *testing.T) {
	r, v, _ := packageRequest(t)
	s, _ := service(t, v, nil)
	p := prepare(t, s, r)
	binding, err := s.Lookup(context.Background(), p.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan *Material, 2)
	failures := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; m, err := s.Consume(context.Background(), binding); results <- m; failures <- err }()
	}
	close(start)
	var material *Material
	wins := 0
	for range 2 {
		m := receive(t, results)
		err := receive(t, failures)
		if m != nil {
			material = m
			wins++
		}
		if err != nil && !errors.Is(err, xerror.ErrProductSessionUnavailable) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("consume winners=%d", wins)
	}
	defer material.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	useDone := make(chan error, 1)
	closeDone := make(chan struct{})
	go func() {
		useDone <- material.Use(context.Background(), func(key *rsa.PrivateKey, _ *packagevalidation.VerifiedPackage) error {
			close(entered)
			<-release
			if key.D == nil {
				return errors.New("key wiped during borrowed use")
			}
			return nil
		})
	}()
	receive(t, entered)
	go func() { material.Close(); close(closeDone) }()
	unblock()
	if err := receive(t, useDone); err != nil {
		t.Fatal(err)
	}
	receive(t, closeDone)
	if err := material.Use(context.Background(), func(*rsa.PrivateKey, *packagevalidation.VerifiedPackage) error {
		t.Error("closed material callback ran")
		return nil
	}); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
}

func reauthorizationRequest(r Request) (Request, ReauthorizationTarget) {
	r.Purpose = constants.ProductSessionReauthorize
	r.RunGroupID = uuid.NewString()
	return r, ReauthorizationTarget{ExecutionID: r.RunGroupID, PreviousLeaseFamilyID: uuid.NewString(),
		Package: packagevalidation.PublicPackage{ProductID: testProduct, ReleaseID: testRelease, Version: "1.0.0", PackageHash: testHash}, RuntimeIRHash: strings.Repeat("a", 64), Revision: 1}
}

func TestReauthorizationIdentityReplayAndTargetChanges(t *testing.T) {
	original, v, _ := packageRequest(t)
	r, target := reauthorizationRequest(original)
	var mu sync.Mutex
	current := target
	source := sourceFunc(func(context.Context, string) (ReauthorizationTarget, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	})
	s, _ := service(t, v, source)
	p := prepare(t, s, r)
	if p.ExecutionID != r.RunGroupID || p.PreviousLeaseFamilyID == nil || *p.PreviousLeaseFamilyID != target.PreviousLeaseFamilyID {
		t.Fatal("reauthorization identity")
	}
	*p.PreviousLeaseFamilyID = "changed"
	replay := prepare(t, s, r)
	if *replay.PreviousLeaseFamilyID != target.PreviousLeaseFamilyID {
		t.Fatal("nullable response alias")
	}
	// The same request UUID has an independent START scope.
	start := original
	start.RequestID = r.RequestID
	fresh := prepare(t, s, start)
	if fresh.ExecutionID == r.RunGroupID || fresh.RuntimeSessionID == p.RuntimeSessionID {
		t.Fatal("purpose scope collision")
	}
	changed := r
	changed.RunGroupID = uuid.NewString()
	expectFailure(t, s, changed, xerror.ErrProductSessionConflict)
	binding, err := s.Lookup(context.Background(), p.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	current.Revision++
	mu.Unlock()
	if m, err := s.Consume(context.Background(), binding); m != nil || !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
	expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
}

func TestReauthorizationSourceFailuresAndPublicationRace(t *testing.T) {
	original, v, _ := packageRequest(t)
	r, target := reauthorizationRequest(original)
	for _, tc := range []struct {
		name   string
		source ReauthorizationSource
		want   error
	}{
		{"disabled", nil, xerror.ErrExecutionControlState},
		{"missing", sourceFunc(func(context.Context, string) (ReauthorizationTarget, error) {
			return ReauthorizationTarget{}, xerror.ErrExecutionNotFound
		}), xerror.ErrExecutionNotFound},
		{"user paused or terminal", sourceFunc(func(context.Context, string) (ReauthorizationTarget, error) {
			return ReauthorizationTarget{}, xerror.ErrExecutionControlState
		}), xerror.ErrExecutionControlState},
		{"unexpected", sourceFunc(func(context.Context, string) (ReauthorizationTarget, error) {
			return ReauthorizationTarget{}, errors.New("SECRET source")
		}), xerror.ErrProductSessionConfiguration},
		{"malformed", sourceFunc(func(context.Context, string) (ReauthorizationTarget, error) {
			bad := target
			bad.Revision = 0
			return bad, nil
		}), xerror.ErrProductSessionConfiguration},
		{"different package", sourceFunc(func(context.Context, string) (ReauthorizationTarget, error) {
			bad := target
			bad.Package.PackageHash = strings.Repeat("b", 64)
			return bad, nil
		}), xerror.ErrProductPackageInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := service(t, v, tc.source)
			s.generate = func() (*rsa.PrivateKey, error) { t.Error("invalid target generated key"); return nil, nil }
			expectFailure(t, s, r, tc.want)
		})
	}
	for _, stage := range []string{"publication", "replay", "consume"} {
		t.Run(stage, func(t *testing.T) {
			var calls atomic.Int32
			threshold := int32(2)
			if stage != "publication" {
				threshold = 3
			}
			source := sourceFunc(func(context.Context, string) (ReauthorizationTarget, error) {
				if calls.Add(1) >= threshold {
					return ReauthorizationTarget{}, xerror.ErrExecutionControlState
				}
				return target, nil
			})
			s, _ := service(t, v, source)
			if stage == "publication" {
				expectFailure(t, s, r, xerror.ErrExecutionControlState)
				return
			}
			p := prepare(t, s, r)
			if stage == "replay" {
				expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
			} else {
				s.mu.Lock()
				binding := s.sessions[p.RuntimeSessionID].binding
				s.mu.Unlock()
				if m, err := s.Consume(context.Background(), binding); m != nil || !errors.Is(err, xerror.ErrProductSessionUnavailable) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCallerEndsAfterPublicationAndDuplicateShutdown(t *testing.T) {
	r, v, _ := packageRequest(t)
	s, _ := service(t, v, nil)
	ctx, cancel := context.WithCancel(context.Background())
	p, err := s.Prepare(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if prepare(t, s, r).RuntimeSessionID != p.RuntimeSessionID {
		t.Fatal("HTTP lifetime destroyed pending session")
	}
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- s.Shutdown(context.Background()) }()
	}
	for range 2 {
		if err := receive(t, done); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestStartupFailureAndShutdownDuringOwnerGeneration(t *testing.T) {
	_, v, _ := packageRequest(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s, err := New(v, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.newID = func() (string, error) { calls.Add(1); return uuid.NewString(), nil }
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Start(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled startup accessed randomness")
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}

	s, err = New(v, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	s.newID = func() (string, error) { return "", errors.New("SECRET entropy") }
	if err := s.Start(context.Background()); !errors.Is(err, xerror.ErrProductSessionConfiguration) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("startup error: %v", err)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}

	s, err = New(v, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	s.newID = func() (string, error) { close(entered); <-release; return uuid.NewString(), nil }
	result := make(chan error, 1)
	go func() { result <- s.Start(context.Background()) }()
	receive(t, entered)
	if err := s.Shutdown(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	unblock()
	if err := receive(t, result); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
}
