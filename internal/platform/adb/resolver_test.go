package adb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestResolveCandidates(t *testing.T) {
	root := t.TempDir()
	installed := testFile(t, filepath.Join(root, "app with spaces", "runtime.exe"))
	bundle := filepath.Join(filepath.Dir(installed), platformToolsDir, executableName())
	development := testFile(t, filepath.Join(root, "dev", executableName()))
	override := testFile(t, filepath.Join(root, "override", executableName()))
	path := "relative" + string(os.PathListSeparator) + string(os.PathListSeparator) + filepath.Dir(development)

	for _, tc := range []struct {
		name string
		cfg  config.ADBConfig
		want Toolchain
		err  error
	}{
		{"fallback disabled", config.ADBConfig{}, Toolchain{}, xerror.ErrADBNotFound},
		{"development fallback", config.ADBConfig{AllowPathFallback: true}, Toolchain{development, DevelopmentPath, ""}, nil},
		{"broken override", config.ADBConfig{ExecutablePath: filepath.Join(root, "missing.exe"), AllowPathFallback: true}, Toolchain{}, xerror.ErrADBInvalidCandidate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testResolver(tc.cfg, installed, path)
			got, err := r.selectCandidate()
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("candidate = %+v, %v", got, err)
			}
		})
	}

	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	r := testResolver(config.ADBConfig{AllowPathFallback: true}, installed, path)
	if _, err := r.selectCandidate(); !errors.Is(err, xerror.ErrADBInvalidCandidate) {
		t.Fatalf("broken bundle should stop fallback: %v", err)
	}
	if err := os.Remove(bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(development, bundle); err == nil {
		if _, err := r.selectCandidate(); !errors.Is(err, xerror.ErrADBInvalidCandidate) {
			t.Fatalf("escaped bundled path: %v", err)
		}
		if err := os.Remove(bundle); err != nil {
			t.Fatal(err)
		}
	}
	bundle = testFile(t, bundle)
	if got, err := r.selectCandidate(); err != nil || got != (Toolchain{bundle, Bundled, ""}) {
		t.Fatalf("bundle = %+v, %v", got, err)
	}
	r.cfg.ExecutablePath = override
	if got, err := r.selectCandidate(); err != nil || got != (Toolchain{override, Configured, ""}) {
		t.Fatalf("override = %+v, %v", got, err)
	}
}

func TestResolveProbeAndRetry(t *testing.T) {
	path := testFile(t, filepath.Join(t.TempDir(), executableName()))
	r := NewResolver(config.ADBConfig{ExecutablePath: path})
	var calls int
	probes := []processrunner.Result{
		{Outcome: processrunner.TimedOut},
		probeOutput("other tool\n"),
		probeOutput("Android Debug Bridge version 1.0.41\r\nVersion other metadata\r\n"),
		probeOutput("Android Debug Bridge version 1.0.41\n"),
	}
	probes[2].StdoutTruncated = true
	r.run = func(_ context.Context, executable string, args []string, timeout time.Duration) processrunner.Result {
		if executable != path || !reflect.DeepEqual(args, []string{"version"}) || timeout != constants.ADBVersionProbeTimeout {
			t.Fatalf("unexpected probe: %q %q %s", executable, args, timeout)
		}
		calls++
		return probes[calls-1]
	}
	for range 3 {
		if _, err := r.Resolve(t.Context()); !errors.Is(err, xerror.ErrADBVersionProbe) {
			t.Fatalf("probe failure = %v", err)
		}
	}
	want := Toolchain{path, Configured, "1.0.41"}
	for range 2 {
		if got, err := r.Resolve(t.Context()); err != nil || got != want {
			t.Fatalf("toolchain = %+v, %v", got, err)
		}
	}
	if calls != 4 {
		t.Fatalf("probe calls = %d", calls)
	}
}

func TestResolveSharesProbeAndWaiterCanCancel(t *testing.T) {
	root := t.TempDir()
	path := testFile(t, filepath.Join(root, executableName()))
	r := NewResolver(config.ADBConfig{ExecutablePath: path})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r.run = func(ctx context.Context, _ string, _ []string, _ time.Duration) processrunner.Result {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return probeOutput("Android Debug Bridge version 1.0.41\r\n")
		case <-ctx.Done():
			return processrunner.Result{Outcome: processrunner.Cancelled}
		}
	}

	done := make(chan error, 4)
	resolve := func() {
		_, err := r.Resolve(ctx)
		done <- err
	}
	go resolve()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("probe did not start")
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancelWait()
	if _, err := r.Resolve(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v", err)
	}
	for range 3 {
		go resolve()
	}
	close(release)
	for range 4 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("resolver did not finish")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("probe calls = %d", n)
	}
}

func testResolver(cfg config.ADBConfig, installed, path string) *Resolver {
	r := NewResolver(cfg)
	r.executable = func() (string, error) { return installed, nil }
	r.path = func() string { return path }
	return r
}

func testFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func probeOutput(output string) processrunner.Result {
	code := 0
	return processrunner.Result{Outcome: processrunner.Succeeded, ExitCode: &code, Stdout: []byte(output)}
}
