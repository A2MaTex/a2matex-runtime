package sqlitestore

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStoreMigratesReopensAndRejectsSecondOwner(t *testing.T) {
	root := t.TempDir()
	first := New()
	if err := first.startAt(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.release() })
	second := New()
	if err := second.startAt(t.Context(), root); err == nil || !strings.Contains(err.Error(), "lock unavailable") {
		_ = second.release()
		t.Fatalf("second start: %v", err)
	}
	if _, err := first.db.ExecContext(t.Context(),
		"INSERT INTO run_groups VALUES ('g1','request1','RUNNING',1,NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := first.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := second.startAt(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.release() })
	var status string
	if err := second.db.QueryRowContext(t.Context(), "SELECT status FROM run_groups WHERE id='g1'").Scan(&status); err != nil || status != "RUNNING" {
		t.Fatalf("reopen status = %q, err = %v", status, err)
	}
	var ledgerRows int
	if err := second.db.QueryRowContext(t.Context(), "SELECT count(*) FROM goose_db_version").Scan(&ledgerRows); err != nil || ledgerRows != 2 {
		t.Fatalf("ledger rows = %d, err = %v", ledgerRows, err)
	}
}

func TestMigrationRejectsUnsupportedHistory(t *testing.T) {
	root := t.TempDir()
	first := New()
	if err := first.startAt(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.release() })
	if _, err := first.db.ExecContext(t.Context(),
		"INSERT INTO goose_db_version(version_id,is_applied) VALUES (99,1)"); err != nil {
		t.Fatal(err)
	}
	if err := first.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := New()
	if err := second.startAt(t.Context(), root); err == nil || !strings.Contains(err.Error(), "version 99") {
		_ = second.release()
		t.Fatalf("unexpected startup result: %v", err)
	}
}

func TestMigrationRollsBackSchemaAndVersion(t *testing.T) {
	db, err := openDatabase(t.Context(), filepath.Join(t.TempDir(), databaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files := fstest.MapFS{
		"migrations/00001_bad.sql": {Data: []byte("-- +goose Up\nCREATE TABLE partial (id INTEGER);\nINVALID SQL;")},
	}
	if _, err := migrate(t.Context(), db, files); err == nil {
		t.Fatal("expected migration failure")
	}
	var partial, version int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name='partial'").Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("partial table count = %d, err = %v", partial, err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT max(version_id) FROM goose_db_version").Scan(&version); err != nil || version != 0 {
		t.Fatalf("version = %d, err = %v", version, err)
	}
}

func TestExecutionSchemaConstraints(t *testing.T) {
	store := New()
	if err := store.startAt(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer store.release()
	for _, statement := range []string{
		"INSERT INTO run_groups VALUES ('g1','r1','RUNNING',1,NULL)",
		"INSERT INTO device_executions VALUES ('d1','g1','device',0,'QUEUED','start',1,NULL,NULL,NULL)",
		"INSERT INTO node_attempts VALUES ('a1','d1',1,'start',1,'RUNNING',1,NULL,NULL,NULL)",
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	for name, statement := range map[string]string{
		"duplicate request":      "INSERT INTO run_groups VALUES ('g2','r1','SUCCEEDED',1,2)",
		"second active run":      "INSERT INTO run_groups VALUES ('g2','r2','PAUSED',1,NULL)",
		"duplicate serial":       "INSERT INTO device_executions VALUES ('d2','g1','device',1,'QUEUED','start',1,NULL,NULL,NULL)",
		"missing parent":         "INSERT INTO device_executions VALUES ('d2','missing','another',1,'QUEUED','start',1,NULL,NULL,NULL)",
		"second running attempt": "INSERT INTO node_attempts VALUES ('a2','d1',2,'tap',1,'RUNNING',2,NULL,NULL,NULL)",
		"success without output": "UPDATE node_attempts SET status='SUCCEEDED',finished_at=2 WHERE id='a1'",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.db.ExecContext(t.Context(), statement); err == nil {
				t.Fatal("expected constraint failure")
			}
		})
	}
}
