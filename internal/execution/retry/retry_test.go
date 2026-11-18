package retry_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/retry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func failure(code string, retryable bool) outcomes.Result {
	return outcomes.Result{Status: constants.ExecutionFailed,
		Error: &xerror.CommandError{Code: code, Message: "Approved producer explanation", Retryable: retryable}}
}

func guardError(t *testing.T, err error, operation, reason string) {
	t.Helper()
	var detail *xerror.OperationError
	var command xerror.CommandError
	if !errors.Is(err, xerror.ErrRetryInvalid) || !errors.As(err, &detail) ||
		detail.Operation != operation || detail.Detail != reason || errors.As(err, &command) {
		t.Fatalf("unexpected retry error: %v (%+v)", err, detail)
	}
	if err.Error() != operation+": node retry state is invalid" {
		t.Fatalf("unsafe retry error text: %v", err)
	}
}

func contextFailure(t *testing.T, err error, operation string, cause error) {
	t.Helper()
	var detail *xerror.OperationError
	if !errors.Is(err, cause) || errors.Is(err, xerror.ErrRetryInvalid) ||
		!errors.As(err, &detail) || detail.Operation != operation {
		t.Fatalf("lost context classification: %v (%+v)", err, detail)
	}
}

func TestDecideEligibleAndExhausted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		policy  ir.RetryPolicy
		attempt int
		want    *execution.RetryDecision
	}{
		{"one total attempt", ir.RetryPolicy{MaxAttempts: 1}, 1, nil},
		{"second of two", ir.RetryPolicy{MaxAttempts: 2, DelayMS: 1000}, 1, &execution.RetryDecision{MaxAttempts: 2, DelayMS: 1000}},
		{"two exhausted", ir.RetryPolicy{MaxAttempts: 2}, 2, nil},
		{"first of three zero delay", ir.RetryPolicy{MaxAttempts: 3}, 1, &execution.RetryDecision{MaxAttempts: 3}},
		{"second of three max delay", ir.RetryPolicy{MaxAttempts: 3, DelayMS: constants.MaxRetryDelayMS}, 2,
			&execution.RetryDecision{MaxAttempts: 3, DelayMS: constants.MaxRetryDelayMS}},
		{"third exhausted", ir.RetryPolicy{MaxAttempts: 3}, 3, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, code := range []string{constants.CommandStartFailed, constants.ADBUnavailable} {
				got, err := retry.Decide(t.Context(), tc.policy, tc.attempt, failure(code, true))
				if err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("Decide(%s) = %+v, %v; want %+v", code, got, err, tc.want)
				}
			}
		})
	}
}

func TestDecideDeclinesUnsafeOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result outcomes.Result
	}{
		{"success", outcomes.Result{Status: constants.ExecutionSucceeded}},
		{"timeout", outcomes.Result{Status: constants.ExecutionTimedOut}},
		{"fabricated retryable timeout", outcomes.Result{Status: constants.ExecutionTimedOut,
			Error: &xerror.CommandError{Code: constants.CommandStartFailed, Retryable: true}}},
		{"stop", outcomes.Result{Status: constants.ExecutionFailed, Stop: true}},
		{"cancellation stop", outcomes.Result{Status: constants.ExecutionCancelled, Stop: true}},
		{"start failure not retryable", failure(constants.CommandStartFailed, false)},
		{"tool failure not retryable", failure(constants.ADBUnavailable, false)},
		{"ordinary command failure", failure(constants.CommandFailed, false)},
		{"invalid output", failure(constants.NodeOutputInvalid, false)},
		{"device loss", failure(constants.DeviceDisconnected, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := retry.Decide(t.Context(), ir.RetryPolicy{MaxAttempts: 3}, 1, tc.result)
			if got != nil || err != nil {
				t.Fatalf("unsafe result selected a retry: %+v, %v", got, err)
			}
		})
	}
}

func TestDecideInvalidInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		policy       ir.RetryPolicy
		attempt      int
	}{
		{"omitted max attempts", "retry_policy_invalid", ir.RetryPolicy{}, 1},
		{"negative limit", "retry_policy_invalid", ir.RetryPolicy{MaxAttempts: -1}, 1},
		{"over limit", "retry_policy_invalid", ir.RetryPolicy{MaxAttempts: constants.MaxNodeAttemptNumber + 1}, 1},
		{"overflow limit", "retry_policy_invalid", ir.RetryPolicy{MaxAttempts: math.MaxInt64}, 1},
		{"negative delay", "retry_policy_invalid", ir.RetryPolicy{MaxAttempts: 3, DelayMS: -1}, 1},
		{"over delay", "retry_policy_invalid", ir.RetryPolicy{MaxAttempts: 3, DelayMS: constants.MaxRetryDelayMS + 1}, 1},
		{"overflow delay", "retry_policy_invalid", ir.RetryPolicy{MaxAttempts: 3, DelayMS: math.MaxInt64}, 1},
		{"zero attempt", "retry_attempt_invalid", ir.RetryPolicy{MaxAttempts: 3}, 0},
		{"negative attempt", "retry_attempt_invalid", ir.RetryPolicy{MaxAttempts: 3}, -1},
		{"attempt over policy", "retry_attempt_invalid", ir.RetryPolicy{MaxAttempts: 1}, 2},
		{"attempt over global limit", "retry_attempt_invalid", ir.RetryPolicy{MaxAttempts: 3}, math.MaxInt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := retry.Decide(t.Context(), tc.policy, tc.attempt, failure(constants.CommandStartFailed, true))
			if got != nil {
				t.Fatal("invalid input returned usable metadata")
			}
			guardError(t, err, "decide_node_retry", tc.reason)
		})
	}
	for _, status := range []string{"", constants.ExecutionRunning, constants.ExecutionQueued,
		constants.ExecutionPaused, constants.ExecutionInterrupted, constants.ExecutionCancelled} {
		t.Run("unsupported status "+status, func(t *testing.T) {
			got, err := retry.Decide(t.Context(), ir.RetryPolicy{MaxAttempts: 3}, 1, outcomes.Result{Status: status})
			if got != nil {
				t.Fatal("unsupported status returned a retry")
			}
			guardError(t, err, "decide_node_retry", "retry_outcome_invalid")
		})
	}
	for _, result := range []outcomes.Result{
		{Status: constants.ExecutionFailed},
		{Status: constants.ExecutionFailed, Output: json.RawMessage(`{"protected":"value"}`),
			Error: &xerror.CommandError{Code: "protected code", Message: "protected message", Retryable: true}},
	} {
		got, err := retry.Decide(t.Context(), ir.RetryPolicy{MaxAttempts: 1}, 1, result)
		if got != nil {
			t.Fatal("malformed exhausted result returned a retry")
		}
		guardError(t, err, "decide_node_retry", "retry_outcome_invalid")
	}
}

func TestG7HandoffAndDecisionOwnership(t *testing.T) {
	t.Parallel()
	policy := ir.RetryPolicy{MaxAttempts: 3, DelayMS: 1000}
	result := outcomes.Normalize(t.Context(), invocation.Observation{}, nil,
		xerror.CommandError{Code: constants.CommandStartFailed, Message: "Command could not start", Retryable: true})
	first, err := retry.Decide(t.Context(), policy, 1, result)
	if err != nil || first == nil || *first != (execution.RetryDecision{MaxAttempts: 3, DelayMS: 1000}) {
		t.Fatalf("real G7 result was not eligible: %+v, %v", first, err)
	}
	first.MaxAttempts, first.DelayMS = 1, constants.MaxRetryDelayMS+1
	second, err := retry.Decide(t.Context(), policy, 1, result)
	if err != nil || second == nil || *second != (execution.RetryDecision{MaxAttempts: 3, DelayMS: 1000}) ||
		policy != (ir.RetryPolicy{MaxAttempts: 3, DelayMS: 1000}) || !result.Error.Retryable {
		t.Fatalf("decision mutation affected caller or later decision: %+v, %v", second, err)
	}
	latest := outcomes.Normalize(t.Context(), invocation.Observation{AttemptError: context.DeadlineExceeded}, nil, nil)
	if next, err := retry.Decide(t.Context(), policy, 2, latest); next != nil || err != nil {
		t.Fatalf("previous retryability leaked into newest timeout: %+v, %v", next, err)
	}
	result.Error.Retryable = false
	if next, err := retry.Decide(t.Context(), policy, 2, result); next != nil || err != nil {
		t.Fatalf("previous retryability was retained: %+v, %v", next, err)
	}
	_, guard := retry.Decide(t.Context(), ir.RetryPolicy{}, 1, result)
	normalized := outcomes.Normalize(t.Context(), invocation.Observation{}, nil, guard)
	if normalized.Status != constants.ExecutionFailed || !normalized.Stop ||
		normalized.Error == nil || normalized.Error.Code != constants.InternalError || normalized.Error.Retryable {
		t.Fatalf("guard error did not become an internal stop: %+v", normalized)
	}
}

func TestWaitFixedDelayAndBounds(t *testing.T) {
	for _, ms := range []int{0, 1, 1000, constants.MaxRetryDelayMS} {
		t.Run(fmt.Sprintf("delay %d", ms), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				if err := retry.Wait(t.Context(), ms); err != nil || time.Since(start) != time.Duration(ms)*time.Millisecond {
					t.Fatalf("fixed wait: error=%v, elapsed=%v", err, time.Since(start))
				}
			})
		})
	}
	for _, ms := range []int{-1, constants.MaxRetryDelayMS + 1, math.MaxInt} {
		t.Run(fmt.Sprintf("invalid delay %d", ms), func(t *testing.T) {
			guardError(t, retry.Wait(t.Context(), ms), "wait_node_retry", "retry_delay_invalid")
		})
	}
}

func TestInactiveContextPreventsDecisionsAndWaits(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled", ctx, context.Canceled},
		{"deadline before timer signal", fixedDeadline{Context: t.Context(), at: time.Unix(1, 0)}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := retry.Decide(tc.ctx, ir.RetryPolicy{MaxAttempts: 3}, 1, failure(constants.CommandStartFailed, true))
			if got != nil {
				t.Fatal("inactive execution selected a retry")
			}
			contextFailure(t, err, "decide_node_retry", tc.want)
			_, err = retry.Decide(tc.ctx, ir.RetryPolicy{}, 0, outcomes.Result{})
			contextFailure(t, err, "decide_node_retry", tc.want)
			for _, ms := range []int{0, 1000, -1} {
				contextFailure(t, retry.Wait(tc.ctx, ms), "wait_node_retry", tc.want)
			}
		})
	}
}

func TestWaitCancellationAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parent time.Duration
		waitMS int
	}{
		{"earlier parent deadline", time.Second, 2000},
		{"timer and deadline tie", time.Second, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), tc.parent)
				defer cancel()
				start := time.Now()
				err := retry.Wait(ctx, tc.waitMS)
				contextFailure(t, err, "wait_node_retry", context.DeadlineExceeded)
				if time.Since(start) != tc.parent {
					t.Fatalf("deadline wait lasted %v, want %v", time.Since(start), tc.parent)
				}
			})
		})
	}
	t.Run("owner cancels during wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() {
				time.Sleep(500 * time.Millisecond)
				cancel()
			}()
			start := time.Now()
			contextFailure(t, retry.Wait(ctx, 2000), "wait_node_retry", context.Canceled)
			if time.Since(start) != 500*time.Millisecond {
				t.Fatalf("cancellation did not stop the wait: %v", time.Since(start))
			}
		})
	})
}

func TestCancellationAtReturnAndGuardCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy ir.RetryPolicy
		result outcomes.Result
	}{
		{"eligible return", ir.RetryPolicy{MaxAttempts: 3}, failure(constants.CommandStartFailed, true)},
		{"declined return", ir.RetryPolicy{MaxAttempts: 3}, failure(constants.CommandFailed, false)},
		{"guard error", ir.RetryPolicy{}, outcomes.Result{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			controlled := &cancelOnCheck{Context: ctx, cancel: cancel}
			got, err := retry.Decide(controlled, tc.policy, 1, tc.result)
			if got != nil {
				t.Fatal("late cancellation returned a usable decision")
			}
			contextFailure(t, err, "decide_node_retry", context.Canceled)
		})
	}
	for _, ms := range []int{0, 1000, -1} {
		t.Run(fmt.Sprintf("wait check %d", ms), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				controlled := &cancelOnCheck{Context: ctx, cancel: cancel}
				contextFailure(t, retry.Wait(controlled, ms), "wait_node_retry", context.Canceled)
			})
		})
	}
}

func TestConcurrentCallsAreIndependent(t *testing.T) {
	t.Parallel()
	result := failure(constants.CommandStartFailed, true)
	for n := range 16 {
		t.Run(fmt.Sprintf("execution %d", n), func(t *testing.T) {
			t.Parallel()
			policy := ir.RetryPolicy{MaxAttempts: 3, DelayMS: int64(n)}
			got, err := retry.Decide(t.Context(), policy, 1, result)
			if err != nil || got == nil || got.DelayMS != n || got.MaxAttempts != 3 {
				t.Fatalf("execution decision leaked: %+v, %v", got, err)
			}
			got.DelayMS = -1
			next, err := retry.Decide(t.Context(), policy, 2, result)
			if err != nil || next == nil || next.DelayMS != n {
				t.Fatalf("execution decision retained another call's mutation: %+v, %v", next, err)
			}
		})
	}
}

func TestConcurrentWaitsUseIndependentTimers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type observation struct {
			ms      int
			elapsed time.Duration
			err     error
		}
		results := make(chan observation, 2)
		for _, ms := range []int{1000, 2000} {
			go func() {
				start := time.Now()
				err := retry.Wait(t.Context(), ms)
				results <- observation{ms: ms, elapsed: time.Since(start), err: err}
			}()
		}
		for range 2 {
			got := <-results
			if got.err != nil || got.elapsed != time.Duration(got.ms)*time.Millisecond {
				t.Fatalf("concurrent timer affected another wait: %+v", got)
			}
		}
	})
}

type fixedDeadline struct {
	context.Context
	at time.Time
}

func (c fixedDeadline) Deadline() (time.Time, bool) { return c.at, true }

type cancelOnCheck struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
}

func (c *cancelOnCheck) Err() error {
	if c.checks.Add(1) == 2 {
		c.cancel()
	}
	return c.Context.Err()
}
