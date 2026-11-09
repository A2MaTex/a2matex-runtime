package sqlitestore

import (
	"strconv"
	"strings"
	"testing"
)

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
