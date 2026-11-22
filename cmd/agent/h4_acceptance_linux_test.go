package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestH4MultiDeviceAgentAcceptance(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("H4 Linux process acceptance requires Python 3")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "a2matex-runtime")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags",
		"-X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.Version=0.1.0", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build H4 agent: %v\n%s", err, output)
	}
	check := exec.CommandContext(ctx, python, "testdata/h4_acceptance.py", binary)
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("H4 process acceptance: %v\n%s", err, output)
	} else {
		t.Logf("%s", output)
	}
}
