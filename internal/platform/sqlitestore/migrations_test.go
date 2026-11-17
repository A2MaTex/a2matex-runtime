package sqlitestore

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestArtifactMigrationUpgradesExistingStore(t *testing.T) {
	ctx := t.Context()
	db, err := openDatabase(ctx, filepath.Join(t.TempDir(), databaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	source, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, source, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 20261105065253); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE event_stream_state SET last_committed_cursor = 17 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	report, err := migrate(ctx, db, migrations)
	if err != nil || len(report.Applied) != 1 || report.Applied[0].Filename != "20261117203000_screenshot_artifacts.sql" {
		t.Fatalf("artifact migration: %+v, %v", report, err)
	}
	var cursor int
	if err := db.QueryRowContext(ctx, "SELECT last_committed_cursor FROM event_stream_state WHERE id = 1").Scan(&cursor); err != nil || cursor != 17 {
		t.Fatalf("existing state: cursor %d, %v", cursor, err)
	}
}

func TestMigrateUpReportsAppliedAndPendingVersions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	files, err := migrations.ReadDir("migrations")
	if err != nil || len(files) == 0 {
		t.Fatalf("embedded migrations: %v", err)
	}
	first, err := MigrateUp(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Applied) != len(files) {
		t.Fatalf("first migration report: %+v", first)
	}
	for index, file := range files {
		prefix, _, _ := strings.Cut(file.Name(), "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		applied := first.Applied[index]
		if applied.Version != version || applied.Filename != file.Name() || applied.Duration < 0 {
			t.Fatalf("applied migration: %+v", applied)
		}
	}
	if first.Version != first.Applied[len(first.Applied)-1].Version {
		t.Fatalf("incorrect final version: %+v", first)
	}
	second, err := MigrateUp(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != first.Version || len(second.Applied) != 0 {
		t.Fatalf("repeated migration report: %+v", second)
	}
}
