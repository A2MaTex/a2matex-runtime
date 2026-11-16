// Package processrunner runs trusted local tools for the Runtime.
package processrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type Outcome string

const (
	InvalidRequest Outcome = "INVALID_REQUEST"
	Succeeded      Outcome = "SUCCEEDED"
	NonzeroExit    Outcome = "NONZERO_EXIT"
	StartFailed    Outcome = "START_FAILED"
	TimedOut       Outcome = "TIMED_OUT"
	Cancelled      Outcome = "CANCELLED"
	RunnerFailed   Outcome = "RUNNER_FAILED"
)

type Result struct {
	Outcome         Outcome
	ExitCode        *int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Duration        time.Duration
}

// Run invokes an absolute executable path supplied by trusted Runtime code.
// The caller owns argument construction and interpretation of the result.
func Run(ctx context.Context, executable string, args []string, timeout time.Duration) Result {
	return RunWithStdoutLimit(ctx, executable, args, timeout, constants.ProcessOutputLimitBytes)
}

// RunWithStdoutLimit changes only the stdout limit for a trusted adapter read.
func RunWithStdoutLimit(ctx context.Context, executable string, args []string, timeout time.Duration, limit int) Result {
	if !filepath.IsAbs(executable) || strings.ContainsRune(executable, 0) || timeout <= 0 || limit <= 0 {
		return Result{Outcome: InvalidRequest}
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return Result{Outcome: InvalidRequest}
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return Result{Outcome: contextOutcome(err)}
	}

	cmd := exec.CommandContext(runCtx, executable, args...)
	cmd.WaitDelay = constants.ProcessCleanupTimeout
	stdout := capture{limit: limit}
	stderr := capture{limit: constants.ProcessOutputLimitBytes}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	// Wait joins the context watcher. Record whether cancellation actually
	// stopped the child, since Wait may report the resulting exit instead.
	var stopOutcome Outcome
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		switch {
		case err == nil:
			stopOutcome = contextOutcome(runCtx.Err())
		case !errors.Is(err, os.ErrProcessDone):
			stopOutcome = RunnerFailed
		}
		return err
	}

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		outcome := StartFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = contextOutcome(err)
		}
		return Result{Outcome: outcome, Duration: time.Since(startedAt)}
	}

	err := cmd.Wait()
	result := Result{
		Outcome:         processOutcome(err, stopOutcome),
		Stdout:          stdout.data,
		Stderr:          stderr.data,
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
		Duration:        time.Since(startedAt),
	}
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code != -1 {
			result.ExitCode = &code
		}
	}
	return result
}

func contextOutcome(err error) Outcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return TimedOut
	}
	return Cancelled
}

func processOutcome(err error, stopOutcome Outcome) Outcome {
	if stopOutcome != "" {
		return stopOutcome
	}
	if err == nil {
		return Succeeded
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return NonzeroExit
	}
	return RunnerFailed
}

type capture struct {
	data      []byte
	truncated bool
	limit     int
}

func (c *capture) Write(p []byte) (int, error) {
	keep := min(len(p), c.limit-len(c.data))
	if keep != 0 {
		if c.data == nil {
			c.data = make([]byte, 0, c.limit)
		}
		c.data = append(c.data, p[:keep]...)
	}
	c.truncated = c.truncated || keep < len(p)
	return len(p), nil
}
