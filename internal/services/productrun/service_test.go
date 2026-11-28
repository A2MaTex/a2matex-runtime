package productrun_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
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
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productrun"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

const productID = "11111111-1111-4111-8111-111111111111"

type selection struct{ offline bool }

func (s *selection) Discover(context.Context) ([]adb.Endpoint, error) {
	return []adb.Endpoint{{ADBSerial: "device-a", State: adb.Connected}, {ADBSerial: "device-b", State: adb.Connected}}, nil
}
func (s *selection) ProbeReadiness(_ context.Context, e adb.Endpoint) (adb.Readiness, error) {
	if s.offline {
		return adb.Readiness{ADBSerial: e.ADBSerial, Reason: &adb.UnavailableReason{Code: constants.DeviceOffline, Message: "device is offline"}}, nil
	}
	return adb.Readiness{ADBSerial: e.ADBSerial, Ready: true}, nil
}

type fixture struct {
	admission              *runadmission.Service
	loader                 *productworkflow.Loader
	verifier               *executionlease.Verifier
	checkpoints            *services.CheckpointService
	afterBegin             chan struct{}
	began                  chan struct{}
	expectFailure          bool
	service                *productrun.Service
	repo                   *repositories.ExecutionRepository
	store                  *sqlitestore.Store
	owner                  *executionlifecycle.Service
	sessions               *productsession.Service
	source                 *selection
	signer                 ed25519.PrivateKey
	contentKey             []byte
	packageRoot, ref, hash string
	calls                  atomic.Int32
	entered                chan struct{}
	block                  chan struct{}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func setup(t *testing.T, hold bool, blocked bool) *fixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	f := &fixture{store: sqlitestore.New(), source: &selection{}, packageRoot: t.TempDir(), ref: productID + "/package.a2m", entered: make(chan struct{}, 8)}
	if blocked {
		f.block = make(chan struct{})
	}
	must(t, f.store.Start(t.Context()))
	f.repo = repositories.NewExecutionRepository(f.store)
	cp := services.NewCheckpointService(f.repo, nil)
	f.checkpoints = cp
	f.owner = executionlifecycle.New(cp)
	must(t, f.owner.Start(context.Background()))
	catalog, err := nodecontract.NewDefaultRegistry()
	must(t, err)
	handlers, err := registry.New(catalog, []registry.Registration{{Type: "flow.start", RuntimeHandler: "flow.start", Run: start.Invoke}, {Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke}, {Type: "flow.delay", RuntimeHandler: "flow.delay", Run: func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		f.calls.Add(1)
		f.entered <- struct{}{}
		if f.block != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.block:
			}
		}
		return delay.Invoke(ctx, call)
	}}})
	must(t, err)
	validator, err := runtimeir.New(catalog, handlers, "0.2.0")
	must(t, err)
	loader, err := productworkflow.New(validator)
	must(t, err)
	packages, err := packagevalidation.New(catalog, handlers, "0.2.0")
	must(t, err)
	f.sessions, err = productsession.New(packages, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	must(t, err)
	must(t, f.sessions.Start(context.Background()))
	public, private, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	f.signer = private
	der, err := x509.MarshalPKIXPublicKey(public)
	must(t, err)
	trust, err := executionlease.New(executionlease.Options{VerificationKeys: map[string]string{"test": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}})
	must(t, err)
	resolver, err := bindings.New(catalog)
	must(t, err)
	invoker, err := invocation.New(handlers)
	must(t, err)
	output, err := outputs.New(catalog)
	must(t, err)
	admission, err := runadmission.New(runadmission.Dependencies{Validator: validator, Preflight: services.NewDevicePreflight(f.source), Repository: f.repo, Lifecycle: f.owner, Runner: runner.Dependencies{Bindings: resolver, Invoker: invoker, Outputs: output, Checkpoints: f}})
	must(t, err)
	f.admission, f.loader, f.verifier = admission, loader, trust
	f.service = productrun.New(f.sessions, trust, loader, admission, f.repo, f.owner)
	f.install(t, hold)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		f.owner.PrepareStop(constants.ExecutionStopIntentional, deadline)
		if err := f.owner.Shutdown(ctx); err != nil && !f.expectFailure {
			t.Errorf("owner shutdown: %v", err)
		}
		_ = f.sessions.Shutdown(ctx)
		_ = cp.Shutdown(ctx)
		must(t, f.store.Shutdown(ctx))
	})
	return f
}
func (f *fixture) install(t *testing.T, hold bool) {
	raw, err := os.ReadFile("../productworkflow/testdata/loading-v1.json")
	must(t, err)
	var v struct {
		ContentKeyBase64 string
		Cases            []struct {
			PackageZipBase64         string
			CanonicalRuntimeIrBase64 string
		}
	}
	must(t, json.Unmarshal(raw, &v))
	f.contentKey, err = base64.StdEncoding.DecodeString(v.ContentKeyBase64)
	must(t, err)
	zipBytes, err := base64.StdEncoding.DecodeString(v.Cases[0].PackageZipBase64)
	must(t, err)
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	must(t, err)
	entries := map[string][]byte{}
	for _, e := range zr.File {
		r, err := e.Open()
		must(t, err)
		entries[e.Name], err = io.ReadAll(r)
		must(t, err)
		must(t, r.Close())
	}
	if hold {
		plaintext, err := base64.StdEncoding.DecodeString(v.Cases[0].CanonicalRuntimeIrBase64)
		must(t, err)
		var document map[string]any
		must(t, json.Unmarshal(plaintext, &document))
		nodes := document["nodes"].([]any)
		nodes[1].(map[string]any)["timeoutMs"] = 60000
		plaintext, err = json.Marshal(document)
		must(t, err)
		block, err := aes.NewCipher(f.contentKey)
		must(t, err)
		gcm, err := cipher.NewGCM(block)
		must(t, err)
		nonce := make([]byte, 12)
		_, err = rand.Read(nonce)
		must(t, err)
		entries["workflow.enc"] = gcm.Seal(nil, nonce, plaintext, nil)
		var manifest packagevalidation.Manifest
		must(t, json.Unmarshal(entries["manifest.json"], &manifest))
		manifest.Workflow.SHA256 = hash(entries["workflow.enc"])
		manifest.Workflow.Encryption.Nonce = base64.StdEncoding.EncodeToString(nonce)
		entries["manifest.json"], err = json.Marshal(manifest)
		must(t, err)
	}
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	for _, name := range []string{"manifest.json", "workflow.enc", "input-schema.json"} {
		w, err := zw.Create(name)
		must(t, err)
		_, err = w.Write(entries[name])
		must(t, err)
	}
	must(t, zw.Close())
	path := filepath.Join(f.packageRoot, filepath.FromSlash(f.ref))
	must(t, os.MkdirAll(filepath.Dir(path), 0700))
	must(t, os.WriteFile(path, buffer.Bytes(), 0600))
	f.hash = hash(buffer.Bytes())
}
func (f *fixture) request(t *testing.T, lifetime time.Duration, count int) productrun.Request {
	t.Helper()
	prepared, err := f.sessions.Prepare(t.Context(), productsession.Request{RequestID: uuid.NewString(), Purpose: constants.ProductSessionStart, DirectoryHeaderValues: []string{url.PathEscape(f.packageRoot)}, PackageRef: f.ref, PackageHash: f.hash, LauncherVersion: "0.2.0"})
	must(t, err)
	der, err := base64.StdEncoding.DecodeString(prepared.RuntimePublicKey)
	must(t, err)
	key, err := x509.ParsePKIXPublicKey(der)
	must(t, err)
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key.(*rsa.PublicKey), f.contentKey, nil)
	must(t, err)
	now := time.Now().Unix()
	expires := now + int64(lifetime/time.Second)
	claims := map[string]any{"leaseId": uuid.NewString(), "leaseFamilyId": uuid.NewString(), "sequence": 1, "executionId": prepared.ExecutionID, "userId": uuid.NewString(), "productId": prepared.Package.ProductID, "releaseId": prepared.Package.ReleaseID, "packageHash": prepared.Package.PackageHash, "runtimeSessionId": prepared.RuntimeSessionID, "runtimeKeyThumbprint": prepared.RuntimeKeyThumbprint, "issuedAt": now, "notBefore": now, "refreshAfter": now + int64(lifetime/time.Second)/2, "expiresAt": expires, "entitlementId": uuid.NewString()}
	encode := func(v any) string {
		b, err := json.Marshal(v)
		must(t, err)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	input := encode(map[string]string{"alg": "EdDSA", "typ": "a2matex-execution-lease+jws", "kid": "test"}) + "." + encode(claims)
	envelope, err := json.Marshal(map[string]string{"releaseId": prepared.Package.ReleaseID, "algorithm": "RSA-OAEP-SHA256", "runtimeSessionId": prepared.RuntimeSessionID, "runtimeKeyThumbprint": prepared.RuntimeKeyThumbprint, "encryptedKey": base64.StdEncoding.EncodeToString(encrypted)})
	must(t, err)
	r := productrun.Request{RequestID: uuid.NewString(), RuntimeSessionID: prepared.RuntimeSessionID, Directory: f.packageRoot, Authorization: productrun.Authorization{Lease: input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.signer, []byte(input))), KeyEnvelope: envelope, ServerTime: time.Now().UTC().Format(constants.ExecutionTimestampLayout)}}
	for _, serial := range []string{"device-a", "device-b"}[:count] {
		r.Devices = append(r.Devices, runadmission.DeviceInput{ADBSerial: serial, Inputs: json.RawMessage(`{}`)})
	}
	return r
}
func (f *fixture) wait(t *testing.T, id, status string) productrun.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := f.service.ByID(t.Context(), id)
		if err == nil && snapshot.RunGroup.Status == status {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("wait %s: %+v %v", status, snapshot, err)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestProtectedStartActualEngineReplayAndProvenance(t *testing.T) {
	f := setup(t, false, false)
	r := f.request(t, time.Minute, 2)
	result, err := f.service.Start(t.Context(), r)
	must(t, err)
	if result.Replayed {
		t.Fatal("new start replayed")
	}
	id := result.Snapshot.RunGroup.ID
	final := f.wait(t, id, constants.ExecutionSucceeded)
	if final.RunGroup.Summary.Succeeded != 2 || final.Authorization.State != "ENDED" || f.calls.Load() != 2 {
		t.Fatal(final, f.calls.Load())
	}
	raw, err := f.repo.SnapshotByID(t.Context(), id)
	must(t, err)
	if raw.Product == nil || raw.Product.RuntimeSessionID != r.RuntimeSessionID {
		t.Fatal("provenance missing")
	}
	must(t, os.Remove(filepath.Join(f.packageRoot, filepath.FromSlash(f.ref))))
	r.Authorization.Lease = "not-a-signature"
	r.RuntimeSessionID = uuid.NewString()
	r.Devices[0].Inputs = []byte(`{"ignored":true}`)
	replay, err := f.service.Start(t.Context(), r)
	must(t, err)
	if !replay.Replayed || replay.Snapshot.RunGroup.ID != id || f.calls.Load() != 2 {
		t.Fatal("redispatch")
	}
	r.Directory += "/changed"
	_, err = f.service.Start(t.Context(), r)
	if !errors.Is(err, xerror.ErrProductRunConflict) {
		t.Fatal(err)
	}
	_, err = f.repo.Attempts(t.Context(), raw.DeviceExecutions[0].ID, 0, 0)
	if !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatal("history leak", err)
	}
	public, err := json.Marshal(final)
	must(t, err)
	for _, secret := range []string{"nextNodeId", "nodeId", "runtimePublicKey", "keyEnvelope", "workflow.enc", r.Authorization.Lease} {
		if strings.Contains(string(public), secret) {
			t.Fatal("public leak", secret)
		}
	}
	page, err := f.repo.ReplayEvents(t.Context(), 0, 0)
	must(t, err)
	for _, event := range page.Events {
		if !event.Product {
			t.Fatal("unclassified event")
		}
	}
}
func TestProtectedStartFailuresConsumeOnlyOwnedIntent(t *testing.T) {
	for _, kind := range []string{"signature", "envelope", "input", "offline", "root", "expired", "budget"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, false, false)
			life := time.Minute
			if kind == "expired" {
				life = 2 * time.Second
			}
			if kind == "budget" {
				life = 3 * time.Second
			}
			r := f.request(t, life, 1)
			switch kind {
			case "signature":
				r.Authorization.Lease += "x"
			case "envelope":
				r.Authorization.KeyEnvelope = []byte(`{}`)
			case "input":
				r.Devices[0].Inputs = []byte(`{"unexpected":true}`)
			case "offline":
				f.source.offline = true
			case "root":
				r.Directory += "/other"
			case "expired":
				time.Sleep(2100 * time.Millisecond)
			}
			_, err := f.service.Start(t.Context(), r)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if f.calls.Load() != 0 {
				t.Fatal("invalid dispatched")
			}
			var count int
			must(t, f.store.DB().QueryRow("SELECT count(*) FROM run_groups").Scan(&count))
			if count != 0 {
				t.Fatal("partial admission")
			}
			_, lookup := f.sessions.Lookup(t.Context(), r.RuntimeSessionID)
			if kind == "root" {
				must(t, lookup)
			} else if !errors.Is(lookup, xerror.ErrProductSessionUnavailable) {
				t.Fatal("failed session retained", lookup)
			}
		})
	}
}
func TestProtectedPauseResumeCancelAndSafeExpiry(t *testing.T) {
	for _, mode := range []string{"pause resume", "cancel active", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			held := mode == "expiry"
			f := setup(t, held, !held)
			lifetime := time.Minute
			if held {
				lifetime = 5 * time.Second
			}
			r := f.request(t, lifetime, 2)
			result, err := f.service.Start(t.Context(), r)
			must(t, err)
			id := result.Snapshot.RunGroup.ID
			if held {
				paused := f.wait(t, id, constants.ExecutionPaused)
				if paused.Authorization.State != "RENEWAL_REQUIRED" {
					t.Fatal(paused)
				}
				deadline := time.Now().Add(7 * time.Second)
				for {
					snap, err := f.service.ByID(t.Context(), id)
					if err == nil && snap.Authorization.State == "EXPIRED" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("expiry not settled", snap, err)
					}
					time.Sleep(time.Millisecond)
				}
				_, _, err = f.service.Action(t.Context(), id, constants.RunActionResume)
				if !errors.Is(err, xerror.ErrProductAuthorizationExpired) {
					t.Fatal(err)
				}
				// EXPIRED is published only after release acknowledgement; no writer can
				// restore these fields before a later (unimplemented) reauthorization.
				group := reflect.ValueOf(f.owner).Elem().FieldByName("product").Elem()
				if !group.FieldByName("loaded").IsNil() || !group.FieldByName("worker").IsNil() || group.FieldByName("store").IsNil() || group.FieldByName("states").Len() != 2 {
					t.Fatal("expiry did not detach graph and preserve live state")
				}
				if f.calls.Load() != 0 {
					t.Fatal("budget dispatched")
				}
			} else {
				for range 2 {
					select {
					case <-f.entered:
					case <-time.After(time.Second):
						t.Fatal("workers not entered")
					}
				}
				if mode == "pause resume" {
					_, _, err = f.service.Action(t.Context(), id, constants.RunActionPause)
					must(t, err)
					close(f.block)
					f.wait(t, id, constants.ExecutionPaused)
					deadline := time.Now().Add(time.Second)
					for {
						_, _, err = f.service.Action(t.Context(), id, constants.RunActionResume)
						if err == nil {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal(err)
						}
						time.Sleep(time.Millisecond)
					}
					final := f.wait(t, id, constants.ExecutionSucceeded)
					if final.RunGroup.Summary.Succeeded != 2 || f.calls.Load() != 2 {
						t.Fatal("resume redispatch", final)
					}
					return
				}
			}
			_, _, err = f.service.Action(t.Context(), id, constants.RunActionCancel)
			must(t, err)
			f.wait(t, id, constants.ExecutionCancelled)
		})
	}
}
func TestProtectedConcurrentSameSession(t *testing.T) {
	f := setup(t, false, true)
	r := f.request(t, time.Minute, 1)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.service.Start(t.Context(), r); results <- err }()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, xerror.ErrProductSessionBusy) {
			t.Fatal(err)
		}
	}
	if wins < 1 {
		t.Fatal("no admitted winner")
	}
	var count int
	must(t, f.store.DB().QueryRow("SELECT count(*) FROM run_groups").Scan(&count))
	if count != 1 {
		t.Fatal("duplicate group")
	}
	close(f.block)
}

// Block only the acknowledgement boundary, keeping the real SQLite BeginAttempt.
func (f *fixture) BeginAttempt(ctx context.Context, r execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
	result, err := f.checkpoints.BeginAttempt(ctx, r)
	if err == nil && r.NodeID == "wait" && f.afterBegin != nil {
		close(f.began)
		select {
		case <-f.afterBegin:
		case <-ctx.Done():
		}
	}
	return result, err
}
func (f *fixture) FinishAttempt(ctx context.Context, r execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	return f.checkpoints.FinishAttempt(ctx, r)
}
func TestExpiryAfterRealBeginNeverInvokesAndFailsClosed(t *testing.T) {
	f := setup(t, false, false)
	f.afterBegin = make(chan struct{})
	f.began = make(chan struct{})
	f.expectFailure = true
	request := f.request(t, 5*time.Second, 1)
	_, err := f.service.Start(t.Context(), request)
	must(t, err)
	select {
	case <-f.began:
	case <-time.After(time.Second):
		t.Fatal("real wait begin missing")
	}
	done := make(chan error, 1)
	go func() { done <- f.owner.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("uncertain begin did not fail owner")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("hard expiry did not fence owner")
	}
	if f.calls.Load() != 0 {
		t.Fatal("handler invoked after stalled begin")
	}
	var status string
	must(t, f.store.DB().QueryRow(`SELECT status FROM node_attempts WHERE node_id='wait'`).Scan(&status))
	if status != "RUNNING" {
		t.Fatal("uncertain attempt fabricated", status)
	}
}

type faultRepository struct {
	*repositories.ExecutionRepository
	mode       string
	committed  atomic.Bool
	readFailed atomic.Bool
	cancel     context.CancelFunc
}

func (r *faultRepository) Admit(ctx context.Context, a execution.Admission) (execution.AdmissionResult, error) {
	if r.mode == "rollback" {
		return execution.AdmissionResult{}, xerror.ErrExecutionPersistence
	}
	result, err := r.ExecutionRepository.Admit(ctx, a)
	if err == nil {
		r.committed.Store(true)
		if r.cancel != nil {
			r.cancel()
		}
		if r.mode == "unknown" {
			return execution.AdmissionResult{}, xerror.ErrExecutionCommitUnknown
		}
	}
	return result, err
}
func (r *faultRepository) SnapshotByID(ctx context.Context, id string) (execution.Snapshot, error) {
	if r.mode == "response" && r.committed.Load() && r.readFailed.CompareAndSwap(false, true) {
		return execution.Snapshot{}, xerror.ErrExecutionPersistence
	}
	return r.ExecutionRepository.SnapshotByID(ctx, id)
}
func TestProtectedAdmissionFailureAndCommittedWorkOwnership(t *testing.T) {
	for _, mode := range []string{"rollback", "unknown", "response", "cancel after commit"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t, false, false)
			request := f.request(t, time.Minute, 1)
			wrapper := &faultRepository{ExecutionRepository: f.repo, mode: mode}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel after commit" {
				wrapper.cancel = cancel
			}
			if mode == "unknown" {
				f.expectFailure = true
			}
			f.service = productrun.New(f.sessions, f.verifier, f.loader, f.admission, wrapper, f.owner)
			_, err := f.service.Start(ctx, request)
			if err == nil {
				t.Fatal("expected failed/uncertain response")
			}
			var groups int
			must(t, f.store.DB().QueryRow(`SELECT count(*) FROM run_groups`).Scan(&groups))
			if mode == "rollback" {
				if groups != 0 || f.calls.Load() != 0 {
					t.Fatal("rollback dispatched")
				}
				return
			}
			if groups != 1 {
				t.Fatal("lost committed admission")
			}
			if mode == "unknown" {
				if f.calls.Load() != 0 {
					t.Fatal("unknown dispatched")
				}
				return
			}
			var id string
			must(t, f.store.DB().QueryRow(`SELECT id FROM run_groups`).Scan(&id))
			f.wait(t, id, constants.ExecutionSucceeded)
			replay, err := f.service.Start(t.Context(), request)
			must(t, err)
			if !replay.Replayed || f.calls.Load() != 1 {
				t.Fatal("committed retry redispatched", f.calls.Load())
			}
		})
	}
}
