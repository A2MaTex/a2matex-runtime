package repositories

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func TestScreenshotSurvivesAbruptRuntimeTermination(t *testing.T) {
	const childFlag = "A2MATEX_TEST_ARTIFACT_CRASH"
	if os.Getenv(childFlag) == "1" {
		runArtifactCrashChild(t)
		return
	}

	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestScreenshotSurvivesAbruptRuntimeTermination$")
	cmd.Env = append(cmd.Environ(), childFlag+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, readErr := reader.ReadString('\n')
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	var exit *exec.ExitError
	fields := strings.Fields(line)
	if readErr != nil || killErr != nil || !errors.As(waitErr, &exit) || len(fields) != 5 || fields[0] != "STAGED" {
		t.Fatalf("crash helper: %q, read=%v kill=%v wait=%v, %s", line, readErr, killErr, waitErr, stderr.String())
	}

	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown(context.Background())
	repo := NewExecutionRepository(store)
	recovery := services.NewExecutionRecovery(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := recovery.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer recovery.Shutdown(context.Background())
	artifacts := startArtifactService(t, repo)
	defer artifacts.Shutdown(context.Background())

	_, file, err := artifacts.Open(t.Context(), fields[1])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(raw, testPNG(t)) {
		t.Fatalf("committed screenshot changed after restart: %v", err)
	}
	directory := filepath.Join(store.StateDirectory(), "artifacts")
	for _, name := range fields[2:4] {
		if _, err := os.Stat(filepath.Join(directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan survived restart: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, fields[4])); err != nil {
		t.Fatalf("unknown file was removed: %v", err)
	}
}

func runArtifactCrashChild(t *testing.T) {
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	repo := NewExecutionRepository(store)
	artifacts := startArtifactService(t, repo)
	checkpoint := services.NewCheckpointService(repo, artifacts)
	admission := testAdmission(0)
	admission.Devices = admission.Devices[:1]
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	begin := beginFixture(admission, 0, 140)
	begin.Now = time.Now().Add(-time.Second)
	requireBegin(t, checkpoint, begin, 1, "4")
	finish := stagedFinish(t, artifacts, begin, testPNG(t))
	finish.NextNodeID = "capture_again"
	if result, err := checkpoint.FinishAttempt(t.Context(), finish); err != nil || result.Disposition != constants.CheckpointCommitted {
		t.Fatalf("screenshot did not commit: %+v, %v", result, err)
	}
	second := beginFixture(admission, 0, 141)
	second.NodeID, second.ExpectedDeviceStatus, second.Now = finish.NextNodeID, constants.ExecutionRunning, time.Now()
	if result, err := checkpoint.BeginAttempt(t.Context(), second); err != nil || result.Disposition != constants.CheckpointCommitted {
		t.Fatalf("second attempt: %+v, %v", result, err)
	}
	orphan := stagedFinish(t, artifacts, second, testPNG(t))
	temp := ".stage-" + testExecutionID(142) + ".tmp"
	const unknown = "unrecognized.txt"
	for _, name := range []string{temp, unknown} {
		if err := os.WriteFile(filepath.Join(store.StateDirectory(), "artifacts", name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Printf("STAGED %s %s %s %s\n", finish.Artifact.ID, orphan.Artifact.FileKey, temp, unknown)
	time.Sleep(30 * time.Second)
}
