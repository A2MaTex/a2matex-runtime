package productsession

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/keyenvelope"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const (
	testProduct   = "11111111-1111-4111-8111-111111111111"
	testRelease   = "22222222-2222-4222-8222-222222222222"
	testReference = testProduct + "/package.a2m"
	testHash      = "09ec3f0a6a578539e18362027fa907be68257a5590d6a8b8eb6072481ad71d01"
)

type verifierFunc func(context.Context, packagevalidation.Request) (*packagevalidation.VerifiedPackage, error)

func (f verifierFunc) Verify(ctx context.Context, r packagevalidation.Request) (*packagevalidation.VerifiedPackage, error) {
	return f(ctx, r)
}

type sourceFunc func(context.Context, string) (ReauthorizationTarget, error)

func (f sourceFunc) Resolve(ctx context.Context, id string) (ReauthorizationTarget, error) {
	return f(ctx, id)
}

type testClock struct {
	mu       sync.Mutex
	wall     time.Time
	elapsed  time.Duration
	instants map[time.Time]time.Duration
}

func newClock() *testClock {
	return &testClock{wall: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), instants: make(map[time.Time]time.Duration)}
}
func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	instant := c.wall.Add(c.elapsed)
	c.instants[instant] = c.elapsed
	return instant
}
func (c *testClock) since(instant time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.elapsed - c.instants[instant]
}
func (c *testClock) advance(d time.Duration) { c.mu.Lock(); c.elapsed += d; c.mu.Unlock() }
func (c *testClock) jump(d time.Duration)    { c.mu.Lock(); c.wall = c.wall.Add(d); c.mu.Unlock() }

// One public test-only key is parsed independently for each candidate, not shared.
var keyOnce sync.Once
var fixtureKeyDER []byte
var fixtureKeyErr error

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		key, err := generateKey()
		if err != nil {
			fixtureKeyErr = err
			return
		}
		fixtureKeyDER = x509.MarshalPKCS1PrivateKey(key)
		wipeKey(key)
	})
	if fixtureKeyErr != nil {
		t.Fatal(fixtureKeyErr)
	}
	key, err := x509.ParsePKCS1PrivateKey(fixtureKeyDER)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func packageRequest(t *testing.T) (Request, *packagevalidation.Verifier, string) {
	t.Helper()
	raw, err := os.ReadFile("../../platform/productpackage/testdata/j1-valid.a2m")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(testReference))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]registry.Registration, 0, 2)
	for _, kind := range []string{"android.tap", "android.screenshot"} {
		definition, _ := catalog.CompilationDefinition(kind)
		entries = append(entries, registry.Registration{Type: kind, RuntimeHandler: definition.RuntimeHandler, Run: func(context.Context, registry.Invocation) (map[string]any, error) {
			t.Error("session dispatched a node")
			return nil, nil
		}})
	}
	handlers, err := registry.New(catalog, entries)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := packagevalidation.New(catalog, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	return Request{RequestID: uuid.NewString(), Purpose: constants.ProductSessionStart, DirectoryHeaderValues: []string{strings.ReplaceAll(url.QueryEscape(root), "+", "%20")}, PackageRef: testReference, PackageHash: testHash, LauncherVersion: "0.1.0"}, verifier, path
}

func service(t *testing.T, v PackageVerifier, source ReauthorizationSource) (*Service, *testClock) {
	t.Helper()
	s, err := New(v, source, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	s.now, s.since = clock.now, clock.since
	// Parse on the test goroutine before the service can call the factory elsewhere.
	key := testKey(t)
	der := x509.MarshalPKCS1PrivateKey(key)
	wipeKey(key)
	s.generate = func() (*rsa.PrivateKey, error) { return x509.ParsePKCS1PrivateKey(der) }
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, clock
}

func prepare(t *testing.T, s *Service, r Request) Prepared {
	t.Helper()
	p, err := s.Prepare(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func expectFailure(t *testing.T, s *Service, r Request, want error) {
	t.Helper()
	p, err := s.Prepare(context.Background(), r)
	if !errors.Is(err, want) || p.RuntimeSessionID != "" {
		t.Fatalf("prepare returned %+v, %v; want %v", p, err, want)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("private error leaked")
	}
	var operation *xerror.OperationError
	if !errors.As(err, &operation) || operation.Operation == "" {
		t.Fatalf("missing context: %v", err)
	}
}

func TestPrepareBindingReplayAndPrivateOwnership(t *testing.T) {
	r, v, path := packageRequest(t)
	var reads, keys atomic.Int32
	s, _ := service(t, verifierFunc(func(ctx context.Context, r packagevalidation.Request) (*packagevalidation.VerifiedPackage, error) {
		reads.Add(1)
		return v.Verify(ctx, r)
	}), nil)
	generate := s.generate
	s.generate = func() (*rsa.PrivateKey, error) { keys.Add(1); return generate() }
	p := prepare(t, s, r)
	if !validID(p.ExecutionID) || !validID(p.RuntimeSessionID) || p.ExecutionID == p.RuntimeSessionID || p.PreviousLeaseFamilyID != nil || p.Package.PackageHash != testHash || p.ExpiresAt != "2026-11-26T00:05:00.000Z" {
		t.Fatalf("public response: %+v", p)
	}
	der, err := base64.StdEncoding.DecodeString(p.RuntimePublicKey)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(der)
	if public.(*rsa.PublicKey).N.BitLen() != 3072 || public.(*rsa.PublicKey).E != 65537 || p.RuntimeKeyThumbprint != hex.EncodeToString(hash[:]) {
		t.Fatal("public key encoding/size/thumbprint")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	equivalent := r
	equivalent.DirectoryHeaderValues = []string{strings.ReplaceAll(r.DirectoryHeaderValues[0], "%2F", "%2f") + "%2F.%2F"}
	replay := prepare(t, s, equivalent)
	if !reflect.DeepEqual(replay, p) || reads.Load() != 1 || keys.Load() != 1 {
		t.Fatal("replay did work or changed identity")
	}
	p.Package.Version = "changed"
	if prepare(t, s, r).Package.Version != "1.0.0" {
		t.Fatal("response alias")
	}
	binding, err := s.Lookup(context.Background(), p.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	wrong := binding
	wrong.OwnerGeneration = uuid.NewString()
	if material, err := s.Consume(context.Background(), wrong); material != nil || !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatalf("foreign binding: %v", err)
	}
	material, err := s.Consume(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	for _, value := range []any{binding, material} {
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != "{}" {
			t.Fatal("private data serialized")
		}
	}
	var borrowed *rsa.PrivateKey
	if err := material.Use(context.Background(), func(k *rsa.PrivateKey, p *packagevalidation.VerifiedPackage) error {
		borrowed = k
		if p.Package.PackageHash != testHash || len(p.Snapshot.ZIPBytes) == 0 {
			t.Fatal("wrong snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(context.Background(), binding); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
	expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
	changed := r
	changed.LauncherVersion = "0.2.0"
	expectFailure(t, s, changed, xerror.ErrProductSessionConflict)
	material.Close()
	material.Close()
	if borrowed.D != nil || borrowed.Primes != nil {
		t.Fatal("key not cleared")
	}
	if err := material.Use(context.Background(), func(*rsa.PrivateKey, *packagevalidation.VerifiedPackage) error { return nil }); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
}

func TestPreparationValidationAndConflict(t *testing.T) {
	r, v, _ := packageRequest(t)
	s, _ := service(t, v, nil)
	original := prepare(t, s, r)
	for _, change := range []func(*Request){
		func(r *Request) { r.PackageRef = testProduct + "/other.a2m" },
		func(r *Request) { r.PackageHash = strings.Repeat("a", 64) },
		func(r *Request) { r.LauncherVersion = "0.2.0" },
		func(r *Request) { r.DirectoryHeaderValues = []string{r.DirectoryHeaderValues[0] + "%2Felsewhere"} },
	} {
		copy := r
		change(&copy)
		expectFailure(t, s, copy, xerror.ErrProductSessionConflict)
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.RequestID = "invalid" }, func(r *Request) { r.Purpose = "UNKNOWN" }, func(r *Request) { r.RunGroupID = uuid.NewString() },
		func(r *Request) { r.PackageHash = "" }, func(r *Request) { r.LauncherVersion = "0.0.0-dev" },
	} {
		copy := r
		change(&copy)
		expectFailure(t, s, copy, xerror.ErrProductSessionInvalid)
	}
	duplicate := r
	duplicate.DirectoryHeaderValues = append(append([]string(nil), r.DirectoryHeaderValues...), r.DirectoryHeaderValues[0])
	expectFailure(t, s, duplicate, xerror.ErrProductPackageLocation)
	malformed := r
	malformed.PackageRef = "SECRET/../file.a2m"
	expectFailure(t, s, malformed, xerror.ErrProductPackageLocation)
	if prepare(t, s, r).RuntimeSessionID != original.RuntimeSessionID {
		t.Fatal("invalid request modified original")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.receipts) != 1 {
		t.Fatal("invalid requests allocated receipts")
	}
}

func TestExpiryRetentionCapacityAndWallClock(t *testing.T) {
	r, v, _ := packageRequest(t)
	s, clock := service(t, v, nil)
	p := prepare(t, s, r)
	clock.advance(constants.ProductSessionPendingLifetime - time.Nanosecond)
	clock.jump(-24 * time.Hour)
	if prepare(t, s, r).ExpiresAt != p.ExpiresAt {
		t.Fatal("sliding display expiry")
	}
	s.mu.Lock()
	key := s.sessions[p.RuntimeSessionID].material.key
	s.mu.Unlock()
	clock.advance(time.Nanosecond)
	if _, err := s.Lookup(context.Background(), p.RuntimeSessionID); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
	if key.D != nil {
		t.Fatal("expiry retained private key")
	}
	expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
	clock.advance(constants.ProductSessionReceiptLifetime - time.Nanosecond)
	s.sweep()
	expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
	clock.advance(time.Nanosecond)
	s.sweep()
	s.mu.Lock()
	count := len(s.receipts)
	s.mu.Unlock()
	if count != 0 {
		t.Fatal("terminal receipt not pruned")
	}
	for i := 0; i < constants.ProductSessionPendingLimit; i++ {
		r.RequestID = uuid.NewString()
		prepare(t, s, r)
	}
	extra := r
	extra.RequestID = uuid.NewString()
	expectFailure(t, s, extra, xerror.ErrProductSessionBusy)
	if prepare(t, s, r).RuntimeSessionID == "" {
		t.Fatal("capacity blocked replay")
	}
	clock.advance(constants.ProductSessionPendingLifetime)
	s.sweep()
	s.mu.Lock()
	remaining := s.pending
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("idle cleanup left pending slots")
	}
}

func TestReceiptCapacityAndConsumedRetry(t *testing.T) {
	r, v, _ := packageRequest(t)
	s, clock := service(t, v, nil)
	for i := 0; i < constants.ProductSessionReceiptLimit; i++ {
		r.RequestID = uuid.NewString()
		p := prepare(t, s, r)
		b, err := s.Lookup(context.Background(), p.RuntimeSessionID)
		if err != nil {
			t.Fatal(err)
		}
		m, err := s.Consume(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		m.Close()
	}
	extra := r
	extra.RequestID = uuid.NewString()
	expectFailure(t, s, extra, xerror.ErrProductSessionBusy)
	expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
	clock.advance(constants.ProductSessionReceiptLifetime)
	s.sweep()
	prepare(t, s, extra)
}

func TestFailureAndCancellationDoNotPublish(t *testing.T) {
	r, v, _ := packageRequest(t)
	for _, tc := range []struct {
		name  string
		setup func(*Service)
		want  error
	}{
		{"generator error", func(s *Service) {
			s.generate = func() (*rsa.PrivateKey, error) { return nil, errors.New("SECRET entropy") }
		}, xerror.ErrProductSessionKeyGeneration},
		{"nil key", func(s *Service) { s.generate = func() (*rsa.PrivateKey, error) { return nil, nil } }, xerror.ErrProductSessionKeyGeneration},
		{"wrong size", func(s *Service) {
			s.generate = func() (*rsa.PrivateKey, error) {
				return &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: big.NewInt(1), E: 65537}}, nil
			}
		}, xerror.ErrProductSessionKeyGeneration},
		{"UUID error", func(s *Service) { s.newID = func() (string, error) { return "", errors.New("SECRET random") } }, xerror.ErrProductSessionConfiguration},
		{"UUID collision", func(s *Service) { s.newID = func() (string, error) { return s.generation, nil } }, xerror.ErrProductSessionConfiguration},
		{"nil verification", func(s *Service) {
			s.verifier = verifierFunc(func(context.Context, packagevalidation.Request) (*packagevalidation.VerifiedPackage, error) {
				return nil, nil
			})
		}, xerror.ErrProductSessionConfiguration},
		{"inconsistent snapshot", func(s *Service) {
			s.verifier = verifierFunc(func(ctx context.Context, request packagevalidation.Request) (*packagevalidation.VerifiedPackage, error) {
				result, err := v.Verify(ctx, request)
				if err != nil {
					return nil, err
				}
				result.Snapshot.Location.Directory += "-changed"
				return result, nil
			})
		}, xerror.ErrProductSessionConfiguration},
		{"verification error", func(s *Service) {
			s.verifier = verifierFunc(func(context.Context, packagevalidation.Request) (*packagevalidation.VerifiedPackage, error) {
				return nil, errors.New("SECRET dependency")
			})
		}, xerror.ErrProductSessionConfiguration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := service(t, v, nil)
			tc.setup(s)
			expectFailure(t, s, r, tc.want)
			expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.pending != 0 || s.preparing != 0 || s.inFlight != 0 || len(s.sessions) != 0 {
				t.Fatal("failed attempt retained resources")
			}
		})
	}
	s, _ := service(t, v, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := s.Prepare(ctx, r); !errors.Is(err, context.Canceled) || p.RuntimeSessionID != "" {
		t.Fatal(err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := s.Prepare(ctx, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestActualPackageFailuresArePreserved(t *testing.T) {
	r, v, path := packageRequest(t)
	s, _ := service(t, v, nil)
	oldLauncher := r
	oldLauncher.LauncherVersion = "0.0.1"
	expectFailure(t, s, oldLauncher, xerror.ErrProductPackageIncompatible)
	r.RequestID = uuid.NewString()
	wrong := r
	wrong.PackageHash = strings.Repeat("0", 64)
	expectFailure(t, s, wrong, xerror.ErrProductPackageInvalid)
	if err := os.WriteFile(path, []byte("SECRET bad ZIP"), 0600); err != nil {
		t.Fatal(err)
	}
	r.RequestID = uuid.NewString()
	expectFailure(t, s, r, xerror.ErrProductPackageInvalid)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r.RequestID = uuid.NewString()
	expectFailure(t, s, r, xerror.ErrProductPackageNotFound)
}

func TestRealKeyFreshness(t *testing.T) {
	r, v, _ := packageRequest(t)
	s, _ := service(t, v, nil)
	s.generate = generateKey
	first := prepare(t, s, r)
	r.RequestID = uuid.NewString()
	second := prepare(t, s, r)
	if first.RuntimePublicKey == second.RuntimePublicKey || first.RuntimeSessionID == second.RuntimeSessionID || first.ExecutionID == second.ExecutionID {
		t.Fatal("session identity/key reused")
	}
}

func TestServiceLifecycle(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if _, err := New(nil, nil, logger); !errors.Is(err, xerror.ErrProductSessionConfiguration) {
		t.Fatal(err)
	}
	r, v, _ := packageRequest(t)
	if _, err := New(v, nil, nil); !errors.Is(err, xerror.ErrProductSessionConfiguration) {
		t.Fatal(err)
	}
	s, err := New(v, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, s, r, xerror.ErrProductSessionUnavailable)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, xerror.ErrProductSessionConfiguration) {
		t.Fatal(err)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	active, _ := service(t, v, nil)
	if err := active.Start(context.Background()); !errors.Is(err, xerror.ErrProductSessionConfiguration) {
		t.Fatal(err)
	}
	p := prepare(t, active, r)
	b, err := active.Lookup(context.Background(), p.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := active.Consume(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := active.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Use(context.Background(), func(*rsa.PrivateKey, *packagevalidation.VerifiedPackage) error {
		t.Error("used after owner stopped")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	replacement, _ := service(t, v, nil)
	if _, err := replacement.Lookup(context.Background(), p.RuntimeSessionID); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal(err)
	}
}

func TestSessionLogsContainNoPrivateData(t *testing.T) {
	r, v, _ := packageRequest(t)
	var logs bytes.Buffer
	s, err := New(v, nil, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	key := testKey(t)
	der := x509.MarshalPKCS1PrivateKey(key)
	wipeKey(key)
	s.generate = func() (*rsa.PrivateKey, error) { return x509.ParsePKCS1PrivateKey(der) }
	clock := newClock()
	s.now, s.since = clock.now, clock.since
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := prepare(t, s, r)
	clock.advance(constants.ProductSessionPendingLifetime)
	s.sweep()
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{r.PackageRef, r.DirectoryHeaderValues[0], p.RuntimePublicKey, p.RuntimeKeyThumbprint, "workflow.enc"} {
		if strings.Contains(logs.String(), private) {
			t.Fatal("private data in log")
		}
	}
}

func TestTypedNilDependenciesAndOwnerCancellationDuringUse(t *testing.T) {
	r, v, _ := packageRequest(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var nilVerifier *packagevalidation.Verifier
	if _, err := New(nilVerifier, nil, logger); !errors.Is(err, xerror.ErrProductSessionConfiguration) {
		t.Fatal(err)
	}
	var nilFunction verifierFunc
	if _, err := New(nilFunction, nil, logger); !errors.Is(err, xerror.ErrProductSessionConfiguration) {
		t.Fatal(err)
	}
	var nilSource sourceFunc
	s, _ := service(t, v, nilSource)
	reauth, _ := reauthorizationRequest(r)
	expectFailure(t, s, reauth, xerror.ErrExecutionControlState)
	p := prepare(t, s, r)
	binding, err := s.Lookup(context.Background(), p.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := s.Consume(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	err = material.Use(context.Background(), func(*rsa.PrivateKey, *packagevalidation.VerifiedPackage) error { s.cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("owner cancellation reported success: %v", err)
	}
}

// A real ticker drives expiry without a request or direct sweep call.
type expiryWriter struct {
	done chan struct{}
	once sync.Once
}

func (w *expiryWriter) Write(value []byte) (int, error) {
	if bytes.Contains(value, []byte(`"event":"product_sessions_expired"`)) {
		w.once.Do(func() { close(w.done) })
	}
	return len(value), nil
}

func TestIdleReaperReleasesMaterial(t *testing.T) {
	r, v, _ := packageRequest(t)
	writer := &expiryWriter{done: make(chan struct{})}
	logger := slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := New(v, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	s.now, s.since = clock.now, clock.since
	key := testKey(t)
	der := x509.MarshalPKCS1PrivateKey(key)
	wipeKey(key)
	s.generate = func() (*rsa.PrivateKey, error) { return x509.ParsePKCS1PrivateKey(der) }
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	p := prepare(t, s, r)
	s.mu.Lock()
	private := s.sessions[p.RuntimeSessionID].material.key
	s.mu.Unlock()
	clock.advance(constants.ProductSessionPendingLifetime)
	receive(t, writer.done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != 0 || len(s.sessions) != 0 || private.D != nil {
		t.Fatal("idle cleanup retained material")
	}
}

func TestLeaseVerificationDoesNotConsumeSession(t *testing.T) {
	r, packages, _ := packageRequest(t)
	s, _ := service(t, packages, nil)
	prepared := prepare(t, s, r)
	binding, err := s.Lookup(context.Background(), prepared.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	verifier, request := sessionLease(t, binding)
	compact := request.Compact
	var grant *executionlease.Verified
	for _, token := range []string{compact + "invalid", compact} {
		request.Compact = token
		result, err := verifier.Verify(context.Background(), request)
		if token == compact && (err != nil || result.Grant == nil) || token != compact && !errors.Is(err, xerror.ErrExecutionLeaseInvalid) {
			t.Fatalf("lease decision: %v", err)
		}
		if got, err := s.Lookup(context.Background(), binding.RuntimeSessionID); err != nil || got != binding {
			t.Fatalf("verification consumed or changed J5: %v", err)
		}
		if err == nil {
			grant = result.Grant
		}
	}
	material, err := s.Consume(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	publicDER, err := base64.StdEncoding.DecodeString(prepared.RuntimePublicKey)
	if err != nil {
		t.Fatal(err)
	}
	runtimePublic, err := x509.ParsePKIXPublicKey(publicDER)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte{0x4a}, constants.ProductContentKeyBytes)
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, runtimePublic.(*rsa.PublicKey), content, nil)
	if err != nil {
		t.Fatal(err)
	}
	verified := grant.Claims()
	envelope, err := json.Marshal(map[string]string{
		"releaseId": verified.ReleaseID, "algorithm": constants.ProductKeyEnvelopeAlgorithm,
		"runtimeSessionId": verified.RuntimeSessionID, "runtimeKeyThumbprint": verified.RuntimeKeyThumbprint,
		"encryptedKey": base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		t.Fatal(err)
	}
	var alias []byte
	var borrowedKey *rsa.PrivateKey
	if err := material.Use(context.Background(), func(key *rsa.PrivateKey, snapshot *packagevalidation.VerifiedPackage) error {
		borrowedKey = key
		if snapshot.Package != binding.Package {
			t.Fatal("captured package changed")
		}
		return keyenvelope.WithContentKey(context.Background(), keyenvelope.Request{
			Envelope: envelope, PrivateKey: key, Expected: keyenvelope.Expected{
				ReleaseID: verified.ReleaseID, RuntimeSessionID: verified.RuntimeSessionID, RuntimeKeyThumbprint: verified.RuntimeKeyThumbprint,
			},
		}, func(_ context.Context, key []byte) error {
			alias = key
			if !bytes.Equal(key, content) {
				t.Fatal("J5 material opening key mismatch")
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(alias, make([]byte, constants.ProductContentKeyBytes)) || borrowedKey.D == nil {
		t.Fatal("J7 changed key ownership or retained content key")
	}
	material.Close()
	if borrowedKey.D != nil {
		t.Fatal("caller Close retained private material")
	}
	if _, err := s.Lookup(context.Background(), binding.RuntimeSessionID); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
		t.Fatal("J5 consumption gate changed")
	}
}

func sessionLease(t *testing.T, binding Binding) (*executionlease.Verifier, executionlease.Request) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	// A synthetic Backend decision binds real J5 metadata without exposing its
	// private RSA material or needing a customer/database/HTTP endpoint.
	verifier, err := executionlease.New(executionlease.Options{VerificationKeys: map[string]string{
		"session-test": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	claims, err := json.Marshal(map[string]any{
		"leaseId": uuid.NewString(), "leaseFamilyId": uuid.NewString(), "executionId": binding.ExecutionID,
		"sequence": 1, "entitlementId": uuid.NewString(), "userId": uuid.NewString(),
		"productId": binding.Package.ProductID, "releaseId": binding.Package.ReleaseID,
		"packageHash": binding.Package.PackageHash, "runtimeSessionId": binding.RuntimeSessionID,
		"runtimeKeyThumbprint": binding.RuntimeKeyThumbprint,
		"issuedAt":             now, "notBefore": now, "refreshAfter": now + 60, "expiresAt": now + 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","kid":"session-test","typ":"a2matex-execution-lease+jws"}`))
	input := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	compact := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
	request := executionlease.Request{Mode: constants.LeaseVerificationInitial, Expected: executionlease.Expected{
		ExecutionID: binding.ExecutionID, ProductID: binding.Package.ProductID, ReleaseID: binding.Package.ReleaseID,
		PackageHash: binding.Package.PackageHash, RuntimeSessionID: binding.RuntimeSessionID, RuntimeKeyThumbprint: binding.RuntimeKeyThumbprint,
	}}
	request.Compact = compact
	return verifier, request
}

func TestProtectedWorkflowLoadingFromSession(t *testing.T) {
	raw, err := os.ReadFile("../productworkflow/testdata/loading-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ContentKeyBase64 string
		Cases            []struct {
			PackageZipBase64, PackageHash, RuntimeIrHash string
			Inputs                                       []json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	sample := fixture.Cases[0]
	zipBytes, err := base64.StdEncoding.DecodeString(sample.PackageZipBase64)
	if err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(fixture.ContentKeyBase64)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var entries []registry.Registration
	for _, kind := range []string{"flow.start", "flow.delay", "flow.end"} {
		definition, _ := definitions.CompilationDefinition(kind)
		entries = append(entries, registry.Registration{Type: kind, RuntimeHandler: definition.RuntimeHandler, Run: func(context.Context, registry.Invocation) (map[string]any, error) {
			t.Error("loading dispatched a node")
			return nil, nil
		}})
	}
	handlers, err := registry.New(definitions, entries)
	if err != nil {
		t.Fatal(err)
	}
	packages, err := packagevalidation.New(definitions, handlers, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := runtimeir.New(definitions, handlers, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	loader, err := productworkflow.New(validator)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "request_cancel", "owner_loss"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(testReference))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, zipBytes, 0600); err != nil {
				t.Fatal(err)
			}
			s, _ := service(t, packages, nil)
			prepared := prepare(t, s, Request{RequestID: uuid.NewString(), Purpose: constants.ProductSessionStart, DirectoryHeaderValues: []string{url.PathEscape(root)}, PackageRef: testReference, PackageHash: sample.PackageHash, LauncherVersion: "0.2.0"})
			binding, err := s.Lookup(context.Background(), prepared.RuntimeSessionID)
			if err != nil {
				t.Fatal(err)
			}
			verifier, leaseRequest := sessionLease(t, binding)
			decision, err := verifier.Verify(context.Background(), leaseRequest)
			if err != nil {
				t.Fatal(err)
			}
			claims := decision.Grant.Claims()
			publicDER, err := base64.StdEncoding.DecodeString(prepared.RuntimePublicKey)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := x509.ParsePKIXPublicKey(publicDER)
			if err != nil {
				t.Fatal(err)
			}
			ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, parsed.(*rsa.PublicKey), content, nil)
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := json.Marshal(map[string]string{"releaseId": claims.ReleaseID, "algorithm": constants.ProductKeyEnvelopeAlgorithm, "runtimeSessionId": claims.RuntimeSessionID, "runtimeKeyThumbprint": claims.RuntimeKeyThumbprint, "encryptedKey": base64.StdEncoding.EncodeToString(ciphertext)})
			if err != nil {
				t.Fatal(err)
			}
			work, cancel := context.WithCancel(s.owner)
			defer cancel()
			var observed *productworkflow.Loaded
			var keyAlias []byte
			var borrowedPrivate *rsa.PrivateKey
			loaded, err := func() (*productworkflow.Loaded, error) {
				material, err := s.Consume(work, binding)
				if err != nil {
					return nil, err
				}
				defer material.Close()
				var candidate *productworkflow.Loaded
				transferred := false
				defer func() {
					if !transferred {
						candidate.Close()
					}
				}()
				err = material.Use(work, func(private *rsa.PrivateKey, snapshot *packagevalidation.VerifiedPackage) error {
					borrowedPrivate = private
					return keyenvelope.WithContentKey(work, keyenvelope.Request{Envelope: envelope, PrivateKey: private, Expected: keyenvelope.Expected{ReleaseID: claims.ReleaseID, RuntimeSessionID: claims.RuntimeSessionID, RuntimeKeyThumbprint: claims.RuntimeKeyThumbprint}}, func(ctx context.Context, key []byte) error {
						keyAlias = key
						var loadErr error
						candidate, loadErr = loader.Load(ctx, productworkflow.Request{Package: snapshot, Expected: binding.Package, ContentKey: key, DeviceInputs: sample.Inputs})
						observed = candidate
						if mode == "request_cancel" {
							cancel()
						}
						if mode == "owner_loss" {
							s.cancel()
						}
						return loadErr
					})
				})
				if err != nil {
					return nil, err
				}
				transferred = true
				return candidate, nil
			}()
			if borrowedPrivate == nil || borrowedPrivate.D != nil || !bytes.Equal(keyAlias, make([]byte, constants.ProductContentKeyBytes)) {
				t.Fatal("session/key borrow cleanup failed")
			}
			if mode == "success" {
				if err != nil || loaded == nil {
					t.Fatalf("protected loading failed: %v", err)
				}
				defer loaded.Close()
				if loaded.RuntimeIRHash != sample.RuntimeIrHash || len(loaded.TakeInputs()) != 1 {
					t.Fatal("loaded workflow or input transfer")
				}
			} else if !errors.Is(err, context.Canceled) || loaded != nil || observed == nil || observed.Document != nil || observed.TakeInputs() != nil {
				t.Fatal("outer failure retained a candidate")
			}
		})
	}
}
