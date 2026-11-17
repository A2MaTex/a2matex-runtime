package adb

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestCaptureScreenshotUsesBoundedBinaryADB(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "controlled adb")
	const script = `#!/bin/sh
if [ "$#" -eq 1 ] && [ "$1" = version ]; then
    printf 'Android Debug Bridge version 1.0.41\n'
    exit 0
fi
[ "$#" -eq 5 ] && [ "$*" = '-s 192.0.2.5:5555 exec-out screencap -p' ] || exit 99
case "$A2MATEX_SCREENSHOT_TEST" in
    binary) printf '\211PNG\r\n\032\n\000\377' ;;
    at-limit) exec head -c 16777216 /dev/zero ;;
    over-limit) exec head -c 16777217 /dev/zero ;;
    empty) exit 0 ;;
    failed) printf 'PRIVATE_TEST_STDERR' >&2; exit 7 ;;
    timeout) exec sleep 10 ;;
    *) exit 99 ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(config.ADBConfig{ExecutablePath: binary})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := resolver.Resolve(ctx); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(resolver)
	for _, test := range []struct {
		mode, reason string
		length       int
	}{
		{mode: "binary", length: 10},
		{mode: "at-limit", length: constants.MaxScreenshotBytes},
		{mode: "over-limit", reason: "capture_too_large"},
		{mode: "empty", reason: "empty_capture"},
		{mode: "failed", reason: "NONZERO_EXIT (exit code 7)"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("A2MATEX_SCREENSHOT_TEST", test.mode)
			attempt, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			data, err := dispatcher.CaptureScreenshot(attempt, "192.0.2.5:5555")
			if test.reason != "" {
				var failure xerror.CommandError
				var operation *xerror.OperationError
				if data != nil || !errors.As(err, &failure) || failure.Code != constants.CommandFailed ||
					failure.Retryable || !errors.As(err, &operation) || operation.Detail != test.reason {
					t.Fatalf("data length %d, error %v", len(data), err)
				}
				return
			}
			if err != nil || len(data) != test.length {
				t.Fatalf("data length %d, error %v", len(data), err)
			}
			if test.mode == "binary" && !bytes.Equal(data, []byte("\x89PNG\r\n\x1a\n\x00\xff")) {
				t.Fatal("capture altered the binary bytes")
			}
		})
	}

	t.Setenv("A2MATEX_SCREENSHOT_TEST", "timeout")
	deadline, stop := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stop()
	if data, err := dispatcher.CaptureScreenshot(deadline, "192.0.2.5:5555"); data != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline data length %d, error %v", len(data), err)
	}
}
