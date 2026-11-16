package repositories

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// PruneEvents removes one bounded oldest prefix. Retain may be zero for a full
// purge; the watermark remains authoritative even when no event rows remain.
func (s *ExecutionRepository) PruneEvents(ctx context.Context, retain, batch int) (int, error) {
	if retain < 0 || batch <= 0 || batch > constants.EventPruneBatchSize {
		return 0, xerror.ErrExecutionInvalidInput
	}
	removed := 0
	err := s.WithWriteTx(ctx, func(tx Tx) error {
		const query = `DELETE FROM execution_events WHERE cursor IN (
SELECT cursor FROM execution_events WHERE cursor <= (
SELECT cursor FROM execution_events ORDER BY cursor DESC LIMIT 1 OFFSET ?)
ORDER BY cursor LIMIT ?) RETURNING cursor`
		rows, err := tx.QueryContext(ctx, query, retain, batch)
		if err != nil {
			return storageError(ctx, "prune_execution_events", err)
		}
		defer rows.Close()
		var floor int64
		for rows.Next() {
			var cursor int64
			if err := rows.Scan(&cursor); err != nil {
				return storageError(ctx, "read_pruned_event_cursor", err)
			}
			floor = max(floor, cursor)
			removed++
		}
		if err := rows.Err(); err != nil {
			return storageError(ctx, "finish_pruning_execution_events", err)
		}
		if err := rows.Close(); err != nil {
			return storageError(ctx, "finish_pruning_execution_events", err)
		}
		if removed == 0 {
			return nil
		}
		return tx.updateExpected(ctx, "advance_event_replay_floor", "UPDATE event_stream_state SET replay_floor = ? WHERE id = 1 AND replay_floor < ?", floor, floor)
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
