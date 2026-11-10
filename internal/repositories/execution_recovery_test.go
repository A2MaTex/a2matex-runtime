package repositories

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func recoveryStore(t *testing.T) *ExecutionRepository {
	t.Helper()
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store.DB() != nil {
			_ = store.Shutdown(context.Background())
		}
	})
	return NewExecutionRepository(store)
}

func recoveryFixture(t *testing.T) *ExecutionRepository {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	return recoveryStore(t)
}

func restartRecoveryStore(t *testing.T, repo *ExecutionRepository) *ExecutionRepository {
	t.Helper()
	if err := repo.store.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	return recoveryStore(t)
}

func recoverStartup(t *testing.T, repo *ExecutionRepository) error {
	t.Helper()
	service := services.NewExecutionRecovery(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer service.Shutdown(t.Context())
	return service.Start(t.Context())
}

func TestRecoveryReopensInterruptedTapExactlyOnce(t *testing.T) {
	repo := recoveryFixture(t)
	admission := testAdmission(0)
	admission.Devices = admission.Devices[:1]
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	checkpoint := services.NewCheckpointService(repo)
	start := beginFixture(admission, 0, 50)
	requireBegin(t, checkpoint, start, 1, "4")
	finish := finishFixture(start)
	finish.NextNodeID = "tap_01"
	requireFinish(t, checkpoint, finish, 1, "6")
	tap := start
	tap.NodeID, tap.AttemptID, tap.ExpectedDeviceStatus = "tap_01", testExecutionID(51), constants.ExecutionRunning
	tap.Now = finish.Now.Add(time.Second)
	requireBegin(t, checkpoint, tap, 2, "7")
	before, err := repo.Attempts(t.Context(), tap.DeviceExecutionID, 0, 0)
	if err != nil || len(before.Attempts) != 2 {
		t.Fatalf("initial attempts: %+v, %v", before, err)
	}
	if err := checkpoint.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	repo = restartRecoveryStore(t, repo)
	if err := recoverStartup(t, repo); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
	if err != nil || snapshot.Cursor != "10" || snapshot.RunGroup.Status != constants.ExecutionInterrupted || snapshot.RunGroup.FinishedAt == nil ||
		snapshot.RunGroup.Summary.Interrupted != 1 || snapshot.DeviceExecutions[0].NextNodeID != nil || snapshot.DeviceExecutions[0].StartedAt == nil {
		t.Fatalf("recovered snapshot: %+v, %v", snapshot, err)
	}
	finished, err := repo.Attempts(t.Context(), tap.DeviceExecutionID, 0, 0)
	if err != nil || len(finished.Attempts) != 2 || !reflect.DeepEqual(finished.Attempts[0], before.Attempts[0]) {
		t.Fatalf("committed history changed: %+v, %v", finished, err)
	}
	interrupted := finished.Attempts[1]
	if interrupted.ID != tap.AttemptID || interrupted.Status != constants.ExecutionInterrupted || interrupted.Output != nil ||
		interrupted.FinishedAt == nil || *interrupted.FinishedAt != *snapshot.RunGroup.FinishedAt ||
		!strings.Contains(string(interrupted.Error), `"code":"EXECUTION_INTERRUPTED"`) ||
		string(interrupted.Error) != string(snapshot.DeviceExecutions[0].TerminalError) {
		t.Fatalf("interrupted attempt: %+v", interrupted)
	}
	page, err := repo.ReplayEvents(t.Context(), 7, 0)
	if err != nil || len(page.Events) != 3 || page.Events[0].NodeAttemptID != tap.AttemptID ||
		page.Events[1].DeviceExecutionID != tap.DeviceExecutionID || page.Events[2].Type != constants.EventRunGroupStateChanged {
		t.Fatalf("recovery events: %+v, %v", page, err)
	}
	for _, event := range page.Events {
		if event.OccurredAt != *snapshot.RunGroup.FinishedAt {
			t.Fatalf("recovery used different timestamps: %+v", page.Events)
		}
	}
	repo = restartRecoveryStore(t, repo)
	if err := recoverStartup(t, repo); err != nil {
		t.Fatal(err)
	}
	state, err := repo.EventWatermark(t.Context())
	if err != nil || state.Watermark != 10 {
		t.Fatalf("repeat restart changed history: %+v, %v", state, err)
	}
	if replay, err := repo.Admit(t.Context(), admission); err != nil || replay.Disposition != constants.AdmissionExisting || replay.RunGroupID != admission.RunGroupID {
		t.Fatalf("original request lost its identity: %+v, %v", replay, err)
	}
	if fresh, err := repo.Admit(t.Context(), testAdmission(10)); err != nil || fresh.Disposition != constants.AdmissionCreated {
		t.Fatalf("new request could not start: %+v, %v", fresh, err)
	}
}

func TestRecoveryCleanAndQueuedStore(t *testing.T) {
	repo := recoveryFixture(t)
	if err := recoverStartup(t, repo); err != nil {
		t.Fatal(err)
	}
	state, err := repo.EventWatermark(t.Context())
	if err != nil || state.Watermark != 0 {
		t.Fatalf("clean recovery wrote events: %+v, %v", state, err)
	}
	admission := testAdmission(0)
	admission.Devices = admission.Devices[:1]
	// Exercise a host clock that moved backward between admission and restart.
	admission.Now = time.Now().UTC().Add(time.Hour)
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	repo = restartRecoveryStore(t, repo)
	if err := recoverStartup(t, repo); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
	if err != nil || snapshot.Cursor != "4" || snapshot.DeviceExecutions[0].Status != constants.ExecutionInterrupted ||
		snapshot.DeviceExecutions[0].StartedAt != nil || snapshot.RunGroup.FinishedAt == nil || *snapshot.RunGroup.FinishedAt != snapshot.RunGroup.CreatedAt {
		t.Fatalf("queued recovery: %+v, %v", snapshot, err)
	}
	history, err := repo.Attempts(t.Context(), admission.Devices[0].DeviceExecutionID, 0, 0)
	if err != nil || len(history.Attempts) != 0 {
		t.Fatalf("queued recovery fabricated an attempt: %+v, %v", history, err)
	}
}

func TestRecoveryPreservesSuccessfulSiblingOfPausedDevice(t *testing.T) {
	repo := recoveryFixture(t)
	admission := testAdmission(0)
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	checkpoint := services.NewCheckpointService(repo)
	first := beginFixture(admission, 0, 50)
	requireBegin(t, checkpoint, first, 1, "5")
	requireFinish(t, checkpoint, finishFixture(first), 1, "7")
	err := repo.WithWriteTx(t.Context(), func(tx Tx) error {
		if err := tx.UpdateExpected(t.Context(), "UPDATE device_executions SET status = 'PAUSED' WHERE id = ?", admission.Devices[1].DeviceExecutionID); err != nil {
			return err
		}
		return tx.UpdateExpected(t.Context(), "UPDATE run_groups SET status = 'PAUSED' WHERE id = ?", admission.RunGroupID)
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.ActiveSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	repo = restartRecoveryStore(t, repo)
	if err := recoverStartup(t, repo); err != nil {
		t.Fatal(err)
	}
	after, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
	if err != nil || after.Cursor != "9" || after.RunGroup.Status != constants.ExecutionInterrupted ||
		!reflect.DeepEqual(before.DeviceExecutions[0], after.DeviceExecutions[0]) || after.DeviceExecutions[1].Status != constants.ExecutionInterrupted {
		t.Fatalf("paused recovery changed the successful device: %+v, %v", after, err)
	}
	page, err := repo.ReplayEvents(t.Context(), 7, 0)
	if err != nil || len(page.Events) != 2 || page.Events[0].Type != constants.EventDeviceStateChanged ||
		page.Events[1].Type != constants.EventRunGroupStateChanged {
		t.Fatalf("paused recovery events: %+v, %v", page, err)
	}
}

func TestRecoveryRejectsRunningAttemptUnderTerminalGroup(t *testing.T) {
	repo := recoveryFixture(t)
	admission := testAdmission(0)
	admission.Devices = admission.Devices[:1]
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	checkpoint := services.NewCheckpointService(repo)
	begin := beginFixture(admission, 0, 50)
	requireBegin(t, checkpoint, begin, 1, "4")
	if _, err := repo.store.DB().ExecContext(t.Context(), `UPDATE run_groups SET status = 'FAILED', finished_at = ? WHERE id = ?`,
		begin.Now.UnixMilli(), admission.RunGroupID); err != nil {
		t.Fatal(err)
	}
	repo = restartRecoveryStore(t, repo)
	if err := recoverStartup(t, repo); !errors.Is(err, xerror.ErrRecoveryIntegrity) {
		t.Fatalf("inconsistent store recovered: %v", err)
	}
	state, err := repo.EventWatermark(t.Context())
	if err != nil || state.Watermark != 4 {
		t.Fatalf("integrity failure changed watermark: %+v, %v", state, err)
	}
}

func TestRecoveryOrdersAttemptAndDeviceEventsByStoredID(t *testing.T) {
	repo := recoveryFixture(t)
	admission := testAdmission(0)
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	checkpoint := services.NewCheckpointService(repo)
	first, second := beginFixture(admission, 0, 51), beginFixture(admission, 1, 50)
	requireBegin(t, checkpoint, first, 1, "5")
	requireBegin(t, checkpoint, second, 1, "7")
	repo = restartRecoveryStore(t, repo)
	if err := recoverStartup(t, repo); err != nil {
		t.Fatal(err)
	}
	page, err := repo.ReplayEvents(t.Context(), 7, 0)
	if err != nil || len(page.Events) != 5 || page.Events[0].NodeAttemptID != second.AttemptID ||
		page.Events[1].NodeAttemptID != first.AttemptID || page.Events[2].DeviceExecutionID != second.DeviceExecutionID ||
		page.Events[3].DeviceExecutionID != first.DeviceExecutionID || page.Events[4].Type != constants.EventRunGroupStateChanged {
		t.Fatalf("multi-device recovery order: %+v, %v", page, err)
	}
}
