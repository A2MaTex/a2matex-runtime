package repositories

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func cancellationFixture(t *testing.T, count int) (*ExecutionRepository, *services.CheckpointService, execution.Admission) {
	t.Helper()
	repo := recoveryFixture(t)
	admission := testAdmission(0)
	admission.Devices = admission.Devices[:count]
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	service := services.NewCheckpointService(repo, nil)
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	return repo, service, admission
}

func cancelRemaining(t *testing.T, service *services.CheckpointService) execution.CancellationResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := service.CancelUnfinished(ctx, execution.CancellationRequest{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCancellationPreservesTerminalHistoryAndRestart(t *testing.T) {
	for _, gap := range []string{"queued", "between", "retry", "active"} {
		t.Run(gap, func(t *testing.T) {
			repo, service, admission := cancellationFixture(t, 2)
			first := beginFixture(admission, 0, 50)
			requireBegin(t, service, first, 1, "5")
			requireFinish(t, service, finishFixture(first), 1, "7")
			if gap != "queued" {
				second := beginFixture(admission, 1, 51)
				requireBegin(t, service, second, 1, "9")
				if gap != "active" {
					finish := finishFixture(second)
					finish.NextNodeID = "next"
					if gap == "retry" {
						finish.Status, finish.Output, finish.Error = constants.ExecutionFailed, nil, []byte(retryableCheckpointError)
						finish.NextNodeID, finish.Retry = second.NodeID, &execution.RetryDecision{MaxAttempts: 2, DelayMS: 1000}
					}
					requireFinish(t, service, finish, 1, "11")
				}
			}
			before, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			history, _ := repo.Attempts(t.Context(), admission.Devices[1].DeviceExecutionID, 0, 0)
			result := cancelRemaining(t, service)
			if !result.Changed || result.RunGroupID != admission.RunGroupID || result.Cursor == "" {
				t.Fatalf("missing committed result: %+v", result)
			}
			after, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			if err != nil || after.RunGroup.Status != constants.ExecutionCancelled || after.RunGroup.Summary.Cancelled != 1 ||
				!reflect.DeepEqual(before.DeviceExecutions[0], after.DeviceExecutions[0]) {
				t.Fatalf("snapshot: %+v %v", after, err)
			}
			rows, err := repo.Attempts(t.Context(), admission.Devices[1].DeviceExecutionID, 0, 0)
			if err != nil || len(rows.Attempts) != len(history.Attempts) {
				t.Fatalf("fabricated attempt: %+v %v", rows, err)
			}
			if gap == "active" {
				if rows.Attempts[0].Status != constants.ExecutionCancelled || rows.Attempts[0].Error != nil || rows.Attempts[0].Output != nil {
					t.Fatalf("active cancellation: %+v", rows)
				}
			} else if !reflect.DeepEqual(rows, history) {
				t.Fatalf("rewrote completed history: %+v", rows)
			}
			if repeated := cancelRemaining(t, service); repeated.Changed {
				t.Fatalf("duplicate: %+v", repeated)
			}
			repo = restartRecoveryStore(t, repo)
			if err := recoverStartup(t, repo); err != nil {
				t.Fatal(err)
			}
			reopened, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			if err != nil || !reflect.DeepEqual(after, reopened) {
				t.Fatalf("restart changed settled run: %+v %v", reopened, err)
			}
		})
	}
}

func TestCancellationRollbackAtEachStatement(t *testing.T) {
	for _, table := range []string{"node_attempts", "device_executions", "run_groups", "execution_events"} {
		t.Run(table, func(t *testing.T) {
			repo, service, admission := cancellationFixture(t, 1)
			requireBegin(t, service, beginFixture(admission, 0, 50), 1, "4")
			before, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			attempts, _ := repo.Attempts(t.Context(), admission.Devices[0].DeviceExecutionID, 0, 0)
			action := "UPDATE"
			if table == "execution_events" {
				action = "INSERT"
			}
			if _, err := repo.store.DB().ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER reject_cancellation BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'controlled cancellation fault'); END", action, table)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if result, err := service.CancelUnfinished(ctx, execution.CancellationRequest{Now: time.Now()}); !errors.Is(err, xerror.ErrCheckpointFatal) || result.Changed {
				t.Fatalf("fault hidden: %+v %v", result, err)
			}
			after, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			rows, _ := repo.Attempts(t.Context(), admission.Devices[0].DeviceExecutionID, 0, 0)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(attempts, rows) {
				t.Fatal("partial cancellation committed")
			}
			if _, err := service.CancelUnfinished(ctx, execution.CancellationRequest{Now: time.Now()}); !errors.Is(err, xerror.ErrCheckpointFatal) {
				t.Fatalf("fatal writer bypassed: %v", err)
			}
		})
	}
}

type unknownCancellation struct {
	*ExecutionRepository
	committed bool
	writes    int
}

func (r *unknownCancellation) WithCancellationTx(ctx context.Context, fn func(execution.CancellationTx) error) error {
	r.writes++
	if !r.committed {
		return r.WithWriteTx(ctx, func(tx Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			return xerror.ErrExecutionCommitUnknown
		})
	}
	if err := r.ExecutionRepository.WithCancellationTx(ctx, fn); err != nil {
		return err
	}
	return xerror.ErrExecutionCommitUnknown
}

func TestCancellationUnknownCommitDoesNotRetryAndRecoveryUsesStoredTruth(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			repo, _, admission := cancellationFixture(t, 1)
			wrapped := &unknownCancellation{ExecutionRepository: repo, committed: committed}
			service := services.NewCheckpointService(wrapped, nil)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if result, err := service.CancelUnfinished(ctx, execution.CancellationRequest{Now: time.Now()}); !errors.Is(err, xerror.ErrExecutionCommitUnknown) || result.Changed {
				t.Fatalf("unknown outcome acknowledged: %+v %v", result, err)
			}
			if _, err := service.CancelUnfinished(ctx, execution.CancellationRequest{Now: time.Now()}); !errors.Is(err, xerror.ErrCheckpointFatal) || wrapped.writes != 1 {
				t.Fatalf("unknown commit retried: %d %v", wrapped.writes, err)
			}
			repo = restartRecoveryStore(t, repo)
			if err := recoverStartup(t, repo); err != nil {
				t.Fatal(err)
			}
			snapshot, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			want := constants.ExecutionInterrupted
			if committed {
				want = constants.ExecutionCancelled
			}
			if err != nil || snapshot.RunGroup.Status != want {
				t.Fatalf("stored truth: %+v %v", snapshot, err)
			}
			rows, _ := repo.Attempts(t.Context(), admission.Devices[0].DeviceExecutionID, 0, 0)
			if len(rows.Attempts) != 0 {
				t.Fatal("recovery replayed queued work")
			}
		})
	}
}

func TestCancellationMaximumBatchAndClockRollback(t *testing.T) {
	repo := executionStore(t)
	admission := testAdmission(0)
	admission.Now = time.Now().Add(time.Hour)
	admission.Devices = make([]execution.AdmissionDevice, constants.MaxAdmissionDevices)
	for i := range admission.Devices {
		admission.Devices[i] = execution.AdmissionDevice{DeviceExecutionID: testExecutionID(100 + i), ADBSerial: fmt.Sprint("emulator-", i), EntryNodeID: "start_01"}
	}
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	service := services.NewCheckpointService(repo, nil)
	for i := range admission.Devices {
		if _, err := service.BeginAttempt(t.Context(), beginFixture(admission, i, 200+i)); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := repo.EventWatermark(t.Context())
	cancelRemaining(t, service)
	page, err := repo.ReplayEvents(t.Context(), before.Watermark, constants.MaxEventBatchSize)
	if err != nil || len(page.Events) != 65 {
		t.Fatalf("batch: %d %v", len(page.Events), err)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
	if err != nil || snapshot.RunGroup.Summary.Cancelled != constants.MaxAdmissionDevices || *snapshot.RunGroup.FinishedAt < snapshot.RunGroup.CreatedAt {
		t.Fatalf("clock rollback: %+v %v", snapshot, err)
	}
}

func TestCancellationRejectsUnboundedExpiredAndCorruptState(t *testing.T) {
	repo, service, admission := cancellationFixture(t, 1)
	if _, err := service.CancelUnfinished(t.Context(), execution.CancellationRequest{Now: time.Now()}); !errors.Is(err, xerror.ErrExecutionInvalidInput) {
		t.Fatalf("unbounded: %v", err)
	}
	requireBegin(t, service, beginFixture(admission, 0, 50), 1, "4")
	if _, err := repo.store.DB().ExecContext(t.Context(), "UPDATE run_groups SET status='FAILED', finished_at=?", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if _, err := service.CancelUnfinished(ctx, execution.CancellationRequest{Now: time.Now()}); !errors.Is(err, xerror.ErrRecoveryIntegrity) {
		t.Fatalf("corruption hidden: %v", err)
	}
}

func TestCancellationExpiredCompletionStopsWriter(t *testing.T) {
	_, service, _ := cancellationFixture(t, 1)
	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := service.CancelUnfinished(expired, execution.CancellationRequest{Now: time.Now()}); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, xerror.ErrCheckpointFatal) {
		t.Fatalf("expired: %v", err)
	}
}
