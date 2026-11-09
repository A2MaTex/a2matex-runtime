package repositories

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// AppendEvents shares the caller's state transaction. Only WithWriteTx publishes
// wakeups, after commit. A failed append must abort the enclosing transaction.
func (tx Tx) AppendEvents(ctx context.Context, events []execution.PendingEvent) ([]string, error) {
	if tx.eventsAppended == nil || len(events) == 0 || len(events) > constants.MaxEventBatchSize {
		return nil, xerror.ErrExecutionInvalidInput
	}
	for _, event := range events {
		if !execution.ValidEvent(event) {
			return nil, xerror.ErrExecutionInvalidInput
		}
	}
	state, err := eventState(ctx, tx)
	if err != nil {
		return nil, err
	}
	if state.Watermark > math.MaxInt64-int64(len(events)) {
		return nil, storageError(ctx, "allocate execution event cursors")
	}
	rows, args := make([]string, 0, len(events)), make([]any, 0, len(events)*8)
	cursors := make([]string, 0, len(events))
	for index, event := range events {
		cursor := state.Watermark + int64(index) + 1
		rows = append(rows, "(?,?,?,?,?,?,?,?)")
		args = append(args, cursor, event.Type, event.RunGroupID, nullableID(event.DeviceExecutionID),
			nullableID(event.NodeAttemptID), nullableID(event.NodeID), event.OccurredAt.UnixMilli(), string(event.Payload))
		cursors = append(cursors, strconv.FormatInt(cursor, 10))
	}
	// One ownership check for the entire batch. Producers validate business rules
	// in memory; the transaction checks only its committed reference chain.
	const insert = `WITH batch(cursor,event_type,run_group_id,device_execution_id,node_attempt_id,node_id,occurred_at,payload_json) AS (VALUES %s)
INSERT INTO execution_events(cursor,event_type,run_group_id,device_execution_id,node_attempt_id,node_id,occurred_at,payload_json)
SELECT b.* FROM batch b
JOIN run_groups g ON g.id = b.run_group_id
LEFT JOIN device_executions d ON d.id = b.device_execution_id AND d.run_group_id = g.id
LEFT JOIN node_attempts a ON a.id = b.node_attempt_id AND a.device_execution_id = d.id AND a.node_id = b.node_id
WHERE (b.device_execution_id IS NULL OR d.id IS NOT NULL)
AND (b.node_attempt_id IS NULL OR a.id IS NOT NULL)`
	query := strings.Replace(insert, "%s", strings.Join(rows, ","), 1)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, storageError(ctx, "append execution events")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != int64(len(events)) {
		return nil, storageError(ctx, "verify execution event ownership")
	}
	if err := tx.UpdateExpected(ctx, `UPDATE event_stream_state SET last_committed_cursor = ?
WHERE id = 1 AND last_committed_cursor = ?`, state.Watermark+int64(len(events)), state.Watermark); err != nil {
		return nil, err
	}
	*tx.eventsAppended = true
	return cursors, nil
}

func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func eventState(ctx context.Context, tx Tx) (execution.EventStreamState, error) {
	var state execution.EventStreamState
	err := tx.QueryRowContext(ctx, "SELECT last_committed_cursor,replay_floor FROM event_stream_state WHERE id = 1").
		Scan(&state.Watermark, &state.Floor)
	if err != nil || state.Floor < 0 || state.Watermark < state.Floor {
		return execution.EventStreamState{}, storageError(ctx, "read execution event watermark")
	}
	return state, nil
}

func (s *ExecutionRepository) EventWatermark(ctx context.Context) (execution.EventStreamState, error) {
	var state execution.EventStreamState
	err := s.withReadTx(ctx, func(tx Tx) error {
		var err error
		state, err = eventState(ctx, tx)
		return err
	})
	return state, err
}

func (s *ExecutionRepository) ReplayEvents(ctx context.Context, after int64, limit int) (execution.EventPage, error) {
	if after < 0 || limit < 0 || limit > constants.MaxEventPageSize {
		return execution.EventPage{}, xerror.ErrExecutionInvalidInput
	}
	if limit == 0 {
		limit = constants.MaxEventPageSize
	}
	page := execution.EventPage{Events: make([]execution.Event, 0)}
	err := s.withReadTx(ctx, func(tx Tx) error {
		var err error
		page.State, err = eventState(ctx, tx)
		if err != nil {
			return err
		}
		if after < page.State.Floor {
			return xerror.ErrEventCursorExpired
		}
		if after > page.State.Watermark {
			return xerror.ErrEventCursorFuture
		}
		const query = `SELECT cursor,event_type,run_group_id,device_execution_id,node_attempt_id,node_id,occurred_at,payload_json
FROM execution_events WHERE cursor > ? ORDER BY cursor LIMIT ?`
		rows, err := tx.QueryContext(ctx, query, after, limit+1)
		if err != nil {
			return storageError(ctx, "replay execution events")
		}
		defer rows.Close()
		for rows.Next() {
			if len(page.Events) == limit {
				page.More = true
				break
			}
			var event execution.Event
			var cursor, occurred int64
			var device, attempt, node sql.NullString
			var payload []byte
			if err := rows.Scan(&cursor, &event.Type, &event.RunGroupID, &device, &attempt, &node, &occurred, &payload); err != nil {
				return storageError(ctx, "decode execution event")
			}
			event.Cursor, event.OccurredAt = strconv.FormatInt(cursor, 10), stamp(occurred)
			event.Payload = payload
			event.DeviceExecutionID, event.NodeAttemptID, event.NodeID = device.String, attempt.String, node.String
			page.Events = append(page.Events, event)
		}
		if rows.Err() != nil || rows.Close() != nil {
			return storageError(ctx, "read execution event page")
		}
		return nil
	})
	if err != nil {
		return execution.EventPage{}, err
	}
	return page, nil
}
