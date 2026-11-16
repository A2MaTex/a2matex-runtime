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
[ "$1" = -s ] && [ "$2" = '192.0.2.5:5555' ] &&
    [ "$3" = shell ] && [ "$4" = -T ] && [ "$5" = input ] || exit 1
shift 5
case "$1" in
    tap) [ "$#" -eq 3 ] && [ "$2" = 25 ] && [ "$3" = 90 ] ;;
    swipe) [ "$#" -eq 6 ] && [ "$2" = 540 ] && [ "$3" = 1535 ] &&
        [ "$4" = 540 ] && [ "$5" = 384 ] && [ "$6" = 300 ] ;;
    *) exit 1 ;;
esac
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
	if err := dispatcher.Dispatch(ctx, "192.0.2.5:5555", SwipeCommand{StartX: 540, StartY: 1535, EndX: 540, EndY: 384, DurationMS: 300}); err != nil {
		t.Fatal(err)
	}
}
