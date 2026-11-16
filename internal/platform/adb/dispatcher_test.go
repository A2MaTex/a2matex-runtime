package adb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestSwipeBudgetCheckedAfterToolchainResolution(t *testing.T) {
	for _, test := range []struct {
		name     string
		budget   time.Duration
		wantExit bool
	}{
		{"still enough time", 501 * time.Millisecond, true},
		{"duration equals remaining budget", 500 * time.Millisecond, false},
		{"duration exceeds remaining budget", 450 * time.Millisecond, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), executableName())
			if err := os.WriteFile(binary, nil, 0700); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				resolver := NewResolver(config.ADBConfig{ExecutablePath: binary})
				versionCalls, swipes := 0, 0
				resolver.run = func(_ context.Context, _ string, args []string, _ time.Duration) processrunner.Result {
					code := 0
					if reflect.DeepEqual(args, []string{"version"}) {
						versionCalls++
						time.Sleep(200 * time.Millisecond)
						return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code, Stdout: []byte("Android Debug Bridge version 1.0.41\n")}
					}
					swipes++
					return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code}
				}
				ctx, cancel := context.WithTimeout(t.Context(), test.budget)
				defer cancel()
				err := NewDispatcher(resolver).Dispatch(ctx, "emulator-5554", SwipeCommand{DurationMS: 300})
				if versionCalls != 1 {
					t.Fatalf("version probes = %d, want 1", versionCalls)
				}
				if test.wantExit {
					if err != nil || swipes != 1 {
						t.Fatalf("error = %v, swipes = %d, want one successful swipe", err, swipes)
					}
				} else if !errors.Is(err, context.DeadlineExceeded) || swipes != 0 {
					t.Fatalf("error = %v, swipes = %d, want deadline failure before dispatch", err, swipes)
				}
			})
		})
	}
}

func TestDispatchRejectsInvalidSwipeBeforeResolving(t *testing.T) {
	resolver := NewResolver(config.ADBConfig{})
	resolver.executable = func() (string, error) {
		t.Fatal("invalid command reached ADB resolution")
		return "", nil
	}
	dispatcher := NewDispatcher(resolver)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, command := range []SwipeCommand{
		{DurationMS: 0},
		{DurationMS: constants.MaxAndroidSwipeDurationMS + 1},
		{EndX: -1, DurationMS: 1},
	} {
		assertCommandError(t, dispatcher.Dispatch(ctx, "emulator-5554", command), constants.CommandInputUnsupported, false)
	}
}

func TestDispatchBindsExactSerial(t *testing.T) {
	resolver := NewResolver(config.ADBConfig{})
	resolver.verified.Executable = "verified executable"
	var calls [][]string
	resolver.run = func(ctx context.Context, executable string, args []string, budget time.Duration) processrunner.Result {
		deadline, _ := ctx.Deadline()
		if executable != resolver.verified.Executable || budget <= 0 || budget > time.Second || deadline.IsZero() {
			t.Fatalf("unexpected invocation: %q, budget %v, deadline %v", executable, budget, deadline)
		}
		calls = append(calls, args)
		exitCode := 0
		return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &exitCode}
	}
	dispatcher := NewDispatcher(resolver)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, serial := range []string{"emulator-5554", "[2001:db8::1]:5555"} {
		if err := dispatcher.Dispatch(ctx, serial, TapCommand{X: 0, Y: constants.MaxAndroidPixelCoordinate}); err != nil {
			t.Fatal(err)
		}
	}
	want := [][]string{
		{"-s", "emulator-5554", "shell", "-T", "input", "tap", "0", "100000"},
		{"-s", "[2001:db8::1]:5555", "shell", "-T", "input", "tap", "0", "100000"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

type unknownCommand struct{}

func (unknownCommand) deviceCommand() {}

func TestDispatchRejectsBeforeRunningADB(t *testing.T) {
	resolver := NewResolver(config.ADBConfig{})
	resolver.executable = func() (string, error) {
		t.Fatal("invalid request started ADB resolution")
		return "", nil
	}
	dispatcher := NewDispatcher(resolver)
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	for _, test := range []struct {
		name    string
		ctx     context.Context
		serial  string
		command DeviceCommand
		code    string
	}{
		{"no deadline", context.Background(), "emulator-5554", TapCommand{}, constants.InternalError},
		{"invalid serial", ctx, "emulator 5554", TapCommand{}, constants.InternalError},
		{"missing command", ctx, "emulator-5554", nil, constants.InternalError},
		{"unknown command", ctx, "emulator-5554", unknownCommand{}, constants.InternalError},
		{"negative pixel", ctx, "emulator-5554", TapCommand{X: -1}, constants.CommandInputUnsupported},
		{"pixel above limit", ctx, "emulator-5554", TapCommand{Y: constants.MaxAndroidPixelCoordinate + 1}, constants.CommandInputUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertCommandError(t, dispatcher.Dispatch(test.ctx, test.serial, test.command), test.code, false)
		})
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := dispatcher.Dispatch(cancelled, "emulator-5554", TapCommand{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	expired, expire := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer expire()
	if err := dispatcher.Dispatch(expired, "emulator-5554", TapCommand{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestDispatchResolverFailureAndCancellation(t *testing.T) {
	for _, cancelDuringResolve := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		resolver := NewResolver(config.ADBConfig{})
		resolver.executable = func() (string, error) {
			if cancelDuringResolve {
				cancel()
			}
			return "", os.ErrNotExist
		}
		resolver.run = func(context.Context, string, []string, time.Duration) processrunner.Result {
			t.Fatal("action started after unsuccessful resolution")
			return processrunner.Result{}
		}
		err := NewDispatcher(resolver).Dispatch(ctx, "emulator-5554", TapCommand{})
		cancel()
		if !cancelDuringResolve {
			assertCommandError(t, err, constants.ADBUnavailable, true)
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
	}
}

func TestDispatchClassifiesActionOutcomeWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		outcome   processrunner.Outcome
		code      string
		retryable bool
		context   error
	}{
		{"start failed", processrunner.StartFailed, constants.CommandStartFailed, true, nil},
		{"invalid invocation", processrunner.InvalidRequest, constants.InternalError, false, nil},
		{"nonzero exit", processrunner.NonzeroExit, constants.CommandFailed, false, nil},
		{"ambiguous failure", processrunner.RunnerFailed, constants.CommandFailed, false, nil},
		{"success without exit code", processrunner.Succeeded, constants.CommandFailed, false, nil},
		{"timeout", processrunner.TimedOut, "", false, context.DeadlineExceeded},
		{"cancelled", processrunner.Cancelled, "", false, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := NewResolver(config.ADBConfig{})
			resolver.verified.Executable = "verified executable"
			calls := 0
			resolver.run = func(context.Context, string, []string, time.Duration) processrunner.Result {
				calls++
				return processrunner.Result{Outcome: test.outcome, Stderr: []byte("private diagnostics")}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := NewDispatcher(resolver).Dispatch(ctx, "emulator-5554", TapCommand{})
			if test.context == nil {
				assertCommandError(t, err, test.code, test.retryable)
				if strings.Contains(err.Error(), "private diagnostics") {
					t.Fatal("error exposed process diagnostics")
				}
			} else if !errors.Is(err, test.context) {
				t.Fatalf("error = %v, want %v", err, test.context)
			}
			if calls != 1 {
				t.Fatalf("action calls = %d, want 1", calls)
			}
		})
	}
}

func assertCommandError(t *testing.T, err error, code string, retryable bool) {
	t.Helper()
	var failure xerror.CommandError
	if !errors.As(err, &failure) || failure.Code != code || failure.Retryable != retryable {
		t.Fatalf("error = %#v, want %s with retryable=%t", err, code, retryable)
	}
}
