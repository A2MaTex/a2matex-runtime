package processrunner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func TestRunArgumentsAndExit(t *testing.T) {
	executable := helperPath(t)
	dir := filepath.Join(t.TempDir(), "path with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(dir, filepath.Base(executable))
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy helper: %v, close: %v", copyErr, closeErr)
	}

	for _, tc := range []struct {
		name string
		code int
		want processrunner.Outcome
	}{
		{"success", 0, processrunner.Succeeded},
		{"nonzero", 7, processrunner.NonzeroExit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"", "two words", "雪", "`literal` ; | &", "quote\" and slash\\"}
			result := processrunner.Run(t.Context(), copyPath, helperArgs(append([]string{"echo", strconv.Itoa(tc.code)}, args...)...), 10*time.Second)
			var got []string
			if err := json.Unmarshal(result.Stdout, &got); err != nil || !reflect.DeepEqual(got, args) {
				t.Fatalf("arguments = %q, want %q (decode %v)", got, args, err)
			}
			if result.Outcome != tc.want || result.ExitCode == nil || *result.ExitCode != tc.code ||
				string(result.Stderr) != "separate stderr" || result.Duration <= 0 {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestRunOutputLimit(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(strconv.Itoa(extra), func(t *testing.T) {
			limit := constants.ProcessOutputLimitBytes
			result := processrunner.Run(t.Context(), helperPath(t), helperArgs("output", strconv.Itoa(limit+extra)), 10*time.Second)
			if result.Outcome != processrunner.Succeeded || !bytes.Equal(result.Stdout, bytes.Repeat([]byte{0}, limit)) ||
				!bytes.Equal(result.Stderr, bytes.Repeat([]byte{0xff}, limit)) ||
				result.StdoutTruncated != (extra > 0) || result.StderrTruncated != (extra > 0) {
				t.Fatalf("outcome=%s stdout=%d stderr=%d truncated=%t/%t",
					result.Outcome, len(result.Stdout), len(result.Stderr), result.StdoutTruncated, result.StderrTruncated)
			}
		})
	}
}

func TestRunLifecycle(t *testing.T) {
	executable := helperPath(t)
	t.Run("invalid and missing executables", func(t *testing.T) {
		if got := processrunner.Run(t.Context(), "relative", nil, time.Second); got.Outcome != processrunner.InvalidRequest {
			t.Fatalf("relative path: %s", got.Outcome)
		}
		if got := processrunner.Run(t.Context(), executable, nil, 0); got.Outcome != processrunner.InvalidRequest {
			t.Fatalf("zero timeout: %s", got.Outcome)
		}
		if got := processrunner.Run(t.Context(), filepath.Join(t.TempDir(), "missing.exe"), nil, time.Second); got.Outcome != processrunner.StartFailed || got.ExitCode != nil {
			t.Fatalf("missing executable: %+v", got)
		}
	})
	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		got := processrunner.Run(ctx, executable, helperArgs("sleep"), time.Second)
		if got.Outcome != processrunner.Cancelled || got.Duration != 0 || got.ExitCode != nil {
			t.Fatalf("pre-cancelled invocation: %+v", got)
		}
	})
	t.Run("running cancellation", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "started")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan processrunner.Result, 1)
		go func() {
			done <- processrunner.Run(ctx, executable, helperArgs("mark", marker), 10*time.Second)
		}()
		deadline := time.After(5 * time.Second)
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		for {
			select {
			case result := <-done:
				t.Fatalf("child exited before cancellation: %+v", result)
			case <-deadline:
				t.Fatal("child did not signal startup")
			case <-poll.C:
				if _, err := os.Stat(marker); err != nil {
					continue
				}
				cancel()
				select {
				case result := <-done:
					if result.Outcome != processrunner.Cancelled {
						t.Fatalf("outcome = %s", result.Outcome)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("cancellation did not release the runner")
				}
				return
			}
		}
	})
	for _, parentDeadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout parent=%t", parentDeadline), func(t *testing.T) {
			ctx := t.Context()
			timeout := 250 * time.Millisecond
			if parentDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
				timeout = 10 * time.Second
			}
			started := time.Now()
			got := processrunner.Run(ctx, executable, helperArgs("sleep"), timeout)
			if got.Outcome != processrunner.TimedOut || time.Since(started) > 3*time.Second {
				t.Fatalf("timeout: %+v", got)
			}
		})
	}
	t.Run("descendant retains stdout", func(t *testing.T) {
		started := time.Now()
		got := processrunner.Run(t.Context(), executable, helperArgs("descendant"), 10*time.Second)
		if got.Outcome != processrunner.RunnerFailed || got.ExitCode == nil || *got.ExitCode != 0 || time.Since(started) > 4*time.Second {
			t.Fatalf("inherited output cleanup: %+v", got)
		}
	})
}

func helperPath(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func helperArgs(args ...string) []string {
	return append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)
}

func TestHelperProcess(t *testing.T) {
	index := slices.Index(os.Args, "--")
	if index < 0 {
		return
	}
	args := os.Args[index+1:]
	switch args[0] {
	case "echo":
		fmt.Fprint(os.Stderr, "separate stderr")
		_ = json.NewEncoder(os.Stdout).Encode(args[2:])
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "output":
		size, _ := strconv.Atoi(args[1])
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{0}, size))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte{0xff}, size))
	case "mark":
		if err := os.WriteFile(args[1], nil, 0600); err != nil {
			os.Exit(2)
		}
		fallthrough
	case "sleep":
		time.Sleep(15 * time.Second)
	case "descendant":
		cmd := exec.Command(helperPath(t), helperArgs("hold")...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_ = cmd.Process.Release()
	case "hold":
		time.Sleep(5 * time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}
