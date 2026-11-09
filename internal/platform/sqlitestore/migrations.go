package sqlitestore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

type AppliedMigration struct {
	Version  int64
	Filename string
	Duration time.Duration
}

type MigrationReport struct {
	Applied []AppliedMigration
	Version int64
}

// MigrateUp applies the embedded migrations to the local execution store.
func MigrateUp(ctx context.Context) (MigrationReport, error) {
	store := New()
	if err := store.Start(ctx); err != nil {
		return MigrationReport{}, err
	}
	return store.migrationReport, store.Shutdown(ctx)
}

func migrate(ctx context.Context, db *sql.DB, files fs.FS) (MigrationReport, error) {
	source, err := fs.Sub(files, "migrations")
	if err != nil {
		return MigrationReport{}, errors.New("open embedded execution migrations")
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, source, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return MigrationReport{}, errors.New("initialize execution migrations")
	}
	sources := provider.ListSources()
	versions := make([]int64, len(sources)+1)
	for index, source := range sources {
		versions[index+1] = source.Version
	}
	if err := checkHistory(ctx, db, versions); err != nil {
		return MigrationReport{}, err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return MigrationReport{}, ctx.Err()
		}
		return MigrationReport{}, errors.New("apply execution database migrations")
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil || version != versions[len(versions)-1] {
		return MigrationReport{}, errors.New("verify execution database migration version")
	}
	if err := verifySchema(ctx, db); err != nil {
		return MigrationReport{}, err
	}
	report := MigrationReport{Version: version, Applied: make([]AppliedMigration, 0, len(results))}
	for _, result := range results {
		report.Applied = append(report.Applied, AppliedMigration{Version: result.Source.Version,
			Filename: filepath.Base(result.Source.Path), Duration: result.Duration})
	}
	return report, nil
}

func checkHistory(ctx context.Context, db *sql.DB, versions []int64) error {
	var exists bool
	const ledger = "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='goose_db_version')"
	if err := db.QueryRowContext(ctx, ledger).Scan(&exists); err != nil {
		return errors.New("read execution database migration ledger")
	}
	if !exists {
		var populated bool
		const objects = "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%')"
		if err := db.QueryRowContext(ctx, objects).Scan(&populated); err != nil || populated {
			return errors.New("unversioned execution database is not empty")
		}
		return nil
	}
	rows, err := db.QueryContext(ctx, "SELECT version_id, is_applied FROM goose_db_version ORDER BY id")
	if err != nil {
		return errors.New("read applied execution migrations")
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var version int64
		var applied bool
		if err := rows.Scan(&version, &applied); err != nil {
			return errors.New("decode applied execution migration")
		}
		if index >= len(versions) || version != versions[index] || !applied {
			return fmt.Errorf("unsupported execution database migration history at version %d", version)
		}
		index++
	}
	if rows.Err() != nil || index == 0 {
		return errors.New("verify applied execution migrations")
	}
	return nil
}

func verifySchema(ctx context.Context, db *sql.DB) error {
	const objects = `SELECT count(*) FROM sqlite_schema WHERE
		(type='table' AND name IN ('run_groups','device_executions','node_attempts','execution_events','event_stream_state'))
		OR (type='index' AND name IN ('run_groups_one_active','node_attempts_one_running'))`
	var count int
	if err := db.QueryRowContext(ctx, objects).Scan(&count); err != nil || count != 7 {
		return errors.New("verify execution database schema")
	}
	var cursor, floor int64
	if err := db.QueryRowContext(ctx, "SELECT last_committed_cursor, replay_floor FROM event_stream_state WHERE id=1").
		Scan(&cursor, &floor); err != nil || floor < 0 || cursor < floor {
		return errors.New("verify execution database event stream state")
	}
	return nil
}
