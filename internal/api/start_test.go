package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
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
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

type readyStartDevice struct{ calls atomic.Int32 }

func (s *readyStartDevice) Discover(context.Context) ([]adb.Endpoint, error) {
	s.calls.Add(1)
	return []adb.Endpoint{{ADBSerial: "z-device", State: adb.Connected}}, nil
}
func (*readyStartDevice) ProbeReadiness(context.Context, adb.Endpoint) (adb.Readiness, error) {
	return adb.Readiness{ADBSerial: "z-device", Ready: true}, nil
}

type startFixture struct {
	*snapshotFixture
	lifetime     *executionlifecycle.Service
	request      runs.StartRequest
	delayStarted chan struct{}
	resume       chan struct{}
	delayCalls   atomic.Int32
	source       *readyStartDevice
	logs         bytes.Buffer
}

func startStore(t *testing.T) *startFixture {
	t.Helper()
	f := &startFixture{snapshotFixture: snapshotStore(t, 1), delayStarted: make(chan struct{}), resume: make(chan struct{}), source: &readyStartDevice{}}
	f.lifetime = executionlifecycle.New(f.checkpoints)
	if err := f.lifetime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
		if err := f.lifetime.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	delayRun := func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		f.delayCalls.Add(1)
		once.Do(func() { close(f.delayStarted) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.resume:
		}
		return delay.Invoke(ctx, call)
	}
	handlers, err := registry.New(catalog, []registry.Registration{{Type: "flow.start", RuntimeHandler: "flow.start", Run: start.Invoke}, {Type: "flow.delay", RuntimeHandler: "flow.delay", Run: delayRun}, {Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke}})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := runtimeir.New(catalog, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := bindings.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	invoker, err := invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	output, err := outputs.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := runadmission.New(runadmission.Dependencies{Validator: validator, Preflight: services.NewDevicePreflight(f.source), Repository: f.repo, Lifecycle: f.lifetime, Runner: runner.Dependencies{Bindings: resolver, Invoker: invoker, Outputs: output, Checkpoints: f.checkpoints}})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := runadmission.NewStarter(admission, f.repo, f.lifetime)
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	f.token = session.ConnectionToken()
	f.router = newRouter(buildinfo.Info{}, session, nil, nil, runs.NewHandler(f.repo, starter), nil, slog.New(slog.NewJSONHandler(&f.logs, nil)))
	raw, err := os.ReadFile("../execution/runner/testdata/workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	f.request = runs.StartRequest{RequestID: uuid.NewString(), RuntimeIR: raw, Devices: []runs.StartDeviceInput{{ADBSerial: "z-device", Inputs: json.RawMessage(`{"duration":1}`)}}}
	return f
}

func (f *startFixture) post(t *testing.T, ctx context.Context, request runs.StartRequest, status int) execution.Snapshot {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	r := httptest.NewRecorder()
	f.router.ServeHTTP(r, req)
	if r.Code != status || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start HTTP: %d %s", r.Code, r.Body.String())
	}
	var snapshot execution.Snapshot
	if status == 200 || status == 201 {
		if err := json.Unmarshal(r.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func (f *startFixture) waitDelay(t *testing.T) {
	t.Helper()
	select {
	case <-f.delayStarted:
	case <-time.After(time.Second):
		t.Fatal("delay did not start")
	}
}
func (f *startFixture) settled(t *testing.T, id string, status string) execution.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		snapshot, err := f.repo.SnapshotByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.RunGroup.Status == status {
			return snapshot
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("did not settle as %s: %+v", status, snapshot)
		}
	}
}

func TestStartHTTPExecutesOnceAndSurvivesRequestCancellation(t *testing.T) {
	f := startStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	created := f.post(t, ctx, f.request, 201)
	cancel()
	f.waitDelay(t)
	if created.RunGroup.RequestID != f.request.RequestID {
		t.Fatal("request identity")
	}
	replay := f.request
	replay.RuntimeIR = json.RawMessage(`{}`)
	replay.Devices = []runs.StartDeviceInput{}
	if got := f.post(t, t.Context(), replay, 200); got.RunGroup.ID != created.RunGroup.ID {
		t.Fatal("replay created another group")
	}
	other := f.request
	other.RequestID = uuid.NewString()
	f.post(t, t.Context(), other, 409)
	close(f.resume)
	finished := f.settled(t, created.RunGroup.ID, constants.ExecutionSucceeded)
	if finished.RunGroup.Summary.Succeeded != 1 || finished.DeviceExecutions[0].NextNodeID != nil || f.delayCalls.Load() != 2 {
		t.Fatalf("result: %+v calls=%d", finished, f.delayCalls.Load())
	}
	before := f.counts(t)
	f.post(t, t.Context(), replay, 200)
	if after := f.counts(t); after != before || after != [4]int{1, 1, 4, 16} {
		t.Fatalf("settled counts before=%v after=%v", before, after)
	}
	if strings.Contains(f.logs.String(), f.token) || strings.Contains(f.logs.String(), "z-device") || strings.Contains(f.logs.String(), `"duration":`) {
		t.Fatal("request log disclosure")
	}
}

func TestStartHTTPRejectsMultipleDevicesAndSemanticErrorsBeforeWriting(t *testing.T) {
	f := startStore(t)
	multi := f.request
	multi.Devices = append(append([]runs.StartDeviceInput{}, multi.Devices...), runs.StartDeviceInput{ADBSerial: "other", Inputs: json.RawMessage(`{}`)})
	f.post(t, t.Context(), multi, 400)
	if f.source.calls.Load() != 0 {
		t.Fatal("multiple selection reached ADB")
	}
	invalid := f.request
	invalid.RuntimeIR = json.RawMessage(`{}`)
	f.post(t, t.Context(), invalid, 422)
	if got := f.counts(t); got != [4]int{} {
		t.Fatalf("rejection wrote data: %v", got)
	}
	// Authentication precedes even invalid JSON and cannot trigger discovery.
	r := httptest.NewRecorder()
	f.router.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader("invalid")))
	if r.Code != 401 || f.source.calls.Load() != 0 {
		t.Fatal("unauthenticated start reached admission")
	}
}

func TestStartHTTPIntentionalStopCancelsActiveAttempt(t *testing.T) {
	f := startStore(t)
	created := f.post(t, t.Context(), f.request, 201)
	f.waitDelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	f.lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
	if err := f.lifetime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.repo.SnapshotByID(t.Context(), created.RunGroup.ID)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || f.delayCalls.Load() != 1 {
		t.Fatalf("shutdown: %+v %v", snapshot, err)
	}
	var count int
	if err := f.store.DB().QueryRow("SELECT count(*) FROM node_attempts WHERE status='CANCELLED'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("cancelled attempt=%d err=%v", count, err)
	}
}

func TestConcurrentStartHTTPDispatchesOneWorker(t *testing.T) {
	f := startStore(t)
	body, err := json.Marshal(f.request)
	if err != nil {
		t.Fatal(err)
	}
	const requests = 8
	results := make(chan *httptest.ResponseRecorder, requests)
	gate := make(chan struct{})
	for range requests {
		go func() {
			<-gate
			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+f.token)
			r := httptest.NewRecorder()
			f.router.ServeHTTP(r, req)
			results <- r
		}()
	}
	close(gate)
	created, id := 0, ""
	for range requests {
		select {
		case r := <-results:
			if r.Code == 201 {
				created++
			} else if r.Code != 200 {
				t.Fatalf("concurrent start: %d %s", r.Code, r.Body.String())
			}
			var s execution.Snapshot
			if err := json.Unmarshal(r.Body.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
			if id == "" {
				id = s.RunGroup.ID
			} else if id != s.RunGroup.ID {
				t.Fatal("duplicate group identity")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent start did not complete")
		}
	}
	f.waitDelay(t)
	if created != 1 || f.delayCalls.Load() != 1 {
		t.Fatalf("creators=%d dispatches=%d", created, f.delayCalls.Load())
	}
	close(f.resume)
	f.settled(t, id, constants.ExecutionSucceeded)
}
