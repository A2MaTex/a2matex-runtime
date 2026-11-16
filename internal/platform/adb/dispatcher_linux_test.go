package adb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

func TestDispatchInvokesResolvedADBWithE1(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "controlled adb")
	const script = `#!/bin/sh
if [ "$#" -eq 1 ] && [ "$1" = version ]; then
    printf 'Android Debug Bridge version 1.0.41\n'
    exit 0
fi
[ "$#" -eq 8 ] && [ "$1" = -s ] && [ "$2" = '192.0.2.5:5555' ] &&
    [ "$3" = shell ] && [ "$4" = -T ] && [ "$5" = input ] &&
    [ "$6" = tap ] && [ "$7" = 25 ] && [ "$8" = 90 ]
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dispatcher := NewDispatcher(NewResolver(config.ADBConfig{ExecutablePath: binary}))
	if err := dispatcher.Dispatch(ctx, "192.0.2.5:5555", TapCommand{X: 25, Y: 90}); err != nil {
		t.Fatal(err)
	}
}
