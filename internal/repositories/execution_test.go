package repositories

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func testExecutionID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

func executionStore(t *testing.T) *ExecutionRepository {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	s := sqlitestore.New()
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return NewExecutionRepository(s)
}

func testAdmission(n int) execution.Admission {
	return execution.Admission{
		RequestID: testExecutionID(n), RunGroupID: testExecutionID(n + 1),
		Now: time.Date(2026, 11, 5, 0, 0, 0, 123000000, time.UTC),
		Devices: []execution.AdmissionDevice{
			{DeviceExecutionID: testExecutionID(n + 3), ADBSerial: "emulator-5556", EntryNodeID: "start_01"},
			{DeviceExecutionID: testExecutionID(n + 2), ADBSerial: "emulator-5554", EntryNodeID: "start_01"},
		},
	}
}

func TestAdmissionSnapshotAndIdempotency(t *testing.T) {
	s := executionStore(t)
	ctx := t.Context()
	admission := testAdmission(1)
	if _, err := s.ActiveSnapshot(ctx); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatalf("empty active snapshot: %v", err)
	}
	if got, err := s.Admit(ctx, admission); err != nil || got != (execution.AdmissionResult{Disposition: constants.AdmissionCreated, RunGroupID: admission.RunGroupID}) {
		t.Fatalf("new admission: %+v, %v", got, err)
	}
	if got, err := s.Admit(ctx, execution.Admission{RequestID: admission.RequestID}); err != nil || got != (execution.AdmissionResult{Disposition: constants.AdmissionExisting, RunGroupID: admission.RunGroupID}) {
		t.Fatalf("replay: %+v, %v", got, err)
	}
	if id, err := s.LookupRequest(ctx, admission.RequestID); err != nil || id != admission.RunGroupID {
		t.Fatalf("lookup: %q, %v", id, err)
	}
	if got, err := s.Admit(ctx, testAdmission(10)); err != nil || got.Disposition != constants.AdmissionBusy {
		t.Fatalf("busy: %+v, %v", got, err)
	}
	snapshot, err := s.ActiveSnapshot(ctx)
	if err != nil || snapshot.Cursor != "3" || snapshot.RunGroup.ID != admission.RunGroupID ||
		snapshot.RunGroup.Summary != (execution.RunSummary{Total: 2, Queued: 2}) ||
		snapshot.RunGroup.CreatedAt != "2026-11-05T00:00:00.123Z" || len(snapshot.DeviceExecutions) != 2 ||
		snapshot.DeviceExecutions[0].ID != admission.Devices[0].DeviceExecutionID {
		t.Fatalf("snapshot: %+v, %v", snapshot, err)
	}
	rows, err := s.store.DB().QueryContext(ctx, "SELECT event_type, coalesce(device_execution_id,''), payload_json FROM execution_events ORDER BY cursor")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := [][3]string{
		{"RUN_GROUP_STATE_CHANGED", "", `{"status":"RUNNING"}`},
		{"DEVICE_EXECUTION_STATE_CHANGED", admission.Devices[0].DeviceExecutionID, `{"status":"QUEUED","nextNodeId":"start_01"}`},
		{"DEVICE_EXECUTION_STATE_CHANGED", admission.Devices[1].DeviceExecutionID, `{"status":"QUEUED","nextNodeId":"start_01"}`},
	}
	for i, expected := range want {
		var got [3]string
		if !rows.Next() {
			t.Fatalf("missing event %d: %v", i, rows.Err())
		}
		if err := rows.Scan(&got[0], &got[1], &got[2]); err != nil || got != expected {
			t.Fatalf("event %d: %v, %v", i, got, err)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("unexpected event or read failure: %v", rows.Err())
	}
}

func TestConcurrentAdmission(t *testing.T) {
	for _, sameID := range []bool{false, true} {
		t.Run(fmt.Sprintf("same request=%t", sameID), func(t *testing.T) {
			s := executionStore(t)
			a, b := testAdmission(1), testAdmission(10)
			wantOther := constants.AdmissionBusy
			if sameID {
				b.RequestID = a.RequestID
				wantOther = constants.AdmissionExisting
			}
			start := make(chan struct{})
			type result struct {
				admission execution.AdmissionResult
				err       error
			}
			results := make(chan result, 2)
			for _, candidate := range []execution.Admission{a, b} {
				go func() {
					<-start
					value, err := s.Admit(t.Context(), candidate)
					results <- result{value, err}
				}()
			}
			close(start)
			first, second := <-results, <-results
			if second.admission.Disposition == constants.AdmissionCreated {
				first, second = second, first
			}
			if first.err != nil || second.err != nil || first.admission.Disposition != constants.AdmissionCreated ||
				second.admission.Disposition != wantOther ||
				(sameID && first.admission.RunGroupID != second.admission.RunGroupID) {
				t.Fatalf("concurrent admissions: %+v, %+v", first, second)
			}
		})
	}
}

func TestAdmissionRollsBackWhenEventWriteFails(t *testing.T) {
	s := executionStore(t)
	ctx := t.Context()
	// Force an event insertion failure after the group and device writes.
	if _, err := s.store.DB().ExecContext(ctx, `CREATE TRIGGER fail_event BEFORE INSERT ON execution_events
		BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	admission := testAdmission(1)
	if _, err := s.Admit(ctx, admission); !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("admission failure: %v", err)
	}
	var groups, devices, events, cursor int
	if err := s.store.DB().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM run_groups),
		(SELECT count(*) FROM device_executions), (SELECT count(*) FROM execution_events),
		last_committed_cursor FROM event_stream_state`).Scan(&groups, &devices, &events, &cursor); err != nil ||
		groups != 0 || devices != 0 || events != 0 || cursor != 0 {
		t.Fatalf("rollback: groups=%d devices=%d events=%d cursor=%d err=%v", groups, devices, events, cursor, err)
	}
}

func TestAttemptHistoryUsesSequenceAndPreservesNull(t *testing.T) {
	s := executionStore(t)
	ctx := t.Context()
	admission := testAdmission(1)
	if _, err := s.Admit(ctx, admission); err != nil {
		t.Fatal(err)
	}
	id := admission.Devices[0].DeviceExecutionID
	if empty, err := s.Attempts(ctx, id, 0, 0); err != nil || empty.Attempts == nil || len(empty.Attempts) != 0 {
		t.Fatalf("known empty history: %+v, %v", empty, err)
	}
	if _, err := s.Attempts(ctx, testExecutionID(99), 0, 1); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatalf("unknown owner: %v", err)
	}
	if _, err := s.store.DB().ExecContext(ctx, `INSERT INTO node_attempts VALUES
		('a1', ?, 1, 'start_01', 1, 'SUCCEEDED', 1, 2, '{}', NULL),
		('a2', ?, 2, 'tap_01', 1, 'RUNNING', 2, NULL, NULL, NULL)`, id, id); err != nil {
		t.Fatal(err)
	}
	first, err := s.Attempts(ctx, id, 0, 1)
	if err != nil || len(first.Attempts) != 1 || string(first.Attempts[0].Output) != "{}" ||
		first.NextAfterSequence == nil || *first.NextAfterSequence != 1 {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	second, err := s.Attempts(ctx, id, *first.NextAfterSequence, 1)
	if err != nil || len(second.Attempts) != 1 || second.Attempts[0].Sequence != 2 ||
		second.Attempts[0].Output != nil || second.Attempts[0].FinishedAt != nil || second.NextAfterSequence != nil {
		t.Fatalf("second page: %+v, %v", second, err)
	}
}

func TestWriteTransactionCancellationRollsBack(t *testing.T) {
	s := executionStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := s.WithWriteTx(ctx, func(tx Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_groups VALUES ('g','r','RUNNING',1,NULL)"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled transaction: %v", err)
	}
	if _, err := s.ActiveSnapshot(t.Context()); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatalf("cancelled write became visible: %v", err)
	}
}

func TestStaleUpdateDoesNotAdvanceSnapshot(t *testing.T) {
	s := executionStore(t)
	ctx := t.Context()
	admission := testAdmission(1)
	if _, err := s.Admit(ctx, admission); err != nil {
		t.Fatal(err)
	}
	err := s.WithWriteTx(ctx, func(tx Tx) error {
		return tx.UpdateExpected(ctx,
			"UPDATE run_groups SET status='PAUSED' WHERE id=? AND status='PAUSED'", admission.RunGroupID)
	})
	if !errors.Is(err, xerror.ErrExecutionStaleState) {
		t.Fatalf("stale update: %v", err)
	}
	snapshot, err := s.SnapshotByID(ctx, admission.RunGroupID)
	if err != nil || snapshot.RunGroup.Status != "RUNNING" || snapshot.Cursor != "3" {
		t.Fatalf("stale update changed snapshot: %+v, %v", snapshot, err)
	}
}
