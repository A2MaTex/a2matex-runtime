package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func checkpointFixture(t *testing.T, deviceCount int) (*ExecutionRepository, *services.CheckpointService, execution.Admission) {
	t.Helper()
	repo := executionStore(t)
	admission := testAdmission(0)
	admission.Devices = admission.Devices[:deviceCount]
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	service := services.NewCheckpointService(repo)
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	return repo, service, admission
}

func beginFixture(admission execution.Admission, device, id int) execution.BeginAttemptRequest {
	return execution.BeginAttemptRequest{AttemptRef: execution.AttemptRef{
		RunGroupID: admission.RunGroupID, DeviceExecutionID: admission.Devices[device].DeviceExecutionID,
		NodeID: admission.Devices[device].EntryNodeID, AttemptID: testExecutionID(id),
	}, ExpectedDeviceStatus: constants.ExecutionQueued, AttemptNumber: 1, MaxAttempts: 2, Now: admission.Now.Add(time.Second)}
}

func finishFixture(begin execution.BeginAttemptRequest) execution.FinishAttemptRequest {
	return execution.FinishAttemptRequest{AttemptRef: begin.AttemptRef, Status: constants.ExecutionSucceeded,
		Output: json.RawMessage(`{}`), Now: begin.Now.Add(time.Second)}
}

func requireBegin(t *testing.T, s *services.CheckpointService, request execution.BeginAttemptRequest, sequence int64, cursor string) {
	t.Helper()
	result, err := s.BeginAttempt(t.Context(), request)
	want := execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: sequence, Cursor: cursor}
	if err != nil || result != want {
		t.Fatalf("begin: got %+v, %v; want %+v", result, err, want)
	}
}

func requireFinish(t *testing.T, s *services.CheckpointService, request execution.FinishAttemptRequest, sequence int64, cursor string) {
	t.Helper()
	result, err := s.FinishAttempt(t.Context(), request)
	want := execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: sequence, Cursor: cursor}
	if err != nil || result != want {
		t.Fatalf("finish: got %+v, %v; want %+v", result, err, want)
	}
}

func TestCheckpointLinearWorkflow(t *testing.T) {
	repo, service, admission := checkpointFixture(t, 1)
	start := beginFixture(admission, 0, 50)
	requireBegin(t, service, start, 1, "4")
	if result, err := service.BeginAttempt(t.Context(), start); err != nil || result.Disposition != constants.CheckpointStale {
		t.Fatalf("duplicate begin authorized dispatch: %+v, %v", result, err)
	}
	finishStart := finishFixture(start)
	finishStart.NextNodeID = "tap_01"
	requireFinish(t, service, finishStart, 1, "6")

	tap := start
	tap.NodeID, tap.AttemptID, tap.ExpectedDeviceStatus = "tap_01", testExecutionID(51), constants.ExecutionRunning
	tap.Now = finishStart.Now.Add(time.Second)
	requireBegin(t, service, tap, 2, "7")
	if result, err := service.FinishAttempt(t.Context(), finishStart); err != nil || result.Disposition != constants.CheckpointAlreadyFinalized || result.Cursor != "" {
		t.Fatalf("old completion advanced twice: %+v, %v", result, err)
	}
	wrong := finishFixture(tap)
	wrong.NodeID = "end_01"
	if result, err := service.FinishAttempt(t.Context(), wrong); err != nil || result.Disposition != constants.CheckpointStale {
		t.Fatalf("wrong node completed an attempt: %+v, %v", result, err)
	}
	finishTap := finishFixture(tap)
	finishTap.NextNodeID = "end_01"
	requireFinish(t, service, finishTap, 2, "9")

	end := tap
	end.NodeID, end.AttemptID, end.Now = "end_01", testExecutionID(52), finishTap.Now.Add(time.Second)
	requireBegin(t, service, end, 3, "10")
	requireFinish(t, service, finishFixture(end), 3, "13")
	snapshot, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
	if err != nil || snapshot.Cursor != "13" || snapshot.RunGroup.Status != constants.ExecutionSucceeded ||
		snapshot.DeviceExecutions[0].NextNodeID != nil || snapshot.RunGroup.Summary.Succeeded != 1 {
		t.Fatalf("final snapshot: %+v, %v", snapshot, err)
	}
	page, err := repo.ReplayEvents(t.Context(), 2, 0)
	if err != nil || len(page.Events) != 11 {
		t.Fatalf("replay: %+v, %v", page, err)
	}
	if page.Events[0].Type != constants.EventDeviceStateChanged || page.Events[1].NodeAttemptID != start.AttemptID ||
		page.Events[8].Type != constants.EventAttemptStateChanged || page.Events[9].Type != constants.EventDeviceStateChanged ||
		page.Events[10].Type != constants.EventRunGroupStateChanged {
		t.Fatalf("unexpected checkpoint event order: %+v", page.Events)
	}
}

const retryableCheckpointError = `{"code":"COMMAND_START_FAILED","message":"Command could not start","retryable":true}`
const failedCheckpointError = `{"code":"COMMAND_FAILED","message":"Command failed","retryable":false}`

func TestCheckpointRetrySurvivesEventPruning(t *testing.T) {
	repo, service, admission := checkpointFixture(t, 1)
	first := beginFixture(admission, 0, 50)
	requireBegin(t, service, first, 1, "4")
	finish := finishFixture(first)
	finish.Status, finish.Output, finish.Error = constants.ExecutionFailed, nil, json.RawMessage(retryableCheckpointError)
	finish.NextNodeID, finish.Retry = first.NodeID, &execution.RetryDecision{MaxAttempts: 2, DelayMS: 1000}
	requireFinish(t, service, finish, 1, "6")
	page, err := repo.ReplayEvents(t.Context(), 4, 0)
	if err != nil || len(page.Events) != 2 || page.Events[1].Type != constants.EventRetryScheduled ||
		page.Events[1].NodeAttemptID != first.AttemptID || string(page.Events[1].Payload) != `{"nextAttemptNumber":2,"delayMs":1000}` {
		t.Fatalf("retry events: %+v, %v", page, err)
	}
	if _, err := repo.PruneEvents(t.Context(), 0, constants.EventPruneBatchSize); err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.AttemptID, retry.PreviousAttemptID, retry.AttemptNumber = testExecutionID(51), testExecutionID(99), 2
	retry.ExpectedDeviceStatus, retry.Now = constants.ExecutionRunning, finish.Now.Add(time.Second)
	if result, err := service.BeginAttempt(t.Context(), retry); err != nil || result.Disposition != constants.CheckpointStale {
		t.Fatalf("retry with wrong predecessor: %+v, %v", result, err)
	}
	retry.PreviousAttemptID = first.AttemptID
	requireBegin(t, service, retry, 2, "7")
	finish.AttemptRef, finish.Now = retry.AttemptRef, retry.Now.Add(time.Second)
	if _, err := service.FinishAttempt(t.Context(), finish); !errors.Is(err, xerror.ErrExecutionInvalidInput) {
		t.Fatalf("retry beyond admitted maximum: %v", err)
	}
	finish.Retry, finish.NextNodeID = nil, ""
	requireFinish(t, service, finish, 2, "10")
	page, err = repo.ReplayEvents(t.Context(), 7, 0)
	if err != nil || len(page.Events) != 3 || page.Events[1].Type != constants.EventDeviceStateChanged ||
		page.Events[2].Type != constants.EventRunGroupStateChanged {
		t.Fatalf("failure events: %+v, %v", page, err)
	}
}

func TestCheckpointGroupAggregation(t *testing.T) {
	repo, service, admission := checkpointFixture(t, 2)
	first := beginFixture(admission, 0, 50)
	requireBegin(t, service, first, 1, "5")
	finish := finishFixture(first)
	finish.Status, finish.Output, finish.Error = constants.ExecutionFailed, nil, json.RawMessage(failedCheckpointError)
	requireFinish(t, service, finish, 1, "7")
	snapshot, err := repo.ActiveSnapshot(t.Context())
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionRunning || snapshot.RunGroup.Summary.Failed != 1 || snapshot.RunGroup.Summary.Queued != 1 {
		t.Fatalf("sibling execution should continue: %+v, %v", snapshot, err)
	}
	second := beginFixture(admission, 1, 51)
	second.Now = finish.Now.Add(time.Second)
	requireBegin(t, service, second, 1, "9")
	requireFinish(t, service, finishFixture(second), 1, "12")
	snapshot, err = repo.SnapshotByID(t.Context(), admission.RunGroupID)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionFailed || snapshot.RunGroup.Summary.Succeeded != 1 {
		t.Fatalf("failed aggregate: %+v, %v", snapshot, err)
	}
}

func TestCheckpointRoutedFailureAndCancellation(t *testing.T) {
	repo, service, admission := checkpointFixture(t, 1)
	begin := beginFixture(admission, 0, 50)
	requireBegin(t, service, begin, 1, "4")
	finish := finishFixture(begin)
	finish.Status, finish.Output, finish.Error = constants.ExecutionFailed, nil, json.RawMessage(failedCheckpointError)
	finish.NextNodeID = "on_error"
	requireFinish(t, service, finish, 1, "6")
	begin.NodeID, begin.AttemptID, begin.ExpectedDeviceStatus = "on_error", testExecutionID(51), constants.ExecutionRunning
	begin.Now = finish.Now.Add(time.Second)
	requireBegin(t, service, begin, 2, "7")
	finish = finishFixture(begin)
	finish.Status, finish.Output = constants.ExecutionCancelled, nil
	if _, err := service.FinishAttempt(t.Context(), finish); !errors.Is(err, xerror.ErrExecutionInvalidInput) {
		t.Fatalf("unaccepted group cancellation: %v", err)
	}
	finish.GroupCancellation = true
	requireFinish(t, service, finish, 2, "10")
	snapshot, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || snapshot.DeviceExecutions[0].TerminalError != nil {
		t.Fatalf("cancelled snapshot: %+v, %v", snapshot, err)
	}
}

func TestCheckpointContexts(t *testing.T) {
	t.Run("cancelled start and timed-out handler", func(t *testing.T) {
		repo, service, admission := checkpointFixture(t, 1)
		begin := beginFixture(admission, 0, 50)
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		if result, err := service.BeginAttempt(ctx, begin); !errors.Is(err, context.DeadlineExceeded) || result.Disposition != "" {
			t.Fatalf("cancelled begin: %+v, %v", result, err)
		}
		requireBegin(t, service, begin, 1, "4")
		finish := finishFixture(begin)
		finish.Status, finish.Output = constants.ExecutionTimedOut, nil
		finish.Error = json.RawMessage(`{"code":"NODE_TIMED_OUT","message":"Node timed out","retryable":false}`)
		if result, err := service.FinishAttempt(ctx, finish); err != nil || result.Cursor != "7" {
			t.Fatalf("finish after timeout: %+v, %v", result, err)
		}
		history, err := repo.Attempts(t.Context(), begin.DeviceExecutionID, 0, 0)
		if err != nil || len(history.Attempts) != 1 || history.Attempts[0].Status != constants.ExecutionTimedOut {
			t.Fatalf("timeout history: %+v, %v", history, err)
		}
	})
	t.Run("shutdown deadline bounds completion", func(t *testing.T) {
		repo, service, admission := checkpointFixture(t, 1)
		begin := beginFixture(admission, 0, 50)
		requireBegin(t, service, begin, 1, "4")
		finish := finishFixture(begin)
		finish.ShutdownDeadline = time.Now().Add(-time.Second)
		if result, err := service.FinishAttempt(t.Context(), finish); !errors.Is(err, xerror.ErrCheckpointFatal) ||
			!errors.Is(err, context.DeadlineExceeded) || result.Disposition != "" {
			t.Fatalf("expired checkpoint: %+v, %v", result, err)
		}
		assertUnfinishedCheckpoint(t, repo, begin)
	})
}

func assertUnfinishedCheckpoint(t *testing.T, repo *ExecutionRepository, begin execution.BeginAttemptRequest) {
	t.Helper()
	snapshot, err := repo.ActiveSnapshot(t.Context())
	if err != nil || snapshot.Cursor != "4" || snapshot.RunGroup.Status != constants.ExecutionRunning ||
		snapshot.DeviceExecutions[0].Status != constants.ExecutionRunning || snapshot.DeviceExecutions[0].NextNodeID == nil ||
		*snapshot.DeviceExecutions[0].NextNodeID != begin.NodeID {
		t.Fatalf("rollback snapshot: %+v, %v", snapshot, err)
	}
	history, err := repo.Attempts(t.Context(), begin.DeviceExecutionID, 0, 0)
	if err != nil || len(history.Attempts) != 1 || history.Attempts[0].Status != constants.ExecutionRunning ||
		history.Attempts[0].FinishedAt != nil || history.Attempts[0].Output != nil {
		t.Fatalf("rollback history: %+v, %v", history, err)
	}
}

func TestCheckpointStartRollsBackBeforeDispatch(t *testing.T) {
	for _, failure := range []struct{ name, trigger string }{
		{"attempt insert", "BEFORE INSERT ON node_attempts"},
		{"device update", "BEFORE UPDATE ON device_executions"},
		{"event append", "BEFORE INSERT ON execution_events"},
		{"cursor update", "BEFORE UPDATE OF last_committed_cursor ON event_stream_state"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			repo, service, admission := checkpointFixture(t, 1)
			begin := beginFixture(admission, 0, 50)
			injectCheckpointFault(t, repo, failure.trigger)
			wakeup := repo.EventWakeup()
			result, err := service.BeginAttempt(t.Context(), begin)
			if !errors.Is(err, xerror.ErrExecutionPersistence) || result.Disposition != "" {
				t.Fatalf("start authorized dispatch: %+v, %v", result, err)
			}
			snapshot, err := repo.ActiveSnapshot(t.Context())
			if err != nil || snapshot.Cursor != "2" || snapshot.RunGroup.Summary.Queued != 1 || snapshot.DeviceExecutions[0].StartedAt != nil {
				t.Fatalf("failed start changed snapshot: %+v, %v", snapshot, err)
			}
			history, err := repo.Attempts(t.Context(), begin.DeviceExecutionID, 0, 0)
			if err != nil || len(history.Attempts) != 0 {
				t.Fatalf("failed start wrote an attempt: %+v, %v", history, err)
			}
			assertNoCheckpointWakeup(t, wakeup)
		})
	}
}

func TestCheckpointFinishRollsBackAndStopsDispatch(t *testing.T) {
	for _, failure := range []struct{ name, trigger string }{
		{"attempt update", "BEFORE UPDATE ON node_attempts"},
		{"device update", "BEFORE UPDATE ON device_executions"},
		{"group update", "BEFORE UPDATE ON run_groups"},
		{"event append", "BEFORE INSERT ON execution_events"},
		{"cursor update", "BEFORE UPDATE OF last_committed_cursor ON event_stream_state"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			repo, service, admission := checkpointFixture(t, 1)
			begin := beginFixture(admission, 0, 50)
			dispatches := 0
			dispatch := func(request execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
				result, err := service.BeginAttempt(t.Context(), request)
				if err == nil && result.Disposition == constants.CheckpointCommitted {
					dispatches++ // Simulated external side effect.
				}
				return result, err
			}
			if result, err := dispatch(begin); err != nil || result.Cursor != "4" || dispatches != 1 {
				t.Fatalf("initial dispatch: %+v, %v, count %d", result, err, dispatches)
			}
			injectCheckpointFault(t, repo, failure.trigger)
			wakeup := repo.EventWakeup()
			result, err := service.FinishAttempt(t.Context(), finishFixture(begin))
			if !errors.Is(err, xerror.ErrCheckpointFatal) || !errors.Is(err, xerror.ErrExecutionPersistence) || result.Disposition != "" {
				t.Fatalf("finish should stop the run: %+v, %v", result, err)
			}
			if err := service.Wait(); !errors.Is(err, xerror.ErrCheckpointFatal) {
				t.Fatalf("supervisor did not receive fatal failure: %v", err)
			}
			assertUnfinishedCheckpoint(t, repo, begin)
			assertNoCheckpointWakeup(t, wakeup)
			begin.AttemptID, begin.ExpectedDeviceStatus = testExecutionID(51), constants.ExecutionRunning
			if next, err := dispatch(begin); !errors.Is(err, xerror.ErrCheckpointFatal) || next.Disposition != "" {
				t.Fatalf("begin after fatal persistence failure: %+v, %v", next, err)
			}
			if dispatches != 1 {
				t.Fatalf("unexpected handler dispatch count: %d", dispatches)
			}
		})
	}
}

func injectCheckpointFault(t *testing.T, repo *ExecutionRepository, trigger string) {
	t.Helper()
	query := "CREATE TRIGGER checkpoint_failure " + trigger + " BEGIN SELECT RAISE(ABORT, 'test checkpoint failure'); END"
	if _, err := repo.store.DB().ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
}

func assertNoCheckpointWakeup(t *testing.T, wakeup <-chan struct{}) {
	t.Helper()
	select {
	case <-wakeup:
		t.Fatal("checkpoint rollback published a wakeup")
	default:
	}
}

// Simulate the two possible database outcomes of an unconfirmed COMMIT.
type unconfirmedCheckpointRepo struct {
	*ExecutionRepository
	commit    bool
	readbacks int
}

func (s *unconfirmedCheckpointRepo) WithCheckpointTx(ctx context.Context, operation func(execution.CheckpointTx) error) error {
	err := s.WithWriteTx(ctx, func(tx Tx) error {
		if err := operation(tx); err != nil {
			return err
		}
		if !s.commit {
			return xerror.ErrExecutionCommitUnknown
		}
		*tx.eventsAppended = false
		return nil
	})
	if err != nil {
		return err
	}
	return xerror.ErrExecutionCommitUnknown
}

func (s *unconfirmedCheckpointRepo) ReadCheckpoint(ctx context.Context, ref execution.AttemptRef) (execution.CheckpointState, int64, error) {
	s.readbacks++
	return s.ExecutionRepository.ReadCheckpoint(ctx, ref)
}

func TestCheckpointReconcilesCommittedResultOnce(t *testing.T) {
	repo, _, admission := checkpointFixture(t, 1)
	uncertain := &unconfirmedCheckpointRepo{ExecutionRepository: repo, commit: true}
	service := services.NewCheckpointService(uncertain)
	begin := beginFixture(admission, 0, 50)
	requireBegin(t, service, begin, 1, "4")
	if uncertain.readbacks != 1 {
		t.Fatalf("begin readbacks: %d", uncertain.readbacks)
	}
	wakeup := repo.EventWakeup()
	finish := finishFixture(begin)
	requireFinish(t, service, finish, 1, "7")
	if uncertain.readbacks != 2 {
		t.Fatalf("finish readbacks: %d", uncertain.readbacks-1)
	}
	select {
	case <-wakeup:
	default:
		t.Fatal("confirmed commit did not notify observers")
	}
	if result, err := service.FinishAttempt(t.Context(), finish); err != nil || result.Disposition != constants.CheckpointAlreadyFinalized || uncertain.readbacks != 2 {
		t.Fatalf("duplicate reconciled completion: %+v, %v, %d readbacks", result, err, uncertain.readbacks)
	}
	page, err := repo.ReplayEvents(t.Context(), 4, 0)
	if err != nil || len(page.Events) != 3 || page.State.Watermark != 7 {
		t.Fatalf("reconciliation rewrote events: %+v, %v", page, err)
	}
}

func TestCheckpointUnconfirmedRollbackStopsExecution(t *testing.T) {
	repo, service, admission := checkpointFixture(t, 1)
	begin := beginFixture(admission, 0, 50)
	requireBegin(t, service, begin, 1, "4")
	uncertain := &unconfirmedCheckpointRepo{ExecutionRepository: repo}
	service = services.NewCheckpointService(uncertain)
	wakeup := repo.EventWakeup()
	result, err := service.FinishAttempt(t.Context(), finishFixture(begin))
	if !errors.Is(err, xerror.ErrCheckpointFatal) || !errors.Is(err, xerror.ErrExecutionCommitUnknown) || result.Disposition != "" || uncertain.readbacks != 1 {
		t.Fatalf("unresolved completion: %+v, %v, %d readbacks", result, err, uncertain.readbacks)
	}
	assertUnfinishedCheckpoint(t, repo, begin)
	assertNoCheckpointWakeup(t, wakeup)
}

type cancelCheckpointRepo struct {
	*ExecutionRepository
	cancel context.CancelFunc
}

func (s *cancelCheckpointRepo) WithCheckpointTx(ctx context.Context, operation func(execution.CheckpointTx) error) error {
	return s.ExecutionRepository.WithCheckpointTx(ctx, func(tx execution.CheckpointTx) error {
		err := operation(tx)
		s.cancel()
		return err
	})
}

func TestCheckpointStartCancelledAfterWrites(t *testing.T) {
	repo, _, admission := checkpointFixture(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service := services.NewCheckpointService(&cancelCheckpointRepo{ExecutionRepository: repo, cancel: cancel})
	begin := beginFixture(admission, 0, 50)
	wakeup := repo.EventWakeup()
	if result, err := service.BeginAttempt(ctx, begin); !errors.Is(err, context.Canceled) || result.Disposition != "" {
		t.Fatalf("cancelled start authorized a command: %+v, %v", result, err)
	}
	snapshot, err := repo.ActiveSnapshot(t.Context())
	if err != nil || snapshot.Cursor != "2" || snapshot.RunGroup.Summary.Queued != 1 {
		t.Fatalf("cancelled start changed state: %+v, %v", snapshot, err)
	}
	history, err := repo.Attempts(t.Context(), begin.DeviceExecutionID, 0, 0)
	if err != nil || len(history.Attempts) != 0 {
		t.Fatalf("cancelled start wrote an attempt: %+v, %v", history, err)
	}
	assertNoCheckpointWakeup(t, wakeup)
}

func TestConcurrentCheckpointsGrantOneContinuation(t *testing.T) {
	repo, service, admission := checkpointFixture(t, 1)
	begin := beginFixture(admission, 0, 50)
	compete := func(operation func() (execution.CheckpointResult, error), other constants.CheckpointDisposition) {
		t.Helper()
		type outcome struct {
			result execution.CheckpointResult
			err    error
		}
		var wg sync.WaitGroup
		outcomes := make(chan outcome, 2)
		for range 2 {
			wg.Go(func() {
				result, err := operation()
				outcomes <- outcome{result, err}
			})
		}
		wg.Wait()
		close(outcomes)
		counts := make(map[constants.CheckpointDisposition]int)
		for attempt := range outcomes {
			if attempt.err != nil {
				t.Fatal(attempt.err)
			}
			counts[attempt.result.Disposition]++
		}
		if counts[constants.CheckpointCommitted] != 1 || counts[other] != 1 {
			t.Fatalf("expected one committed checkpoint and one %s, got %v", other, counts)
		}
	}
	compete(func() (execution.CheckpointResult, error) {
		return service.BeginAttempt(t.Context(), begin)
	}, constants.CheckpointStale)
	compete(func() (execution.CheckpointResult, error) {
		return service.FinishAttempt(t.Context(), finishFixture(begin))
	}, constants.CheckpointAlreadyFinalized)
	page, err := repo.ReplayEvents(t.Context(), 0, 0)
	if err != nil || page.State.Watermark != 7 || len(page.Events) != 7 {
		t.Fatalf("duplicate checkpoints wrote events: %+v, %v", page, err)
	}
}
