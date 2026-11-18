// Package retry decides safe retries and waits without invoking or persisting work.
package retry

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Decide returns fresh D4 metadata for an eligible retry, or nil for no retry.
// A nil decision does not override result.Stop or authorize ordinary routing.
func Decide(
	ctx context.Context,
	policy ir.RetryPolicy,
	attemptNumber int,
	result outcomes.Result,
) (*execution.RetryDecision, error) {
	const operation = "decide_node_retry"
	fail := func(reason string) (*execution.RetryDecision, error) {
		return nil, retryError(ctx, operation, reason)
	}
	finish := func(decision *execution.RetryDecision) (*execution.RetryDecision, error) {
		if err := contextError(ctx); err != nil {
			return nil, xerror.WithOperation(operation, err)
		}
		return decision, nil
	}
	if err := contextError(ctx); err != nil {
		return nil, xerror.WithOperation(operation, err)
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > constants.MaxNodeAttemptNumber ||
		policy.DelayMS < 0 || policy.DelayMS > constants.MaxRetryDelayMS {
		return fail("retry_policy_invalid")
	}
	if attemptNumber < 1 || int64(attemptNumber) > policy.MaxAttempts {
		return fail("retry_attempt_invalid")
	}
	if result.Stop || result.Status == constants.ExecutionSucceeded || result.Status == constants.ExecutionTimedOut {
		return finish(nil)
	}
	if result.Status != constants.ExecutionFailed || result.Error == nil {
		return fail("retry_outcome_invalid")
	}
	if !result.Error.Retryable {
		return finish(nil)
	}
	if result.Error.Code != constants.CommandStartFailed && result.Error.Code != constants.ADBUnavailable {
		return fail("retry_outcome_invalid")
	}
	if int64(attemptNumber) == policy.MaxAttempts {
		return finish(nil)
	}
	return finish(&execution.RetryDecision{MaxAttempts: int(policy.MaxAttempts), DelayMS: int(policy.DelayMS)})
}

// Wait performs only the fixed delay. G9 must confirm the prior completion first
// and confirm a new RUNNING attempt before any later handler invocation.
func Wait(ctx context.Context, delayMS int) error {
	const operation = "wait_node_retry"
	if err := contextError(ctx); err != nil {
		return xerror.WithOperation(operation, err)
	}
	if delayMS < 0 || delayMS > constants.MaxRetryDelayMS {
		return retryError(ctx, operation, "retry_delay_invalid")
	}
	if delayMS > 0 {
		timer := time.NewTimer(time.Duration(delayMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	// An observable cancellation wins even if the timer channel was selected.
	if err := contextError(ctx); err != nil {
		return xerror.WithOperation(operation, err)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func retryError(ctx context.Context, operation, reason string) error {
	if err := contextError(ctx); err != nil {
		return xerror.WithOperation(operation, err)
	}
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrRetryInvalid}
}
