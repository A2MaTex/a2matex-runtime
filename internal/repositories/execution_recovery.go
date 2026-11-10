package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var _ execution.RecoveryRepository = (*ExecutionRepository)(nil)

func (s *ExecutionRepository) WithRecoveryTx(ctx context.Context, operation func(execution.RecoveryTx) error) error {
	return s.WithWriteTx(ctx, func(tx Tx) error { return operation(tx) })
}

func (tx Tx) ReadRecoveryState(ctx context.Context) (execution.RecoveryState, error) {
	var state execution.RecoveryState
	const group = `SELECT id,status,created_at FROM run_groups WHERE status IN ('RUNNING','PAUSED')`
	err := tx.QueryRowContext(ctx, group).Scan(&state.RunGroupID, &state.Status, &state.CreatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return execution.RecoveryState{}, storageError(ctx, "read active group for recovery")
	}
	if state.RunGroupID != "" {
		state.Devices, err = tx.recoveryDevices(ctx, state.RunGroupID)
		if err != nil {
			return execution.RecoveryState{}, err
		}
	}
	// The partial index covers only unfinished attempts, including inconsistent
	// attempts that belong to a terminal group or a missing parent.
	state.Attempts, err = tx.recoveryAttempts(ctx)
	if err != nil {
		return execution.RecoveryState{}, err
	}
	return state, nil
}

func (tx Tx) recoveryDevices(ctx context.Context, groupID string) ([]execution.RecoveryDevice, error) {
	const query = `SELECT id,status,next_node_id,created_at,started_at FROM device_executions WHERE run_group_id = ? LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, groupID, constants.MaxAdmissionDevices+1)
	if err != nil {
		return nil, storageError(ctx, "read recovery devices")
	}
	defer rows.Close()
	var devices []execution.RecoveryDevice
	for rows.Next() {
		var device execution.RecoveryDevice
		var next sql.NullString
		var started sql.NullInt64
		if err := rows.Scan(&device.ID, &device.Status, &next, &device.CreatedAt, &started); err != nil {
			return nil, storageError(ctx, "decode recovery device")
		}
		device.NextNodeID = next.String
		if started.Valid {
			device.StartedAt = &started.Int64
		}
		devices = append(devices, device)
	}
	if rows.Err() != nil {
		return nil, storageError(ctx, "read recovery devices")
	}
	if len(devices) > constants.MaxAdmissionDevices {
		return nil, xerror.ErrRecoveryIntegrity
	}
	return devices, nil
}

func (tx Tx) recoveryAttempts(ctx context.Context) ([]execution.RecoveryAttempt, error) {
	const query = `SELECT a.id,a.device_execution_id,a.node_id,a.attempt_number,a.started_at,
		NOT EXISTS(SELECT 1 FROM node_attempts newer WHERE newer.device_execution_id = a.device_execution_id AND newer.sequence > a.sequence)
	FROM node_attempts a INDEXED BY node_attempts_one_running WHERE a.status = 'RUNNING' LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, constants.MaxAdmissionDevices+1)
	if err != nil {
		return nil, storageError(ctx, "read active attempts for recovery")
	}
	defer rows.Close()
	var attempts []execution.RecoveryAttempt
	for rows.Next() {
		var attempt execution.RecoveryAttempt
		if err := rows.Scan(&attempt.ID, &attempt.DeviceID, &attempt.NodeID, &attempt.Number, &attempt.StartedAt, &attempt.Latest); err != nil {
			return nil, storageError(ctx, "decode recovery attempt")
		}
		attempts = append(attempts, attempt)
	}
	if rows.Err() != nil {
		return nil, storageError(ctx, "read active attempts for recovery")
	}
	if len(attempts) > constants.MaxAdmissionDevices {
		return nil, xerror.ErrRecoveryIntegrity
	}
	return attempts, nil
}

func (tx Tx) InterruptRecovery(ctx context.Context, recovery execution.RecoveryWrite) error {
	finished, safeError := recovery.Now.UnixMilli(), string(recovery.Interruption)
	if recovery.Attempts > 0 {
		const attempts = `UPDATE node_attempts SET status = 'INTERRUPTED', finished_at = ?, error_json = ?
		WHERE status = 'RUNNING' AND device_execution_id IN (
			SELECT id FROM device_executions WHERE run_group_id = ? AND status = 'RUNNING')`
		if err := tx.recoveryUpdate(ctx, attempts, recovery.Attempts, finished, safeError, recovery.RunGroupID); err != nil {
			return err
		}
	}
	const devices = `UPDATE device_executions SET status = 'INTERRUPTED', next_node_id = NULL, finished_at = ?, terminal_error_json = ?
	WHERE run_group_id = ? AND status IN ('QUEUED','RUNNING','PAUSED')`
	if err := tx.recoveryUpdate(ctx, devices, recovery.Devices, finished, safeError, recovery.RunGroupID); err != nil {
		return err
	}
	const group = `UPDATE run_groups SET status = 'INTERRUPTED', finished_at = ? WHERE id = ? AND status IN ('RUNNING','PAUSED')`
	return tx.recoveryUpdate(ctx, group, 1, finished, recovery.RunGroupID)
}

func (tx Tx) recoveryUpdate(ctx context.Context, query string, expected int, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return storageError(ctx, "write interrupted execution state")
	}
	count, err := result.RowsAffected()
	if err != nil {
		return storageError(ctx, "verify interrupted execution write")
	}
	if count != int64(expected) {
		return xerror.ErrRecoveryIntegrity
	}
	return nil
}
