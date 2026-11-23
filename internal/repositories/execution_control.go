package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var _ execution.GroupControlRepository = (*ExecutionRepository)(nil)

func (s *ExecutionRepository) WithGroupControlTx(ctx context.Context, operation func(execution.GroupControlTx) error) error {
	return s.WithWriteTx(ctx, func(tx Tx) error { return operation(tx) })
}

func (s *ExecutionRepository) ReadGroupControl(ctx context.Context, id string) (state execution.GroupControlState, err error) {
	err = s.withReadTx(ctx, func(tx Tx) error {
		state, err = tx.GroupControlState(ctx, id)
		return err
	})
	return state, err
}

func (tx Tx) GroupControlState(ctx context.Context, id string) (execution.GroupControlState, error) {
	var state execution.GroupControlState
	err := tx.QueryRowContext(ctx, `SELECT status, finished_at IS NOT NULL FROM run_groups WHERE id=?`, id).Scan(&state.Status, &state.Finished)
	if errors.Is(err, sql.ErrNoRows) {
		return state, xerror.ErrExecutionNotFound
	}
	if err != nil {
		return state, storageError(ctx, "read_control_group", err)
	}
	const query = `SELECT d.id,d.status,COALESCE(d.next_node_id,''),COALESCE(a.sequence,0),COALESCE(a.id,''),
		EXISTS(SELECT 1 FROM node_attempts r WHERE r.device_execution_id=d.id AND r.status='RUNNING'),
		d.finished_at IS NOT NULL,d.terminal_error_json IS NOT NULL
		FROM device_executions d LEFT JOIN node_attempts a ON a.id=
		(SELECT id FROM node_attempts WHERE device_execution_id=d.id ORDER BY sequence DESC LIMIT 1)
		WHERE d.run_group_id=? ORDER BY d.admission_ordinal,d.id`
	rows, err := tx.QueryContext(ctx, query, id)
	if err != nil {
		return state, storageError(ctx, "read_control_members", err)
	}
	defer rows.Close()
	for rows.Next() {
		var member execution.GroupControlMember
		if err := rows.Scan(&member.DeviceExecutionID, &member.Status, &member.NextNodeID, &member.Sequence, &member.AttemptID,
			&member.HasActiveAttempt, &member.Finished, &member.HasError); err != nil {
			return state, storageError(ctx, "decode_control_member", err)
		}
		state.Members = append(state.Members, member)
	}
	if err := rows.Err(); err != nil {
		return state, storageError(ctx, "read_control_members", err)
	}
	stream, err := eventState(ctx, tx)
	state.Watermark = stream.Watermark
	return state, err
}

func (tx Tx) UpdateGroupControl(ctx context.Context, id string, pause bool, count int) error {
	from, to, member := constants.ExecutionPaused, constants.ExecutionRunning, constants.ExecutionQueued
	query := `UPDATE device_executions SET status=? WHERE run_group_id=? AND status='PAUSED'`
	if pause {
		from, to, member = constants.ExecutionRunning, constants.ExecutionPaused, constants.ExecutionPaused
		query = `UPDATE device_executions SET status=? WHERE run_group_id=? AND status IN ('QUEUED','RUNNING')`
	}
	if err := tx.recoveryUpdate(ctx, "update_control_members", query, count, member, id); err != nil {
		return err
	}
	return tx.updateExpected(ctx, "update_control_group", `UPDATE run_groups SET status=? WHERE id=? AND status=?`, to, id, from)
}
