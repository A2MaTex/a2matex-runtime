package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func admittedEventStore(t *testing.T) (*ExecutionRepository, execution.Admission) {
	t.Helper()
	s := executionStore(t)
	admission := testAdmission(0)
	if _, err := s.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	return s, admission
}

func groupEvent(admission execution.Admission) execution.PendingEvent {
	return execution.PendingEvent{Type: constants.EventRunGroupStateChanged,
		RunGroupID: admission.RunGroupID, OccurredAt: admission.Now,
		Payload: json.RawMessage(`{"status":"RUNNING"}`)}
}

func appendGroupEvent(ctx context.Context, s *ExecutionRepository, admission execution.Admission) error {
	return s.WithWriteTx(ctx, func(tx Tx) error {
		_, err := tx.AppendEvents(ctx, []execution.PendingEvent{groupEvent(admission)})
		return err
	})
}

func TestEventReplayPagesAndWireEnvelope(t *testing.T) {
	s, admission := admittedEventStore(t)
	page, err := s.ReplayEvents(t.Context(), 0, 2)
	if err != nil || len(page.Events) != 2 || !page.More || page.State.Watermark != 3 {
		t.Fatalf("first page: %+v, %v", page, err)
	}
	wire, err := json.Marshal(page.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(wire, &fields); err != nil || len(fields) != 5 || fields["cursor"] != "1" ||
		fields["occurredAt"] != "2026-11-05T00:00:00.123Z" {
		t.Fatalf("unexpected envelope: %s, %v", wire, err)
	}
	if err := appendGroupEvent(t.Context(), s, admission); err != nil {
		t.Fatal(err)
	}
	page, err = s.ReplayEvents(t.Context(), 2, 2)
	if err != nil || len(page.Events) != 2 || page.More || page.Events[0].Cursor != "3" || page.Events[1].Cursor != "4" {
		t.Fatalf("next page: %+v, %v", page, err)
	}
	for _, test := range []struct {
		after int64
		limit int
		want  error
	}{{-1, 0, xerror.ErrExecutionInvalidInput}, {0, 101, xerror.ErrExecutionInvalidInput}, {5, 0, xerror.ErrEventCursorFuture}} {
		if _, err := s.ReplayEvents(t.Context(), test.after, test.limit); !errors.Is(err, test.want) {
			t.Fatalf("after %d limit %d: %v", test.after, test.limit, err)
		}
	}
}

func TestEventRollbackPublishesNothing(t *testing.T) {
	s, admission := admittedEventStore(t)
	wakeup := s.EventWakeup()
	<-s.EventMaintenance() // Consume the already committed admission signal.
	abort := errors.New("abort checkpoint")
	err := s.WithWriteTx(t.Context(), func(tx Tx) error {
		if err := tx.UpdateExpected(t.Context(), "UPDATE run_groups SET status = 'PAUSED' WHERE id = ?", admission.RunGroupID); err != nil {
			return err
		}
		event := groupEvent(admission)
		event.Payload = json.RawMessage(`{"status":"PAUSED"}`)
		if _, err := tx.AppendEvents(t.Context(), []execution.PendingEvent{event}); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	select {
	case <-wakeup:
		t.Fatal("rollback woke publisher")
	default:
	}
	select {
	case <-s.EventMaintenance():
		t.Fatal("rollback scheduled retention")
	default:
	}
	snapshot, err := s.ActiveSnapshot(t.Context())
	if err != nil || snapshot.Cursor != "3" || snapshot.RunGroup.Status != "RUNNING" {
		t.Fatalf("rollback snapshot: %+v, %v", snapshot, err)
	}
	page, err := s.ReplayEvents(t.Context(), 3, 0)
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("rollback events: %+v, %v", page, err)
	}
}

func TestConcurrentEventAppendsAndSlowSubscriber(t *testing.T) {
	s, admission := admittedEventStore(t)
	first, second := s.EventWakeup(), s.EventWakeup()
	const count = 20
	var wg sync.WaitGroup
	errorsCh := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsCh <- appendGroupEvent(t.Context(), s, admission)
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, wakeup := range []<-chan struct{}{first, second} {
		select {
		case <-wakeup:
		default:
			t.Fatal("commits did not wake both subscribers")
		}
	}
	page, err := s.ReplayEvents(t.Context(), 3, 0)
	if err != nil || len(page.Events) != count {
		t.Fatalf("concurrent replay: %+v, %v", page, err)
	}
	for index, event := range page.Events {
		if event.Cursor != strconv.Itoa(index+4) {
			t.Fatalf("cursor %s at position %d", event.Cursor, index)
		}
	}
}

func TestEventSnapshotAndEmptyReplayHandoff(t *testing.T) {
	s, admission := admittedEventStore(t)
	snapshot, err := s.ActiveSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := appendGroupEvent(t.Context(), s, admission); err != nil {
		t.Fatal(err)
	}
	after, _ := execution.ParseEventCursor(snapshot.Cursor)
	page, err := s.ReplayEvents(t.Context(), after, 0)
	if err != nil || len(page.Events) != 1 || page.Events[0].Cursor != "4" {
		t.Fatalf("snapshot handoff lost commit: %+v, %v", page, err)
	}
	wakeup := s.EventWakeup() // Register before the empty query.
	page, err = s.ReplayEvents(t.Context(), 4, 0)
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("expected empty replay: %+v, %v", page, err)
	}
	if err := appendGroupEvent(t.Context(), s, admission); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wakeup:
	default:
		t.Fatal("commit between query and wait was lost")
	}
	// A missed/coalesced signal is harmless to the periodic durable recheck.
	page, err = s.ReplayEvents(t.Context(), 4, 0)
	if err != nil || len(page.Events) != 1 || page.Events[0].Cursor != "5" {
		t.Fatalf("durable recheck lost commit: %+v, %v", page, err)
	}
}

func TestEventAppendRejectsInvalidOwnershipAndOverflow(t *testing.T) {
	s, admission := admittedEventStore(t)
	wrong := groupEvent(admission)
	wrong.RunGroupID = testExecutionID(99)
	err := s.WithWriteTx(t.Context(), func(tx Tx) error {
		_, err := tx.AppendEvents(t.Context(), []execution.PendingEvent{groupEvent(admission), wrong})
		return err
	})
	if !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("invalid ownership: %v", err)
	}
	state, err := s.EventWatermark(t.Context())
	if err != nil || state.Watermark != 3 {
		t.Fatalf("partial batch committed: %+v %v", state, err)
	}
	if _, err := s.store.DB().ExecContext(t.Context(), "UPDATE event_stream_state SET last_committed_cursor = ?", int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if err := appendGroupEvent(t.Context(), s, admission); !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("overflow: %v", err)
	}
}

func TestEventRetentionBoundariesAndRestart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	s := NewExecutionRepository(store)
	admission := testAdmission(0)
	if _, err := s.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneEvents(t.Context(), 1, 1); err != nil || n != 1 {
		t.Fatalf("bounded prune: %d %v", n, err)
	}
	if _, err := s.ReplayEvents(t.Context(), 0, 0); !errors.Is(err, xerror.ErrEventCursorExpired) {
		t.Fatalf("below floor: %v", err)
	}
	page, err := s.ReplayEvents(t.Context(), 1, 0)
	if err != nil || len(page.Events) != 2 || page.State.Floor != 1 {
		t.Fatalf("at floor: %+v %v", page, err)
	}
	if n, err := s.PruneEvents(t.Context(), 0, 512); err != nil || n != 2 {
		t.Fatalf("prune all: %d %v", n, err)
	}
	if err := store.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	store = sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	s = NewExecutionRepository(store)
	page, err = s.ReplayEvents(t.Context(), 3, 0)
	if err != nil || len(page.Events) != 0 || page.State.Floor != 3 || page.State.Watermark != 3 {
		t.Fatalf("reopened fully pruned history: %+v %v", page, err)
	}
	if err := appendGroupEvent(t.Context(), s, admission); err != nil {
		t.Fatal(err)
	}
	page, err = s.ReplayEvents(t.Context(), 3, 0)
	if err != nil || len(page.Events) != 1 || page.Events[0].Cursor != "4" {
		t.Fatalf("cursor reused after restart: %+v %v", page, err)
	}
}

func TestEventPruneFailureRollsBackFloorAndRows(t *testing.T) {
	s, _ := admittedEventStore(t)
	if _, err := s.store.DB().ExecContext(t.Context(), `CREATE TRIGGER fail_floor BEFORE UPDATE OF replay_floor
ON event_stream_state BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneEvents(t.Context(), 0, 512); !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("prune failure: %v", err)
	}
	page, err := s.ReplayEvents(t.Context(), 0, 0)
	if err != nil || len(page.Events) != 3 || page.State.Floor != 0 {
		t.Fatalf("prune was not atomic: %+v %v", page, err)
	}
}

func TestAttemptEventsAndOwnershipChain(t *testing.T) {
	s, admission := admittedEventStore(t)
	ctx := t.Context()
	attemptID := testExecutionID(50)
	err := s.WithWriteTx(ctx, func(tx Tx) error {
		const insert = `INSERT INTO node_attempts(id,device_execution_id,sequence,node_id,attempt_number,status,started_at,finished_at,error_json)
VALUES (?,?,1,'tap_01',1,'FAILED',?,?,?)`
		failure := `{"code":"COMMAND_FAILED","message":"Command failed","retryable":false}`
		if _, err := tx.ExecContext(ctx, insert, attemptID, admission.Devices[0].DeviceExecutionID,
			admission.Now.UnixMilli(), admission.Now.UnixMilli(), failure); err != nil {
			return err
		}
		base := execution.PendingEvent{Type: constants.EventAttemptStateChanged, RunGroupID: admission.RunGroupID,
			DeviceExecutionID: admission.Devices[0].DeviceExecutionID, NodeAttemptID: attemptID, NodeID: "tap_01",
			OccurredAt: admission.Now, Payload: json.RawMessage(`{"status":"FAILED","attemptNumber":1,"error":` + failure + `}`)}
		retry := base
		retry.Type, retry.Payload = constants.EventRetryScheduled, json.RawMessage(`{"nextAttemptNumber":2,"delayMs":1000}`)
		_, err := tx.AppendEvents(ctx, []execution.PendingEvent{base, retry})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ReplayEvents(ctx, 3, 0)
	if err != nil || len(page.Events) != 2 || page.Events[0].NodeAttemptID != attemptID ||
		page.Events[1].Type != constants.EventRetryScheduled {
		t.Fatalf("attempt replay: %+v %v", page, err)
	}
	wrong := execution.PendingEvent{Type: constants.EventRetryScheduled, RunGroupID: admission.RunGroupID,
		DeviceExecutionID: admission.Devices[1].DeviceExecutionID, NodeAttemptID: attemptID, NodeID: "tap_01",
		OccurredAt: admission.Now, Payload: json.RawMessage(`{"nextAttemptNumber":2,"delayMs":1000}`)}
	err = s.WithWriteTx(ctx, func(tx Tx) error {
		_, err := tx.AppendEvents(ctx, []execution.PendingEvent{wrong})
		return err
	})
	if !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("cross-device attempt accepted: %v", err)
	}
	wrong.DeviceExecutionID = admission.Devices[0].DeviceExecutionID
	wrong.Payload = json.RawMessage(`{"nextAttemptNumber":2,"delayMs":1000,"rawOutput":"secret"}`)
	err = s.WithWriteTx(ctx, func(tx Tx) error {
		_, err := tx.AppendEvents(ctx, []execution.PendingEvent{wrong})
		return err
	})
	if !errors.Is(err, xerror.ErrExecutionInvalidInput) {
		t.Fatalf("unknown payload field accepted: %v", err)
	}
}

func TestRetentionWorkerPrunesStartupBacklog(t *testing.T) {
	s, admission := admittedEventStore(t)
	// Seed a backlog in one fixture write, as if the process stopped before maintenance.
	const seed = `WITH RECURSIVE cursors(n) AS (
SELECT 4 UNION ALL SELECT n+1 FROM cursors WHERE n < ?)
INSERT INTO execution_events(cursor,event_type,run_group_id,occurred_at,payload_json)
SELECT n,'RUN_GROUP_STATE_CHANGED',?,?,'{"status":"RUNNING"}' FROM cursors`
	latest := constants.EventRetention + constants.EventPruneBatchSize + 3
	err := s.WithWriteTx(t.Context(), func(tx Tx) error {
		if _, err := tx.ExecContext(t.Context(), seed, latest, admission.RunGroupID, admission.Now.UnixMilli()); err != nil {
			return err
		}
		return tx.UpdateExpected(t.Context(), "UPDATE event_stream_state SET last_committed_cursor = ? WHERE id = 1", latest)
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := services.NewEventRetention(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Shutdown(context.Background()) })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		state, err := s.EventWatermark(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state.Floor == int64(latest-constants.EventRetention) {
			if state.Watermark != int64(latest) {
				t.Fatal("retention changed watermark")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("retention did not catch up")
		case <-time.After(time.Millisecond):
		}
	}
	if err := worker.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
