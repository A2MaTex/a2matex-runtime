package adb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestParseLauncherActivity(t *testing.T) {
	const pkg = "org.lineageos.jelly"
	const component = pkg + "/.MainActivity"
	for _, tc := range []struct {
		name, stdout, stderr string
		truncated            bool
		valid                bool
	}{
		{name: "Waydroid non-default launcher", stdout: "priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=false\n" + component + "\n", valid: true},
		{name: "component only", stdout: component, valid: true},
		{name: "fully qualified class", stdout: pkg + "/" + pkg + ".MainActivity\n", valid: true},
		{name: "CRLF", stdout: "\r\n" + component + "\r\n", valid: true},
		{name: "missing", stdout: "No activity found\n"},
		{name: "empty"},
		{name: "metadata only", stdout: "priority=0 preferredOrder=0\n"},
		{name: "foreign package", stdout: "com.example.other/.MainActivity\n"},
		{name: "duplicate", stdout: component + "\n" + component},
		{name: "missing class", stdout: pkg + "/"},
		{name: "shell syntax", stdout: component + ";echo injected"},
		{name: "extra output", stdout: component + "\nError: rejected"},
		{name: "stderr rejection", stdout: component, stderr: "Error: rejected"},
		{name: "truncated", stdout: component, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLauncherActivity(pkg, processrunner.Result{
				Stdout: []byte(tc.stdout), Stderr: []byte(tc.stderr), StdoutTruncated: tc.truncated,
			})
			if tc.valid {
				want := component
				if tc.name == "fully qualified class" {
					want = pkg + "/" + pkg + ".MainActivity"
				}
				if err != nil || got != want {
					t.Fatalf("component = %q, want %q, err = %v", got, want, err)
				}
				return
			}
			assertCommandError(t, err, constants.CommandFailed, false)
			var operation *xerror.OperationError
			if got != "" || !errors.As(err, &operation) || operation.Operation != "resolve_launcher_activity" || strings.Contains(err.Error(), pkg) {
				t.Fatalf("component = %q, err = %v", got, err)
			}
		})
	}
}

func TestLaunchAppResolutionFailureDoesNotLaunch(t *testing.T) {
	for _, output := range []string{"No activity found\n", "other.package/.MainActivity\n"} {
		t.Run(output, func(t *testing.T) {
			resolver := NewResolver(config.ADBConfig{})
			resolver.verified.Executable = "verified executable"
			var calls int
			resolver.run = func(context.Context, string, []string, time.Duration) processrunner.Result {
				calls++
				code := 0
				return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code, Stdout: []byte(output)}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := NewDispatcher(resolver).Dispatch(ctx, "emulator-5554", LaunchAppCommand{PackageName: "org.lineageos.jelly"})
			assertCommandError(t, err, constants.CommandFailed, false)
			if calls != 1 {
				t.Fatalf("got %d calls; resolution failure must not launch", calls)
			}
		})
	}
}

func TestLaunchAppSharesAttemptDeadline(t *testing.T) {
	for _, cancelAfterResolution := range []bool{false, true} {
		t.Run(map[bool]string{false: "remaining budget", true: "cancel before launch"}[cancelAfterResolution], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				resolver := NewResolver(config.ADBConfig{})
				resolver.verified.Executable = "verified executable"
				var calls int
				resolver.run = func(callCtx context.Context, _ string, _ []string, remaining time.Duration) processrunner.Result {
					calls++
					if callCtx != ctx {
						t.Fatal("launch replaced the attempt context")
					}
					code := 0
					if calls == 1 {
						time.Sleep(400 * time.Millisecond)
						if cancelAfterResolution {
							cancel()
						}
						return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code, Stdout: []byte("org.lineageos.jelly/.MainActivity\n")}
					}
					if calls != 2 || remaining != 600*time.Millisecond {
						t.Fatalf("calls = %d, launch budget = %v", calls, remaining)
					}
					time.Sleep(remaining)
					return processrunner.Result{Outcome: processrunner.TimedOut}
				}
				err := NewDispatcher(resolver).Dispatch(ctx, "emulator-5554", LaunchAppCommand{PackageName: "org.lineageos.jelly"})
				if cancelAfterResolution {
					if !errors.Is(err, context.Canceled) || calls != 1 {
						t.Fatalf("calls = %d, err = %v", calls, err)
					}
				} else if !errors.Is(err, context.DeadlineExceeded) || calls != 2 {
					t.Fatalf("calls = %d, err = %v", calls, err)
				}
			})
		})
	}
}

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
	const component = pkg + "/.MainActivity$Launcher"
	resolver := NewResolver(config.ADBConfig{})
	resolver.verified.Executable = "verified executable"
	var calls int
	resolver.run = func(_ context.Context, executable string, args []string, _ time.Duration) processrunner.Result {
		calls++
		want := []string{"-s", serial, "shell", "-T", "cmd", "package", "resolve-activity", "--brief", "--user", "current",
			"-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", "-p", pkg}
		stdout := "priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=false\n" + component + "\n"
		if calls == 2 {
			want = []string{"-s", serial, "shell", "-T", "am", "start", "-W", "--user", "current",
				"-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", "-n", "'" + component + "'"}
			stdout = "Status: ok\nActivity: " + component + "\nComplete\n"
		}
		if executable != resolver.verified.Executable || !slices.Equal(args, want) {
			t.Fatalf("executable = %q, args = %v, want %v", executable, args, want)
		}
		code := 0
		return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code, Stdout: []byte(stdout)}
	}
	dispatcher := NewDispatcher(resolver)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	assertCommandError(t, dispatcher.Dispatch(ctx, serial, LaunchAppCommand{PackageName: pkg + ";exit"}), constants.CommandInputUnsupported, false)
	if calls != 0 {
		t.Fatal("invalid package reached the process runner")
	}
	if err := dispatcher.Dispatch(ctx, serial, LaunchAppCommand{PackageName: pkg}); err != nil || calls != 2 {
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
