package adb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

func TestResolveRunsVersionWithE1(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "path with spaces", executableName())
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"[ \"$#\" -eq 1 ] && [ \"$1\" = version ] || exit 2\n" +
		"printf 'Android Debug Bridge version 1.0.41\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := NewResolver(config.ADBConfig{ExecutablePath: binary}).Resolve(t.Context())
	if err != nil || got != (Toolchain{binary, Configured, "1.0.41"}) {
		t.Fatalf("toolchain = %+v, %v", got, err)
	}
}
