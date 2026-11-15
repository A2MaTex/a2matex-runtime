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

func TestParseDeviceList(t *testing.T) {
	const header = "List of devices attached"
	for _, tc := range []struct {
		name string
		text string
		want []Endpoint
	}{
		{"empty LF", header + "\n\n", []Endpoint{}},
		{"empty CRLF", "\r\n\t" + header + " \r\n", []Endpoint{}},
		{"mixed states", header + "\r\nphone\tunauthorized usb:1\r\nemulator-5554 device product:sdk model:Sample\r\n" +
			"[2001:db8::1]:5555 offline transport_id:2\r\nlinux\tno permissions (missing udev rules)\r\n" +
			"boot recovery\r\nnew authorizing", []Endpoint{
			{"[2001:db8::1]:5555", Offline}, {"boot", OtherUnavailable}, {"emulator-5554", Connected},
			{"linux", OtherUnavailable}, {"new", OtherUnavailable}, {"phone", Unauthorized},
		}},
		{"missing header", "", nil},
		{"unexpected preamble", "notice\n" + header, nil},
		{"repeated header", header + "\n" + header, nil},
		{"missing state", header + "\nphone device\nmalformed", nil},
		{"metadata without state", header + "\nphone model:Pixel", nil},
		{"duplicate serial", header + "\nphone device\nphone offline", nil},
		{"invalid UTF-8", header + "\nphone\xff device", nil},
		{"control character", header + "\nph\x00one device", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDeviceList([]byte(tc.text))
			if tc.want == nil {
				if got != nil || !errors.Is(err, xerror.ErrADBDiscovery) {
					t.Fatalf("expected discovery failure with no results, got %v, %v", got, err)
				}
				return
			}
			if err != nil || got == nil || !slices.Equal(got, tc.want) {
				t.Fatalf("endpoints = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestDiscoverUsesVerifiedToolchain(t *testing.T) {
	path := testFile(t, filepath.Join(t.TempDir(), executableName()))
	r := NewResolver(config.ADBConfig{ExecutablePath: path})
	var calls int
	r.run = func(_ context.Context, executable string, args []string, timeout time.Duration) processrunner.Result {
		if executable != path {
			t.Fatalf("executable = %q, want %q", executable, path)
		}
		calls++
		if calls == 1 {
			if !slices.Equal(args, []string{"version"}) || timeout != constants.ADBVersionProbeTimeout {
				t.Fatalf("probe args = %v, timeout = %v", args, timeout)
			}
			return probeOutput("Android Debug Bridge version 1.0.41\r\n")
		}
		if !slices.Equal(args, []string{"devices", "-l"}) || timeout != constants.ADBDiscoveryTimeout {
			t.Fatalf("discovery args = %v, timeout = %v", args, timeout)
		}
		result := probeOutput("List of devices attached\nphone device\n")
		result.Stderr = []byte("daemon started successfully")
		return result
	}
	for range 2 {
		got, err := r.Discover(t.Context())
		if err != nil || !slices.Equal(got, []Endpoint{{"phone", Connected}}) {
			t.Fatalf("endpoints = %v, %v", got, err)
		}
	}
	if calls != 3 {
		t.Fatalf("expected one probe and two discoveries, got %d invocations", calls)
	}
}

func TestDiscoverRejectsIncompleteResults(t *testing.T) {
	path := testFile(t, filepath.Join(t.TempDir(), executableName()))
	for _, name := range []string{"nonzero exit", "timeout", "truncated stdout", "caller cancellation"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := NewResolver(config.ADBConfig{ExecutablePath: path})
			r.run = func(_ context.Context, _ string, args []string, _ time.Duration) processrunner.Result {
				if slices.Equal(args, []string{"version"}) {
					return probeOutput("Android Debug Bridge version 1.0.41\n")
				}
				result := probeOutput("List of devices attached\nprivate-serial device\n")
				switch name {
				case "nonzero exit":
					result.Outcome = processrunner.NonzeroExit
				case "timeout":
					result.Outcome = processrunner.TimedOut
				case "truncated stdout":
					result.StdoutTruncated = true
				case "caller cancellation":
					cancel()
				}
				return result
			}
			want := xerror.ErrADBDiscovery
			if name == "caller cancellation" {
				want = context.Canceled
			}
			got, err := r.Discover(ctx)
			if got != nil || !errors.Is(err, want) || strings.Contains(err.Error(), "private-serial") {
				t.Fatalf("endpoints = %v, err = %v, want %v", got, err, want)
			}
		})
	}
}

func TestDiscoverClassifiesResolutionFailure(t *testing.T) {
	r := NewResolver(config.ADBConfig{ExecutablePath: filepath.Join(t.TempDir(), executableName())})
	r.run = func(context.Context, string, []string, time.Duration) processrunner.Result {
		t.Fatal("missing executable must not be run")
		return processrunner.Result{}
	}
	got, err := r.Discover(t.Context())
	if got != nil || !errors.Is(err, xerror.ErrADBDiscovery) || !errors.Is(err, xerror.ErrADBInvalidCandidate) {
		t.Fatalf("endpoints = %v, err = %v", got, err)
	}
}
