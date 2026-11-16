package repositories

import (
	"context"
	"database/sql"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
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
	return tx.updateExpected(ctx, "update_execution_state", query, args...)
}

func (tx Tx) updateExpected(ctx context.Context, operation, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return storageError(ctx, operation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected > 1 {
		return storageError(ctx, operation+"_rows_affected", err)
	}
	if affected == 0 {
		return xerror.ErrExecutionStaleState
	}
	return nil
}

func (s *ExecutionRepository) WithWriteTx(ctx context.Context, operation func(Tx) error) error {
	db := s.store.DB()
	if db == nil {
		return missingStore(ctx, "open_write_transaction")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(ctx, "open_write_transaction", err)
	}
	defer tx.Rollback()
	appended := false
	if err := operation(Tx{txQueries: tx, eventsAppended: &appended}); err != nil {
		return xerror.WithOperation("write_transaction", err)
	}
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("commit_write_transaction", err)
	}
	if err := tx.Commit(); err != nil {
		return &xerror.OperationError{Operation: "commit_write_transaction", Detail: sqlitestore.ErrorDetail(err), Err: xerror.ErrExecutionCommitUnknown}
	}
	if appended {
		s.notifyEvents()
	}
	return nil
}

func (s *ExecutionRepository) withReadTx(ctx context.Context, operation func(Tx) error) error {
	db := s.store.DB()
	if db == nil {
		return missingStore(ctx, "open_read_transaction")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storageError(ctx, "open_read_transaction", err)
	}
	defer tx.Rollback()
	return xerror.WithOperation("read_transaction", operation(Tx{txQueries: tx}))
}

func storageError(ctx context.Context, operation string, cause error) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation(operation, err)
	}
	return &xerror.OperationError{Operation: operation, Detail: sqlitestore.ErrorDetail(cause), Err: xerror.ErrExecutionPersistence}
}

func missingStore(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation(operation, err)
	}
	return &xerror.OperationError{Operation: operation, Detail: "execution store is closed", Err: xerror.ErrExecutionPersistence}
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
