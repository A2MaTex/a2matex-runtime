package adb

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

func TestDiscoverRunsE1WithE2Selection(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "adb with spaces")
	const script = `#!/bin/sh
case "$#:$1:$2" in
  1:version:) printf 'Android Debug Bridge version 1.0.41\n' ;;
  2:devices:-l) printf 'List of devices attached\nphone\tdevice model:Sample\n' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := NewResolver(config.ADBConfig{ExecutablePath: binary}).Discover(t.Context())
	if err != nil || !slices.Equal(got, []Endpoint{{"phone", Connected}}) {
		t.Fatalf("endpoints = %v, %v", got, err)
	}
}
