package adb

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func TestFindSelectorPoint(t *testing.T) {
	const id = "com.example:id/confirm"
	const node = `<node resource-id="com.example:id/confirm" bounds="[-40,20][120,100]"/>`
	for _, test := range []struct {
		name, body string
		wrap       bool
		success    bool
	}{
		{"one clipped match", node, true, true},
		{"case sensitive", `<node resource-id="com.example:id/Confirm" bounds="[0,0][2,2]"/>`, true, false},
		{"duplicate", node + node, true, false},
		{"duplicate with hidden", node + `<node resource-id="com.example:id/confirm" visible-to-user="false"/>`, true, false},
		{"hidden ancestor", `<node visible-to-user="false">` + node + `</node>`, true, false},
		{"outside display", `<node resource-id="com.example:id/confirm" bounds="[-40,20][-1,100]"/>`, true, false},
		{"malformed bounds", `<node resource-id="com.example:id/confirm" bounds="[0,0][1,x]"/>`, true, false},
		{"empty bounds", `<node resource-id="com.example:id/confirm" bounds="[0,0][0,2]"/>`, true, false},
		{"malformed XML", `<hierarchy>` + node + `<node>`, false, false},
		{"two documents", `<hierarchy>` + node + `</hierarchy><hierarchy/>`, false, false},
		{"directive", `<!DOCTYPE hierarchy><hierarchy>` + node + `</hierarchy>`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := test.body
			if test.wrap {
				data = `<?xml version="1.0" encoding="UTF-8" standalone="yes" ?><hierarchy rotation="0">` + data + `</hierarchy>`
			}
			point, err := findSelectorPoint(t.Context(), []byte(data), id, DisplaySize{1080, 1920})
			if test.success {
				if err != nil || point != (TapCommand{60, 60}) {
					t.Fatalf("point = %+v, error = %v", point, err)
				}
			} else {
				assertCommandError(t, err, constants.CommandFailed, false)
			}
		})
	}
	assertCommandError(t, parseHierarchyTestError(t.Context(), bytes.Repeat([]byte(" "), constants.AndroidHierarchyMaxBytes+1)), constants.CommandFailed, false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := parseHierarchyTestError(ctx, []byte("<hierarchy/>")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func parseHierarchyTestError(ctx context.Context, data []byte) error {
	_, err := findSelectorPoint(ctx, data, "com.example:id/confirm", DisplaySize{1080, 1920})
	return err
}

func TestHierarchyReadSkipsCleanupAfterCancellation(t *testing.T) {
	resolver := NewResolver(config.ADBConfig{})
	resolver.verified.Executable = "verified executable"
	ctx, cancel := context.WithTimeout(t.Context(), constants.ADBReadinessTimeout)
	defer cancel()
	var calls int
	resolver.run = func(_ context.Context, _ string, args []string, _ time.Duration) processrunner.Result {
		calls++
		code := 0
		if calls == 2 {
			cancel()
			return processrunner.Result{Outcome: processrunner.Cancelled}
		}
		return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code}
	}
	_, err := NewDispatcher(resolver).readHierarchy(ctx, "emulator-5554")
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("error = %v, invocations = %d, want cancelled after two invocations", err, calls)
	}
}
