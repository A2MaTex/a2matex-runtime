package repositories

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const runColumns = "SELECT id,request_id,status,created_at,finished_at FROM run_groups "

func (s *ExecutionRepository) ActiveSnapshot(ctx context.Context) (execution.Snapshot, error) {
	return s.snapshot(ctx, runColumns+"WHERE status IN ('RUNNING','PAUSED')")
}

func (s *ExecutionRepository) SnapshotByID(ctx context.Context, id string) (execution.Snapshot, error) {
	if !execution.ValidID(id) {
		return execution.Snapshot{}, xerror.ErrExecutionInvalidInput
	}
	return s.snapshot(ctx, runColumns+"WHERE id = ?", id)
}

func (s *ExecutionRepository) snapshot(ctx context.Context, query string, args ...any) (execution.Snapshot, error) {
	var snapshot execution.Snapshot
	err := s.withReadTx(ctx, func(tx Tx) error {
		group := &snapshot.RunGroup
		var created int64
		var finished sql.NullInt64
		err := tx.QueryRowContext(ctx, query, args...).Scan(&group.ID, &group.RequestID, &group.Status, &created, &finished)
		if errors.Is(err, sql.ErrNoRows) {
			return xerror.ErrExecutionNotFound
		}
		if err != nil {
			return storageError(ctx, "read run group")
		}
		group.CreatedAt, group.FinishedAt = stamp(created), nullableStamp(finished)

		snapshot.DeviceExecutions, err = readDevices(ctx, tx, group.ID)
		if err != nil {
			return err
		}
		for _, device := range snapshot.DeviceExecutions {
			countStatus(&group.Summary, device.Status)
		}
		var cursor int64
		if err := tx.QueryRowContext(ctx, "SELECT last_committed_cursor FROM event_stream_state WHERE id = 1").Scan(&cursor); err != nil {
			return storageError(ctx, "read snapshot watermark")
		}
		snapshot.Cursor = strconv.FormatInt(cursor, 10)
		return nil
	})
	if err != nil {
		return execution.Snapshot{}, err
	}
	return snapshot, nil
}

func readDevices(ctx context.Context, tx Tx, groupID string) ([]execution.DeviceExecution, error) {
	const query = `SELECT id,adb_serial,status,next_node_id,created_at,started_at,finished_at,terminal_error_json
		FROM device_executions WHERE run_group_id = ? ORDER BY admission_ordinal,id LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, groupID, constants.MaxAdmissionDevices+1)
	if err != nil {
		return nil, storageError(ctx, "read run devices")
	}
	defer rows.Close()

	devices := make([]execution.DeviceExecution, 0)
	for rows.Next() {
		var device execution.DeviceExecution
		var created int64
		var started, finished sql.NullInt64
		var terminalError []byte
		if err := rows.Scan(&device.ID, &device.ADBSerial, &device.Status, &device.NextNodeID, &created,
			&started, &finished, &terminalError); err != nil {
			return nil, storageError(ctx, "decode device state")
		}
		device.CreatedAt, device.StartedAt, device.FinishedAt = stamp(created), nullableStamp(started), nullableStamp(finished)
		device.TerminalError = terminalError
		devices = append(devices, device)
	}
	if rows.Err() != nil || len(devices) == 0 || len(devices) > constants.MaxAdmissionDevices {
		return nil, storageError(ctx, "verify run devices")
	}
	return devices, nil
}

func countStatus(summary *execution.RunSummary, status string) {
	summary.Total++
	switch status {
	case "QUEUED":
		summary.Queued++
	case "RUNNING":
		summary.Running++
	case "PAUSED":
		summary.Paused++
	case "SUCCEEDED":
		summary.Succeeded++
	case "FAILED":
		summary.Failed++
	case "CANCELLED":
		summary.Cancelled++
	case "INTERRUPTED":
		summary.Interrupted++
	}
}

func (s *ExecutionRepository) Attempts(ctx context.Context, deviceID string, afterSequence int64, limit int) (execution.AttemptPage, error) {
	if !execution.ValidID(deviceID) || afterSequence < 0 || limit < 0 || limit > constants.MaxAttemptPageSize {
		return execution.AttemptPage{}, xerror.ErrExecutionInvalidInput
	}
	if limit == 0 {
		limit = constants.MaxAttemptPageSize
	}
	page := execution.AttemptPage{Attempts: make([]execution.NodeAttempt, 0)}
	err := s.withReadTx(ctx, func(tx Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM device_executions WHERE id = ?)", deviceID).Scan(&exists); err != nil {
			return storageError(ctx, "look up attempt owner")
		}
		if !exists {
			return xerror.ErrExecutionNotFound
		}
		const query = `SELECT id,sequence,node_id,attempt_number,status,started_at,finished_at,output_json,error_json
			FROM node_attempts WHERE device_execution_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`
		rows, err := tx.QueryContext(ctx, query, deviceID, afterSequence, limit+1)
		if err != nil {
			return storageError(ctx, "read attempt history")
		}
		defer rows.Close()
		for rows.Next() {
			if len(page.Attempts) == limit {
				next := page.Attempts[len(page.Attempts)-1].Sequence
				page.NextAfterSequence = &next
				break
			}
			attempt := execution.NodeAttempt{DeviceExecutionID: deviceID}
			var started int64
			var finished sql.NullInt64
			var output, attemptError []byte
			if err := rows.Scan(&attempt.ID, &attempt.Sequence, &attempt.NodeID, &attempt.AttemptNumber,
				&attempt.Status, &started, &finished, &output, &attemptError); err != nil {
				return storageError(ctx, "decode attempt history")
			}
			attempt.StartedAt, attempt.FinishedAt = stamp(started), nullableStamp(finished)
			attempt.Output, attempt.Error = output, attemptError
			page.Attempts = append(page.Attempts, attempt)
		}
		if rows.Err() != nil {
			return storageError(ctx, "read attempt history page")
		}
		if err := rows.Close(); err != nil {
			return storageError(ctx, "close attempt history page")
		}
		return nil
	})
	if err != nil {
		return execution.AttemptPage{}, err
	}
	return page, nil
}
