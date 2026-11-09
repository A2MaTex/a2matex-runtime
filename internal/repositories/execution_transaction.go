package repositories

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type txQueries interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Tx gives persistence callers a shared transaction without a commit method.
type Tx struct {
	txQueries
	eventsAppended *bool
}

// UpdateExpected applies a single-row conditional update inside this transaction.
func (tx Tx) UpdateExpected(ctx context.Context, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return storageError(ctx, "update execution state")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected > 1 {
		return storageError(ctx, "verify execution state update")
	}
	if affected == 0 {
		return xerror.ErrExecutionStaleState
	}
	return nil
}

func (s *ExecutionRepository) WithWriteTx(ctx context.Context, operation func(Tx) error) error {
	db := s.store.DB()
	if db == nil {
		return storageError(ctx, "open write transaction")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(ctx, "open write transaction")
	}
	defer tx.Rollback()
	appended := false
	if err := operation(Tx{txQueries: tx, eventsAppended: &appended}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return xerror.ErrExecutionCommitUnknown
	}
	if appended {
		s.notifyEvents()
	}
	return nil
}

func (s *ExecutionRepository) withReadTx(ctx context.Context, operation func(Tx) error) error {
	db := s.store.DB()
	if db == nil {
		return storageError(ctx, "open read transaction")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storageError(ctx, "open read transaction")
	}
	defer tx.Rollback()
	return operation(Tx{txQueries: tx})
}

func storageError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", xerror.ErrExecutionPersistence, operation)
}

func stamp(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(constants.ExecutionTimestampLayout)
}

func nullableStamp(ms sql.NullInt64) *string {
	if !ms.Valid {
		return nil
	}
	value := stamp(ms.Int64)
	return &value
}
