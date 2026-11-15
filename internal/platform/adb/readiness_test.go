package adb

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const readinessTestSerial = "192.0.2.10:5555"

func TestProbeReadinessBootCheck(t *testing.T) {
	unavailableOutput := probeOutput("1\n")
	unavailableOutput.StdoutTruncated = true
	for _, tc := range []struct {
		name   string
		result processrunner.Result
		ready  bool
		err    error
	}{
		{"booted LF", probeOutput("1\n"), true, nil},
		{"booted CRLF", probeOutput(" \t1\r\n"), true, nil},
		{"empty property", probeOutput("\n"), false, nil},
		{"booting", probeOutput("0\n"), false, nil},
		{"unexpected output", probeOutput("1\nextra\n"), false, nil},
		{"truncated", unavailableOutput, false, nil},
		{"command timeout", processrunner.Result{Outcome: processrunner.TimedOut}, false, nil},
		{"cannot start", processrunner.Result{Outcome: processrunner.StartFailed}, false, xerror.ErrADBReadiness},
		{"runner failed", processrunner.Result{Outcome: processrunner.RunnerFailed}, false, xerror.ErrADBReadiness},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := newReadinessTestResolver(t)
			calls := 0
			resolver.run = func(ctx context.Context, executable string, args []string, timeout time.Duration) processrunner.Result {
				calls++
				want := []string{"-s", readinessTestSerial, "shell", "-T", "getprop", "sys.boot_completed"}
				if executable != resolver.verified.Executable || !slices.Equal(args, want) ||
					timeout != constants.ADBReadinessTimeout {
					t.Fatalf("unexpected invocation: %q %v %s", executable, args, timeout)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > constants.ADBReadinessTimeout {
					t.Fatal("probe context has no bounded deadline")
				}
				return tc.result
			}
			got, err := resolver.ProbeReadiness(t.Context(), Endpoint{readinessTestSerial, Connected})
			if calls != 1 || !errors.Is(err, tc.err) {
				t.Fatalf("calls = %d, error = %v, want one call and %v", calls, err, tc.err)
			}
			if tc.err != nil {
				if got != (Readiness{}) {
					t.Fatalf("unexpected result on failure: %+v", got)
				}
				return
			}
			if tc.ready {
				if got != (Readiness{ADBSerial: readinessTestSerial, Ready: true}) {
					t.Fatalf("unexpected ready result: %+v", got)
				}
			} else {
				assertUnavailable(t, got, constants.DeviceNotReady)
			}
		})
	}
}

func TestProbeReadinessSkipsUnavailableEndpoints(t *testing.T) {
	resolver := newReadinessTestResolver(t)
	resolver.run = func(context.Context, string, []string, time.Duration) processrunner.Result {
		t.Fatal("unavailable or invalid endpoint triggered an ADB command")
		return processrunner.Result{}
	}
	for _, tc := range []struct {
		state ConnectionState
		code  string
	}{
		{Offline, constants.DeviceOffline},
		{Unauthorized, constants.DeviceUnauthorized},
		{OtherUnavailable, constants.DeviceNotReady},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			got, err := resolver.ProbeReadiness(t.Context(), Endpoint{readinessTestSerial, tc.state})
			if err != nil {
				t.Fatal(err)
			}
			assertUnavailable(t, got, tc.code)
		})
	}
	for _, invalid := range []Endpoint{{"", Connected}, {"invalid serial", Connected}, {readinessTestSerial, "READY"}} {
		if _, err := resolver.ProbeReadiness(t.Context(), invalid); !errors.Is(err, xerror.ErrADBInvalidEndpoint) {
			t.Fatalf("invalid endpoint error = %v", err)
		}
	}
}

func TestProbeReadinessRechecksAfterNonzeroExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		list string
		code string
	}{
		{"missing", "another device\n", constants.DeviceNotFound},
		{"offline", readinessTestSerial + " offline\n", constants.DeviceOffline},
		{"unauthorized", readinessTestSerial + " unauthorized\n", constants.DeviceUnauthorized},
		{"still connected", readinessTestSerial + " device\n", constants.DeviceNotReady},
		{"discovery failed", "malformed-device-row\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := newReadinessTestResolver(t)
			calls := 0
			var probeDeadline time.Time
			resolver.run = func(ctx context.Context, _ string, args []string, _ time.Duration) processrunner.Result {
				calls++
				deadline, _ := ctx.Deadline()
				if calls == 1 {
					probeDeadline = deadline
					return processrunner.Result{Outcome: processrunner.NonzeroExit, Stderr: []byte("private output")}
				}
				if calls != 2 || !deadline.Equal(probeDeadline) || !slices.Equal(args, []string{"devices", "-l"}) {
					t.Fatalf("unexpected follow-up: %d %v %s", calls, args, deadline)
				}
				return probeOutput("List of devices attached\n" + tc.list)
			}
			got, err := resolver.ProbeReadiness(t.Context(), Endpoint{readinessTestSerial, Connected})
			if calls != 2 {
				t.Fatalf("calls = %d, want 2", calls)
			}
			if tc.code == "" {
				if got != (Readiness{}) || !errors.Is(err, xerror.ErrADBDiscovery) ||
					strings.Contains(err.Error(), "private output") {
					t.Fatalf("result = %+v, error = %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertUnavailable(t, got, tc.code)
		})
	}
}

func TestProbeReadinessPropagatesCallerContext(t *testing.T) {
	resolver := newReadinessTestResolver(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resolver.run = func(context.Context, string, []string, time.Duration) processrunner.Result {
		cancel()
		return processrunner.Result{Outcome: processrunner.NonzeroExit}
	}
	for range 2 {
		if got, err := resolver.ProbeReadiness(ctx, Endpoint{readinessTestSerial, Connected}); got != (Readiness{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("result = %+v, error = %v", got, err)
		}
	}

	deadlineCtx, cancelDeadline := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelDeadline()
	resolver.run = func(ctx context.Context, _ string, _ []string, _ time.Duration) processrunner.Result {
		<-ctx.Done()
		return processrunner.Result{Outcome: processrunner.TimedOut}
	}
	if got, err := resolver.ProbeReadiness(deadlineCtx, Endpoint{readinessTestSerial, Connected}); got != (Readiness{}) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("result = %+v, error = %v", got, err)
	}
}

func TestProbeReadinessReportsResolverFailure(t *testing.T) {
	resolver := NewResolver(config.ADBConfig{ExecutablePath: filepath.Join(t.TempDir(), executableName())})
	got, err := resolver.ProbeReadiness(t.Context(), Endpoint{readinessTestSerial, Connected})
	if got != (Readiness{}) || !errors.Is(err, xerror.ErrADBReadiness) || !errors.Is(err, xerror.ErrADBInvalidCandidate) {
		t.Fatalf("result = %+v, error = %v", got, err)
	}
}

func newReadinessTestResolver(t *testing.T) *Resolver {
	t.Helper()
	resolver := NewResolver(config.ADBConfig{})
	resolver.verified = Toolchain{Executable: filepath.Join(t.TempDir(), executableName())}
	return resolver
}

func assertUnavailable(t *testing.T, result Readiness, code string) {
	t.Helper()
	if result.ADBSerial != readinessTestSerial || result.Ready || result.Reason == nil ||
		result.Reason.Code != code || result.Reason.Message == "" {
		t.Fatalf("unavailable result = %+v, reason = %+v, want %s", result, result.Reason, code)
	}
}
