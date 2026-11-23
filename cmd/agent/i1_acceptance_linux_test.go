package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

func TestI1AttemptHistoryAgentAcceptance(t *testing.T) {
	retainedAgentReadAcceptance(t, "testdata/i1_acceptance.py")
}

func TestI4EventStreamAgentAcceptance(t *testing.T) {
	retainedAgentReadAcceptance(t, "testdata/i4_acceptance.py")
}

func retainedAgentReadAcceptance(t *testing.T, script string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("retained-read Linux managed-process acceptance requires Python 3")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	for _, key := range []string{"XDG_STATE_HOME", "XDG_RUNTIME_DIR", "LOCALAPPDATA"} {
		t.Setenv(key, root)
	}
	store := sqlitestore.New()
	if err := store.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store.DB() != nil {
			if err := store.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	repo := repositories.NewExecutionRepository(store)
	group, device := uuid.NewString(), uuid.NewString()
	if _, err := repo.Admit(ctx, execution.Admission{RunGroupID: group, RequestID: uuid.NewString(), Now: time.Now().UTC(),
		Devices: []execution.AdmissionDevice{{DeviceExecutionID: device, ADBSerial: "i1-no-android", EntryNodeID: "start"}}}); err != nil {
		t.Fatal(err)
	}
	checkpoints := services.NewCheckpointService(repo, nil)
	for i, node := range []string{"start", "wait", "end"} {
		state := constants.ExecutionRunning
		if i == 0 {
			state = constants.ExecutionQueued
		}
		ref := execution.AttemptRef{RunGroupID: group, DeviceExecutionID: device, AttemptID: uuid.NewString(), NodeID: node}
		if _, err := checkpoints.BeginAttempt(ctx, execution.BeginAttemptRequest{AttemptRef: ref, ExpectedDeviceStatus: state, AttemptNumber: 1, MaxAttempts: 1, Now: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		next := ""
		if i < 2 {
			next = []string{"wait", "end"}[i]
		}
		output := json.RawMessage(`{}`)
		if node == "wait" {
			output = json.RawMessage(`{"durationMs":0}`)
		}
		if _, err := checkpoints.FinishAttempt(ctx, execution.FinishAttemptRequest{AttemptRef: ref, Status: constants.ExecutionSucceeded, NextNodeID: next, Output: output, Now: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repo.Attempts(ctx, device, 0, constants.MaxAttemptPageSize)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "expected.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ReplayEvents(ctx, 0, constants.MaxEventPageSize)
	if err != nil {
		t.Fatal(err)
	}
	eventData, err := json.Marshal(events.Events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "expected-events.json"), eventData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkpoints.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "a2matex-runtime")
	if output, err := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build retained-read agent: %v\n%s", err, output)
	}
	check := exec.CommandContext(ctx, python, script, binary, root, device)
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("retained-agent acceptance (%s): %v\n%s", script, err, output)
	} else {
		t.Logf("%s", output)
	}
}
