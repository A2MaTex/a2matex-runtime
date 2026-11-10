package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var _ execution.CheckpointRepository = (*ExecutionRepository)(nil)

func (s *ExecutionRepository) WithCheckpointTx(ctx context.Context, operation func(execution.CheckpointTx) error) error {
	return s.WithWriteTx(ctx, func(tx Tx) error { return operation(tx) })
}

func (s *ExecutionRepository) ReadCheckpoint(ctx context.Context, ref execution.AttemptRef) (execution.CheckpointState, int64, error) {
	var state execution.CheckpointState
	var watermark int64
	err := s.withReadTx(ctx, func(tx Tx) error {
		var err error
		state, err = tx.CheckpointState(ctx, ref, ref.AttemptID)
		if err != nil {
			return err
		}
		stream, err := eventState(ctx, tx)
		watermark = stream.Watermark
		return err
	})
	return state, watermark, err
}

func (s *ExecutionRepository) NotifyRecoveredCheckpoint() { s.notifyEvents() }

// An empty attemptID selects the latest attempt across the device's entire history.
func (tx Tx) CheckpointState(ctx context.Context, ref execution.AttemptRef, attemptID string) (execution.CheckpointState, error) {
	const query = `SELECT g.status, d.status, d.next_node_id, d.created_at,
		COALESCE((SELECT max(sequence) FROM node_attempts WHERE device_execution_id = d.id), 0),
		EXISTS(SELECT 1 FROM node_attempts WHERE device_execution_id = d.id AND status = 'RUNNING'),
		a.id, a.node_id, a.status, a.sequence, a.attempt_number, a.started_at, a.finished_at, a.output_json, a.error_json
	FROM device_executions d JOIN run_groups g ON g.id = d.run_group_id
	LEFT JOIN node_attempts a ON a.device_execution_id = d.id AND a.id = COALESCE(NULLIF(?, ''),
		(SELECT id FROM node_attempts WHERE device_execution_id = d.id ORDER BY sequence DESC LIMIT 1))
	WHERE g.id = ? AND d.id = ?`
	var state execution.CheckpointState
	var next, id, node, status sql.NullString
	var sequence, number, started, finished sql.NullInt64
	var output, attemptError []byte
	err := tx.QueryRowContext(ctx, query, attemptID, ref.RunGroupID, ref.DeviceExecutionID).Scan(
		&state.GroupStatus, &state.DeviceStatus, &next, &state.DeviceCreatedAt,
		&state.LatestSequence, &state.HasActiveAttempt,
		&id, &node, &status, &sequence, &number, &started, &finished, &output, &attemptError)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.CheckpointState{}, xerror.ErrExecutionNotFound
	}
	if err != nil {
		return execution.CheckpointState{}, storageError(ctx, "read checkpoint state")
	}
	state.NextNodeID = next.String
	if id.Valid {
		state.Attempt = &execution.CheckpointAttempt{
			ID: id.String, NodeID: node.String, Status: status.String, Sequence: sequence.Int64,
			Number: int(number.Int64), StartedAt: started.Int64, Output: output, Error: attemptError,
		}
		if finished.Valid {
			state.Attempt.FinishedAt = &finished.Int64
		}
	}
	return state, nil
}

func (tx Tx) InsertAttempt(ctx context.Context, request execution.BeginAttemptRequest, sequence int64) error {
	const query = `INSERT INTO node_attempts(id, device_execution_id, sequence, node_id, attempt_number, status, started_at)
	SELECT ?, ?, ?, ?, ?, 'RUNNING', ?
	WHERE NOT EXISTS(SELECT 1 FROM node_attempts WHERE id = ?)
	AND NOT EXISTS(SELECT 1 FROM node_attempts WHERE device_execution_id = ? AND node_id = ? AND attempt_number = ?)`
	return tx.UpdateExpected(ctx, query, request.AttemptID, request.DeviceExecutionID, sequence,
		request.NodeID, request.AttemptNumber, request.Now.UnixMilli(), request.AttemptID,
		request.DeviceExecutionID, request.NodeID, request.AttemptNumber)
}

func (tx Tx) StartDevice(ctx context.Context, request execution.BeginAttemptRequest) error {
	const query = `UPDATE device_executions SET status = 'RUNNING', started_at = COALESCE(started_at, ?)
	WHERE id = ? AND run_group_id = ? AND status = 'QUEUED' AND next_node_id = ?`
	return tx.UpdateExpected(ctx, query, request.Now.UnixMilli(), request.DeviceExecutionID, request.RunGroupID, request.NodeID)
}

func (tx Tx) FinalizeAttempt(ctx context.Context, request execution.FinishAttemptRequest) error {
	const query = `UPDATE node_attempts SET status = ?, finished_at = ?, output_json = ?, error_json = ?
	WHERE id = ? AND device_execution_id = ? AND node_id = ? AND status = 'RUNNING'`
	return tx.UpdateExpected(ctx, query, request.Status, request.Now.UnixMilli(), nullableJSON(request.Output),
		nullableJSON(request.Error), request.AttemptID, request.DeviceExecutionID, request.NodeID)
}

func (tx Tx) UpdateCheckpointDevice(ctx context.Context, device execution.DeviceCheckpoint) error {
	var finished any
	if device.NextNodeID == "" {
		finished = device.Now.UnixMilli()
	}
	const query = `UPDATE device_executions SET status = ?, next_node_id = ?, finished_at = ?, terminal_error_json = ?
	WHERE id = ? AND run_group_id = ? AND status = 'RUNNING' AND next_node_id = ?`
	return tx.UpdateExpected(ctx, query, device.Status, nullableID(device.NextNodeID), finished,
		nullableJSON(device.Error), device.DeviceExecutionID, device.RunGroupID, device.NodeID)
}

func (tx Tx) OtherDeviceStatuses(ctx context.Context, ref execution.AttemptRef) ([]string, error) {
	const query = `SELECT status FROM device_executions WHERE run_group_id = ? AND id <> ? LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, ref.RunGroupID, ref.DeviceExecutionID, constants.MaxAdmissionDevices)
	if err != nil {
		return nil, storageError(ctx, "read sibling device states")
	}
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return nil, storageError(ctx, "decode sibling device state")
		}
		statuses = append(statuses, status)
	}
	if rows.Err() != nil || len(statuses) >= constants.MaxAdmissionDevices {
		return nil, storageError(ctx, "verify sibling device states")
	}
	return statuses, nil
}

func (tx Tx) UpdateCheckpointGroup(ctx context.Context, runGroupID, status string, now time.Time) error {
	var finished any
	if status != constants.ExecutionPaused {
		finished = now.UnixMilli()
	}
	const query = `UPDATE run_groups SET status = ?, finished_at = ? WHERE id = ? AND status = 'RUNNING'`
	return tx.UpdateExpected(ctx, query, status, finished, runGroupID)
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
