package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The Python harness follows the existing managed-parent acceptance pattern.
// It starts the actual built agent, not an alternative test router or registry.
func TestG22CombinedEngineAcceptance(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("G22 Linux process acceptance requires Python 3; no native acceptance was run")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "a2matex-runtime")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags",
		"-X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.Version=0.1.0", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build G22 agent: %v\n%s", err, output)
	}
	check := exec.CommandContext(ctx, python, "testdata/g22_acceptance.py", binary, "../../examples/g22/compile-workflow.json")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("G22 process acceptance: %v\n%s", err, output)
	} else {
		t.Logf("%s", output)
	}
}
