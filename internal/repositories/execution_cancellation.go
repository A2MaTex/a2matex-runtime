package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (tx Tx) ReadCancellationTarget(ctx context.Context, id string) (execution.CancellationTarget, error) {
	var target execution.CancellationTarget
	const query = `SELECT g.status,
		EXISTS(SELECT 1 FROM device_executions d WHERE d.run_group_id=g.id AND d.status IN ('QUEUED','RUNNING','PAUSED')) OR
		EXISTS(SELECT 1 FROM node_attempts a JOIN device_executions d ON d.id=a.device_execution_id WHERE d.run_group_id=g.id AND a.status='RUNNING')
		FROM run_groups g WHERE g.id=?`
	err := tx.QueryRowContext(ctx, query, id).Scan(&target.Status, &target.HasUnfinished)
	if errors.Is(err, sql.ErrNoRows) {
		return target, xerror.ErrExecutionNotFound
	}
	if err != nil {
		return target, storageError(ctx, "read_cancellation_target", err)
	}
	return target, nil
}

func (s *ExecutionRepository) WithCancellationTx(ctx context.Context, operation func(execution.CancellationTx) error) error {
	return s.WithWriteTx(ctx, func(tx Tx) error { return operation(tx) })
}

func (tx Tx) ReadCancellationState(ctx context.Context) (execution.RecoveryState, error) {
	return tx.ReadRecoveryState(ctx)
}

func (tx Tx) CancelUnfinished(ctx context.Context, write execution.CancellationWrite) error {
	if write.Attempts > 0 {
		const query = `UPDATE node_attempts SET status='CANCELLED', finished_at=?, output_json=NULL, error_json=NULL
		WHERE status='RUNNING' AND device_execution_id IN
		(SELECT id FROM device_executions WHERE run_group_id=? AND status='RUNNING')`
		if err := tx.recoveryUpdate(ctx, "cancel_unfinished_attempts", query, write.Attempts, write.Now.UnixMilli(), write.RunGroupID); err != nil {
			return err
		}
	}
	const devices = `UPDATE device_executions SET status='CANCELLED', next_node_id=NULL, finished_at=?, terminal_error_json=NULL
	WHERE run_group_id=? AND status IN ('QUEUED','RUNNING','PAUSED')`
	if err := tx.recoveryUpdate(ctx, "cancel_unfinished_devices", devices, write.Devices, write.Now.UnixMilli(), write.RunGroupID); err != nil {
		return err
	}
	const group = `UPDATE run_groups SET status='CANCELLED', finished_at=? WHERE id=? AND status IN ('RUNNING','PAUSED')`
	return tx.recoveryUpdate(ctx, "cancel_unfinished_group", group, 1, write.Now.UnixMilli(), write.RunGroupID)
}
