package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	attemptapi "github.com/A2MaTex/a2matex-runtime/internal/api/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type snapshotSpy struct {
	calls int
	err   error
}

func (s *snapshotSpy) ActiveSnapshot(context.Context) (execution.Snapshot, error) {
	s.calls++
	return execution.Snapshot{}, s.err
}
func (s *snapshotSpy) SnapshotByID(context.Context, string) (execution.Snapshot, error) {
	s.calls++
	return execution.Snapshot{}, s.err
}
func snapshotRouter(reader runs.SnapshotReader, session *auth.Session, output io.Writer) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	return newRouter(buildinfo.Info{}, session, nil, nil, runs.NewHandler(reader, nil, nil), nil, nil, nil, slog.New(slog.NewJSONHandler(output, nil)))
}
func snapshotRequest(t *testing.T, router http.Handler, token, path string, status int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != status || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot HTTP status=%d body=%s", response.Code, response.Body.String())
	}
	return response
}
func TestSnapshotAuthenticationAndErrorLogging(t *testing.T) {
	session, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stale, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "invalid", "duplicate", "stale", "query token", "valid"} {
		t.Run(name, func(t *testing.T) {
			spy := &snapshotSpy{err: xerror.ErrExecutionNotFound}
			var logs bytes.Buffer
			router := snapshotRouter(spy, session, &logs)
			path := "/api/v1/runs/invalid"
			if name == "valid" {
				path = "/api/v1/runs/active"
			}
			if name == "query token" {
				path += "?sessionToken=" + session.ConnectionToken()
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			switch name {
			case "invalid":
				req.Header.Set("Authorization", "invalid")
			case "duplicate":
				req.Header.Add("Authorization", "Bearer "+session.ConnectionToken())
				req.Header.Add("Authorization", "Bearer "+session.ConnectionToken())
			case "stale":
				req.Header.Set("Authorization", "Bearer "+stale.ConnectionToken())
			case "valid":
				req.Header.Set("Authorization", "Bearer "+session.ConnectionToken())
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			status, calls := 401, 0
			if name == "valid" {
				status, calls = 404, 1
			}
			if response.Code != status || spy.calls != calls || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("authentication: status=%d calls=%d body=%s", response.Code, spy.calls, response.Body.String())
			}
			if strings.Contains(logs.String(), session.ConnectionToken()) || strings.Contains(logs.String(), stale.ConnectionToken()) || strings.Count(logs.String(), "\n") != 1 {
				t.Fatal("credential exposure or duplicate request log")
			}
			var entry map[string]any
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["level"] != "ERROR" {
				t.Fatalf("request level: %v", entry["level"])
			}
		})
	}
	spy := &snapshotSpy{err: &xerror.OperationError{Operation: "controlled_read_snapshot", Detail: "controlled_failure", Err: errors.Join(xerror.ErrExecutionPersistence, errors.New("private driver text"))}}
	var logs bytes.Buffer
	response := snapshotRequest(t, snapshotRouter(spy, session, &logs), session.ConnectionToken(), uuid.NewString(), 500)
	if strings.Contains(response.Body.String(), "private") || strings.Contains(logs.String(), "private") || !strings.Contains(logs.String(), `"operation":"controlled_read_snapshot"`) || !strings.Contains(logs.String(), `"route":"/api/v1/runs/:runGroupId"`) || strings.Count(logs.String(), "\n") != 1 {
		t.Fatalf("read error projection/logging: %s %s", response.Body.String(), logs.String())
	}
}

type snapshotFixture struct {
	store       *sqlitestore.Store
	repo        *repositories.ExecutionRepository
	checkpoints *services.CheckpointService
	router      *gin.Engine
	token       string
	admission   execution.Admission
}

func snapshotStore(t *testing.T, count int) *snapshotFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	f := &snapshotFixture{store: sqlitestore.New()}
	if err := f.store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.repo = repositories.NewExecutionRepository(f.store)
	f.checkpoints = services.NewCheckpointService(f.repo, nil)
	session, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	f.token = session.ConnectionToken()
	gin.SetMode(gin.ReleaseMode)
	f.router = newRouter(buildinfo.Info{}, session, nil, nil, runs.NewHandler(f.repo, nil, nil), attemptapi.NewHandler(f.repo), nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	f.admission = execution.Admission{RequestID: uuid.NewString(), RunGroupID: uuid.NewString(), Now: time.Now().UTC().Add(-time.Second), Devices: make([]execution.AdmissionDevice, count)}
	for i := range f.admission.Devices {
		f.admission.Devices[i] = execution.AdmissionDevice{DeviceExecutionID: uuid.NewString(), ADBSerial: []string{"z-device", "a-device"}[i], EntryNodeID: "start"}
	}
	t.Cleanup(func() {
		if err := f.checkpoints.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if err := f.store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return f
}
func (f *snapshotFixture) admit(t *testing.T) {
	t.Helper()
	if _, err := f.repo.Admit(t.Context(), f.admission); err != nil {
		t.Fatal(err)
	}
}
func (f *snapshotFixture) get(t *testing.T, path string) execution.Snapshot {
	t.Helper()
	response := snapshotRequest(t, f.router, f.token, path, 200)
	var result runs.SnapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *snapshotFixture) counts(t *testing.T) [4]int {
	t.Helper()
	var result [4]int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM run_groups),(SELECT count(*) FROM device_executions),(SELECT count(*) FROM node_attempts),(SELECT count(*) FROM execution_events)`).Scan(&result[0], &result[1], &result[2], &result[3]); err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *snapshotFixture) begin(t *testing.T, index int) execution.BeginAttemptRequest {
	t.Helper()
	req := execution.BeginAttemptRequest{AttemptRef: execution.AttemptRef{RunGroupID: f.admission.RunGroupID, DeviceExecutionID: f.admission.Devices[index].DeviceExecutionID, NodeID: "start", AttemptID: uuid.NewString()}, ExpectedDeviceStatus: constants.ExecutionQueued, AttemptNumber: 1, MaxAttempts: 1, Now: time.Now().UTC()}
	if result, err := f.checkpoints.BeginAttempt(t.Context(), req); err != nil || result.Disposition != constants.CheckpointCommitted {
		t.Fatalf("begin: %+v %v", result, err)
	}
	return req
}
func (f *snapshotFixture) finish(ctx context.Context, req execution.BeginAttemptRequest, status string) error {
	finish := execution.FinishAttemptRequest{AttemptRef: req.AttemptRef, Status: status, Now: time.Now().UTC()}
	if status == constants.ExecutionSucceeded {
		finish.Output = json.RawMessage(`{}`)
	} else {
		finish.Error = json.RawMessage(`{"code":"COMMAND_FAILED","message":"controlled failure","retryable":false}`)
	}
	_, err := f.checkpoints.FinishAttempt(ctx, finish)
	return err
}
func TestSnapshotSQLiteReadOnlyAndTerminalStates(t *testing.T) {
	for _, status := range []string{constants.ExecutionSucceeded, constants.ExecutionFailed, constants.ExecutionCancelled, constants.ExecutionInterrupted, constants.ExecutionPaused} {
		t.Run(status, func(t *testing.T) {
			f := snapshotStore(t, 1)
			snapshotRequest(t, f.router, f.token, "active", 404)
			snapshotRequest(t, f.router, f.token, uuid.NewString(), 404)
			if f.counts(t) != [4]int{} {
				t.Fatal("empty read created state")
			}
			f.admit(t)
			queued := f.get(t, "active")
			if queued.RunGroup.Summary.Queued != 1 || queued.DeviceExecutions[0].StartedAt != nil || queued.Cursor != "2" {
				t.Fatalf("queued: %+v", queued)
			}
			switch status {
			case constants.ExecutionSucceeded, constants.ExecutionFailed:
				begin := f.begin(t, 0)
				if err := f.finish(t.Context(), begin, status); err != nil {
					t.Fatal(err)
				}
			case constants.ExecutionCancelled:
				lifetime := executionlifecycle.New(f.checkpoints)
				if err := lifetime.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				deadline, _ := ctx.Deadline()
				lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
				if err := lifetime.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			case constants.ExecutionInterrupted:
				recovery := services.NewExecutionRecovery(f.repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if err := recovery.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := recovery.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			case constants.ExecutionPaused:
				// Controlled persistence fixture only; no Pause API is introduced.
				if err := f.repo.WithWriteTx(t.Context(), func(tx repositories.Tx) error {
					if err := tx.UpdateExpected(t.Context(), "UPDATE device_executions SET status='PAUSED' WHERE id=?", f.admission.Devices[0].DeviceExecutionID); err != nil {
						return err
					}
					return tx.UpdateExpected(t.Context(), "UPDATE run_groups SET status='PAUSED' WHERE id=?", f.admission.RunGroupID)
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := f.counts(t)
			got := f.get(t, f.admission.RunGroupID)
			expected, err := f.repo.SnapshotByID(t.Context(), f.admission.RunGroupID)
			if err != nil || !sameSnapshotJSON(t, got, expected) || got.RunGroup.Status != status {
				t.Fatalf("projection: %+v %v", got, err)
			}
			if status == constants.ExecutionPaused {
				active := f.get(t, "active")
				if active.RunGroup.Status != status {
					t.Fatal("paused run missing")
				}
			} else {
				snapshotRequest(t, f.router, f.token, "active", 404)
				// A historical ID stays readable while a newly admitted group is active.
				next := f.admission
				next.RequestID, next.RunGroupID = uuid.NewString(), uuid.NewString()
				next.Devices = []execution.AdmissionDevice{{DeviceExecutionID: uuid.NewString(), ADBSerial: "new-device", EntryNodeID: "start"}}
				if _, err := f.repo.Admit(t.Context(), next); err != nil {
					t.Fatal(err)
				}
				before = f.counts(t)
				old := f.get(t, f.admission.RunGroupID)
				active := f.get(t, "active")
				if old.RunGroup.ID != f.admission.RunGroupID || active.RunGroup.ID != next.RunGroupID {
					t.Fatal("by-ID substituted active run")
				}
			}
			if f.counts(t) != before {
				t.Fatal("GET mutated state/events/attempts")
			}
		})
	}
}
func TestSnapshotConcurrentCheckpointUsesMatchingCursor(t *testing.T) {
	f := snapshotStore(t, 2)
	f.admit(t)
	begin := f.begin(t, 0)
	done := make(chan error, 1)
	go func() { done <- f.finish(t.Context(), begin, constants.ExecutionSucceeded) }()
	for range 12 {
		snapshot := f.get(t, "active")
		summary := snapshot.RunGroup.Summary
		if snapshot.RunGroup.Status != constants.ExecutionRunning || summary.Total != 2 || summary.Queued != 1 || snapshot.DeviceExecutions[0].ADBSerial != "z-device" || snapshot.DeviceExecutions[1].ADBSerial != "a-device" {
			t.Fatalf("state/order: %+v", snapshot)
		}
		switch snapshot.Cursor {
		case "5":
			if summary.Running != 1 || summary.Succeeded != 0 || snapshot.DeviceExecutions[0].Status != constants.ExecutionRunning {
				t.Fatal("old cursor with new state")
			}
		case "7":
			if summary.Running != 0 || summary.Succeeded != 1 || snapshot.DeviceExecutions[0].Status != constants.ExecutionSucceeded {
				t.Fatal("new cursor with old state")
			}
		default:
			t.Fatalf("unexpected checkpoint cursor: %s", snapshot.Cursor)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	before := f.counts(t)
	got := f.get(t, f.admission.RunGroupID)
	if got.Cursor != "7" || f.counts(t) != before {
		t.Fatal("final snapshot changed data or lost commit")
	}
}

func sameSnapshotJSON(t *testing.T, left, right execution.Snapshot) bool {
	t.Helper()
	a, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(a, b)
}
