package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestRecoveryFailureBlocksComposedAgentStartup(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"XDG_STATE_HOME", "XDG_RUNTIME_DIR", "LOCALAPPDATA"} {
		t.Setenv(name, root)
	}
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store.DB() != nil {
			_ = store.Shutdown(context.Background())
		}
	})
	admission := execution.Admission{
		RequestID: "10000000-0000-4000-8000-000000000001", RunGroupID: "10000000-0000-4000-8000-000000000002", Now: time.Now(),
		Devices: []execution.AdmissionDevice{{DeviceExecutionID: "10000000-0000-4000-8000-000000000003", ADBSerial: "emulator-5554", EntryNodeID: "start_01"}},
	}
	if _, err := repositories.NewExecutionRepository(store).Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	const trigger = `CREATE TRIGGER fail_recovery_event BEFORE INSERT ON execution_events
	BEGIN SELECT RAISE(ABORT, 'simulated recovery append failure'); END`
	if _, err := store.DB().ExecContext(t.Context(), trigger); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	defaultLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(defaultLogger) })
	// An invalid address makes the test fail if HTTP starts before recovery.
	cfg := config.Config{ShutdownTimeout: time.Second,
		HTTP: config.HTTPConfig{ListenAddress: "invalid-address"}, Logging: config.LoggingConfig{Level: "error", Format: "json"}}
	app, err := initializeApp(cfg, buildinfo.Info{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := app.Run(ctx); !errors.Is(err, xerror.ErrExecutionPersistence) || !strings.Contains(err.Error(), "start execution_recovery") {
		t.Fatalf("startup did not fail at recovery: %v", err)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Name() == constants.RuntimeConnectionFilename {
			t.Fatalf("failed startup published connection information: %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reopening verifies that the composed agent released the exclusive lock.
	store = sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repositories.NewExecutionRepository(store).ActiveSnapshot(t.Context())
	if err != nil || snapshot.Cursor != "2" || snapshot.RunGroup.Status != constants.ExecutionRunning || snapshot.RunGroup.Summary.Queued != 1 {
		t.Fatalf("failed recovery left partial state: %+v, %v", snapshot, err)
	}
}
