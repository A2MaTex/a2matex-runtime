package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func TestJ10ProductSessionAgentAcceptance(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("J10 managed-process acceptance requires Python 3")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "a2matex-runtime")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", "-X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.Version=0.1.0", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build J10 agent: %v\n%s", err, output)
	}
	trust, err := json.Marshal(productTestTrust(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	seedProductHistory(t)
	check := exec.CommandContext(ctx, python, "testdata/j10_acceptance.py", binary, root)
	check.Env = append(os.Environ(), "A2MATEX_RUNTIME_PRODUCTION_VERIFICATION_KEYS="+string(trust))
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("J10 process acceptance: %v\n%s", err, output)
	} else {
		t.Logf("%s", output)
	}
}

func seedProductHistory(t *testing.T) {
	t.Helper()
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repo := repositories.NewExecutionRepository(store)
	group, device := uuid.NewString(), uuid.NewString()
	if _, err := repo.Admit(t.Context(), execution.Admission{RunGroupID: group, RequestID: uuid.NewString(), Now: time.Now(),
		Devices: []execution.AdmissionDevice{{DeviceExecutionID: device, ADBSerial: "retained-no-android", EntryNodeID: "end"}}}); err != nil {
		t.Fatal(err)
	}
	cp := services.NewCheckpointService(repo, nil)
	defer func() { _ = cp.Shutdown(context.Background()) }()
	ref := execution.AttemptRef{RunGroupID: group, DeviceExecutionID: device, NodeID: "end", AttemptID: uuid.NewString()}
	if _, err := cp.BeginAttempt(t.Context(), execution.BeginAttemptRequest{AttemptRef: ref, ExpectedDeviceStatus: constants.ExecutionQueued, AttemptNumber: 1, MaxAttempts: 1, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.FinishAttempt(t.Context(), execution.FinishAttemptRequest{AttemptRef: ref, NodeType: constants.EndNodeType, Status: constants.ExecutionSucceeded, Output: json.RawMessage(`{}`), Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
}
