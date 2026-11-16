package adb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestParseLaunchResult(t *testing.T) {
	const pkg = "com.example.private"
	const success = "Status: ok\nActivity: " + pkg + "/.MainActivity\nComplete\n"
	const permissionActivity = "Activity: com.android.permissioncontroller/.permission.ui.GrantPermissionsActivity\n"
	const failure = constants.CommandFailed
	for _, tc := range []struct {
		name, stdout, stderr, code       string
		messageContains                  string
		stdoutTruncated, stderrTruncated bool
	}{
		{name: "cold launch", stdout: "Starting: Intent { pkg=" + pkg + " }\n" + success},
		{name: "existing task", stdout: "Warning: Activity not started, its current task has been brought to the front\n" + success},
		{name: "top instance", stdout: "Warning: Activity not started, intent has been delivered to currently running top-most instance.\n" + success},
		{name: "no optional activity and CRLF", stdout: " Status: ok\r\nWaitTime: 5\r\n Complete \r\n\r\n"},
		{name: "Android permission prompt on cold launch", stdout: "Status: ok\n" + permissionActivity + "Complete\n"},
		{name: "fully qualified Android permission prompt", stdout: "Status: ok\nActivity: com.android.permissioncontroller/com.android.permissioncontroller.permission.ui.GrantPermissionsActivity\nComplete\n"},
		{name: "permission prompt without completion", stdout: "Status: ok\n" + permissionActivity, code: failure},
		{name: "explicit rejection with permission prompt", stdout: "Status: ok\n" + permissionActivity + "Complete\n", stderr: "Error: launch rejected\n", code: failure},
		{name: "permission prompt with Android timeout", stdout: "Status: timeout\n" + permissionActivity + "Complete\n", code: constants.CommandTimedOut},
		{name: "Android timeout", stdout: "Status: timeout\nComplete\n", code: constants.CommandTimedOut},
		{name: "zero-exit unresolved intent", stdout: "Error: Activity not started, unable to resolve " + pkg, code: failure, messageContains: "Check the package name and confirm the app is installed"},
		{name: "stderr rejection over success", stdout: success, stderr: "Error type 3\n", code: failure},
		{name: "ignored app switch", stdout: "Warning: Activity not started because the current activity is being kept for the user.\n" + success, code: failure},
		{name: "intent returned", stdout: "Warning: Activity not started because intent should be handled by the caller\n" + success, code: failure},
		{name: "missing status", stdout: "Complete\n", code: failure},
		{name: "missing completion", stdout: "Status: ok\n", code: failure},
		{name: "duplicate completion", stdout: success + "Complete\n", code: failure},
		{name: "out of order", stdout: "Complete\nStatus: ok\n", code: failure},
		{name: "conflicting statuses", stdout: "Status: timeout\n" + success, code: failure},
		{name: "foreign activity", stdout: "Status: ok\nActivity: android/.ResolverActivity\nComplete\n", code: failure},
		{name: "other permission controller activity", stdout: "Status: ok\nActivity: com.android.permissioncontroller/.permission.ui.ManagePermissionsActivity\nComplete\n", code: failure},
		{name: "similar class from other package", stdout: "Status: ok\nActivity: com.example.other/.permission.ui.GrantPermissionsActivity\nComplete\n", code: failure},
		{name: "missing class", stdout: "Status: ok\nActivity: " + pkg + "/\nComplete\n", code: failure},
		{name: "duplicate activity", stdout: "Activity: " + pkg + "/.MainActivity\n" + success, code: failure},
		{name: "truncated stdout", stdout: success, stdoutTruncated: true, code: failure},
		{name: "truncated stderr", stdout: success, stderrTruncated: true, code: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := parseLaunchResult(pkg, processrunner.Result{
				Stdout: []byte(tc.stdout), Stderr: []byte(tc.stderr),
				StdoutTruncated: tc.stdoutTruncated, StderrTruncated: tc.stderrTruncated,
			})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCommandError(t, err, tc.code, false)
			var commandErr xerror.CommandError
			if tc.messageContains != "" && (!errors.As(err, &commandErr) || !strings.Contains(commandErr.Message, tc.messageContains)) {
				t.Fatalf("error = %v, want guidance containing %q", err, tc.messageContains)
			}
			var operation *xerror.OperationError
			if !errors.As(err, &operation) || operation.Operation != "parse_launch_result" || strings.Contains(err.Error(), pkg) {
				t.Fatalf("unexpected launch error: %v", err)
			}
		})
	}
}

func TestLaunchAppDispatchesExactCommandOnce(t *testing.T) {
	const serial = "192.0.2.5:5555"
	const pkg = "com.Example.app"
	resolver := NewResolver(config.ADBConfig{})
	resolver.verified.Executable = "verified executable"
	var calls int
	resolver.run = func(_ context.Context, executable string, args []string, _ time.Duration) processrunner.Result {
		calls++
		want := []string{"-s", serial, "shell", "-T", "am", "start", "-W", "--user", "current",
			"-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", "-p", pkg}
		if executable != resolver.verified.Executable || !slices.Equal(args, want) {
			t.Fatalf("executable = %q, args = %v, want %v", executable, args, want)
		}
		code := 0
		return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code, Stdout: []byte("Status: ok\nComplete\n")}
	}
	dispatcher := NewDispatcher(resolver)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	assertCommandError(t, dispatcher.Dispatch(ctx, serial, LaunchAppCommand{PackageName: pkg + ";exit"}), constants.CommandInputUnsupported, false)
	if calls != 0 {
		t.Fatal("invalid package reached the process runner")
	}
	if err := dispatcher.Dispatch(ctx, serial, LaunchAppCommand{PackageName: pkg}); err != nil || calls != 1 {
		t.Fatalf("error = %v, calls = %d, want success after one launch", err, calls)
	}
}

func TestLaunchAppProcessFailureTakesPrecedenceOverOutput(t *testing.T) {
	resolver := NewResolver(config.ADBConfig{})
	resolver.verified.Executable = "verified executable"
	calls := 0
	resolver.run = func(context.Context, string, []string, time.Duration) processrunner.Result {
		calls++
		return processrunner.Result{Outcome: processrunner.StartFailed, Stdout: []byte("Status: ok\nComplete\n")}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := NewDispatcher(resolver).Dispatch(ctx, "emulator-5554", LaunchAppCommand{PackageName: "a.b"})
	assertCommandError(t, err, constants.CommandStartFailed, true)
	if calls != 1 {
		t.Fatalf("got %d calls, want one attempt", calls)
	}
}
