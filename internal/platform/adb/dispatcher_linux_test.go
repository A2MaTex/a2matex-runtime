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
	const sample = "-A2MaTeX  'demo' \"double\" & $5 \\ \x60tick\x60 99%  "
	script := `#!/bin/sh
if [ "$#" -eq 1 ] && [ "$1" = version ]; then
    printf 'Android Debug Bridge version 1.0.41\n'
    exit 0
fi
[ "$1" = -s ] && [ "$2" = '192.0.2.5:5555' ] && [ "$3" = shell ] && [ "$4" = -T ] || exit 1
if [ "$5" = am ]; then
    shift 4
    [ "$#" -eq 11 ] &&
        [ "$*" = 'am start -W --user current -a android.intent.action.MAIN -c android.intent.category.LAUNCHER -p com.example.app' ] || exit 1
    printf 'Status: ok\nActivity: com.example.app/.MainActivity\nComplete\n'
    exit 0
fi
[ "$5" = input ] || exit 1
shift 5
case "$1" in
    tap) [ "$#" -eq 3 ] && [ "$2" = 25 ] && [ "$3" = 90 ] ;;
    swipe) [ "$#" -eq 6 ] &&
        { [ "$*" = 'swipe 540 1535 540 384 300' ] || [ "$*" = 'swipe 25 90 25 90 800' ]; } ;;
    keyevent) [ "$#" -eq 2 ] && { [ "$2" = KEYCODE_BACK ] || [ "$2" = KEYCODE_HOME ]; } ;;
    text)
        [ "$#" -eq 2 ] || exit 1
        # ADB joins these arguments before the Android shell parses them.
        expected=$(cat <<'EOF'
` + sample + `
EOF
)
        decoded=$(sh -c "set -- $2; [ \"\$#\" -eq 1 ] && printf '%s' \"\$1\"") || exit 1
        [ "$decoded" = "$expected" ] ;;
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
	if err := dispatcher.Dispatch(ctx, "192.0.2.5:5555", LongPressCommand{X: 25, Y: 90, DurationMS: 800}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []DeviceCommand{PressBackCommand{}, PressHomeCommand{}} {
		if err := dispatcher.Dispatch(ctx, "192.0.2.5:5555", command); err != nil {
			t.Fatal(err)
		}
	}
	if err := dispatcher.Dispatch(ctx, "192.0.2.5:5555", LaunchAppCommand{PackageName: "com.example.app"}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(ctx, "192.0.2.5:5555", InputTextCommand{Text: sample}); err != nil {
		t.Fatal(err)
	}
}
