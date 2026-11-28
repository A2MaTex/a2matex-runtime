package productauthorization

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var hardExpiry = errors.New("product authorization hard deadline reached")

// Run owns all coordination callbacks and joins its deadline waiter. Callers
// must join Run before disposing the owner. Callbacks must honor cancellation;
// an unjoined borrower must never be cleared by an abandoned cleanup worker.
func (c *Controller) Run(ownerContext context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return configuration("monitor_product_authorization", "monitor_duplicate")
	}
	c.started = true
	if c.state.Phase == constants.ProductAuthorizationEnded && c.failure == nil {
		c.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(ownerContext)
	c.runCancel = cancel
	c.runContext = ctx
	c.mu.Unlock()
	defer cancel()
	err := c.monitor(ctx, ownerContext)
	c.mu.Lock()
	failure := c.failure
	c.mu.Unlock()
	if failure != nil {
		return c.fail(failure)
	}
	if err != nil && !(errors.Is(err, context.Canceled) && ctx.Err() != nil) {
		return c.fail(err)
	}
	c.End()
	return nil
}

func (c *Controller) monitor(ctx, ownerContext context.Context) error {
	for {
		c.mu.Lock()
		now, err := c.nowLocked()
		if err == nil {
			c.advanceLocked(now)
		}
		state, budgetPaused := c.state, c.budgetPaused
		c.mu.Unlock()
		if err != nil {
			return err
		}
		if state.Phase == constants.ProductAuthorizationEnded || ctx.Err() != nil {
			return nil
		}
		if state.Phase == constants.ProductAuthorizationExpiring {
			// Settlement may outlive cancellation only to confirm safe persistence/
			// cleanup. Stop/End is checked again after every synchronous callback.
			deadline := time.Now().Add(constants.CheckpointTimeout)
			if earlier, ok := ownerContext.Deadline(); ok && earlier.Before(deadline) {
				deadline = earlier
			}
			settlement, cancel := context.WithDeadline(context.WithoutCancel(ownerContext), deadline)
			// End cancels coordination but does not take cleanup authority from
			// the execution owner. Join this AfterFunc before returning.
			done := make(chan struct{})
			stop := context.AfterFunc(ctx, func() { cancel(); close(done) })
			deadlineDone := make(chan struct{})
			stopDeadline := context.AfterFunc(settlement, func() {
				defer close(deadlineDone)
				if errors.Is(settlement.Err(), context.DeadlineExceeded) && !ownerStopped(ctx) {
					// Publish the fatal fence even if a broken owner ignores its
					// bound. Run still joins that callback; no unsafe clear follows.
					c.fail(xerror.WithOperation("quiesce_product_execution", context.DeadlineExceeded))
				}
			})
			err = c.settle(settlement, ctx, state, constants.ProductQuiesceExpiry)
			if !stopDeadline() {
				<-deadlineDone
			}
			if !stop() {
				<-done
			}
			cancel()
			if err != nil {
				return err
			}
			continue
		}
		if state.Phase == constants.ProductAuthorizationExpired {
			select {
			case <-ctx.Done():
			case <-c.wake:
			}
			continue
		}
		if state.BudgetBlocked && !budgetPaused {
			err = c.drainBudget(ctx, state)
			if errors.Is(err, hardExpiry) {
				continue
			}
			if err != nil {
				return err
			}
			continue
		}
		deadline := state.ExpiresAt
		if state.RenewalReason == "" {
			deadline = state.RefreshAt
		}
		timer, err := c.timer(deadline.Sub(now))
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-c.wake:
		case <-timer.C():
		}
		timer.Stop()
	}
}

// fail publishes one fatal owner fence outside the mutex. The first cause wins;
// terminal/stop authority may already have ended an otherwise healthy controller.
func (c *Controller) fail(err error) error {
	c.mu.Lock()
	if c.state.Phase == constants.ProductAuthorizationEnded && c.failure == nil {
		c.mu.Unlock()
		return nil
	}
	if c.failure == nil {
		c.failure = err
	}
	err = c.failure
	report := !c.failureReported
	c.failureReported = true
	c.state.Phase = constants.ProductAuthorizationEnded
	c.transitionLocked()
	cancel := c.runCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if report {
		c.owner.Fail(err)
	}
	return err
}

func (c *Controller) drainBudget(parent context.Context, state Snapshot) error {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	joined := make(chan struct{})
	// This owned waiter keeps hard expiry observable while a cooperative
	// Quiesce/Pause callback blocks. It owns no graph and performs no I/O.
	go func() {
		defer close(done)
		for {
			c.mu.Lock()
			now, err := c.nowLocked()
			if err == nil {
				c.advanceLocked(now)
				if !now.Before(state.ExpiresAt) {
					err = hardExpiry
				}
			}
			c.mu.Unlock()
			if err != nil {
				cancel(err)
				if errors.Is(err, hardExpiry) {
					// A cooperative callback joins immediately. A broken one
					// must still raise a fatal fence within the cleanup bound.
					c.awaitBudgetJoin(parent, joined)
				} else {
					c.fail(err)
				}
				return
			}
			timer, err := c.timer(state.ExpiresAt.Sub(now))
			if err != nil {
				cancel(err)
				c.fail(err)
				return
			}
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C():
				timer.Stop()
			}
		}
	}()
	err := c.settle(ctx, parent, state, constants.ProductQuiesceBudget)
	close(joined)
	cancel(nil)
	<-done
	if cause := context.Cause(ctx); cause != nil && cause != context.Canceled {
		return cause
	}
	return err
}

func (c *Controller) awaitBudgetJoin(parent context.Context, joined <-chan struct{}) {
	duration := constants.CheckpointTimeout
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) < duration {
		duration = time.Until(deadline)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-joined:
	case <-parent.Done():
	case <-timer.C:
		if !ownerStopped(parent) {
			c.fail(xerror.WithOperation("quiesce_product_execution", context.DeadlineExceeded))
		}
	}
}

func (c *Controller) settle(ctx, live context.Context, state Snapshot, mode constants.ProductQuiesceMode) error {
	report, err := c.owner.Quiesce(ctx, state.ExecutionID, state.Generation, mode)
	if currentErr := c.current(ctx, live, state, mode); currentErr != nil {
		return currentErr
	}
	if err != nil {
		return xerror.WithOperation("quiesce_product_execution", err)
	}
	if err := validateReport(report, state); err != nil {
		return err
	}
	if report.Terminal {
		c.End() // The terminal owner joins and releases its own resources.
		return nil
	}
	c.mu.Lock()
	now := c.lastNow // A validated sample from current; no unsynchronized clock read.
	c.mu.Unlock()
	result, err := c.writer.Pause(ctx, execution.GroupControlRequest{
		RunGroupID: state.ExecutionID, Members: append([]execution.Boundary(nil), report.Members...), Now: now})
	if currentErr := c.current(ctx, live, state, mode); currentErr != nil {
		return currentErr
	}
	if err != nil {
		return xerror.WithOperation("checkpoint_product_expiry", err)
	}
	cursor, cursorErr := strconv.ParseInt(result.Cursor, 10, 64)
	committed := result.Disposition == constants.GroupControlCommitted && result.Changed == len(report.Members) && cursorErr == nil && cursor > 0
	already := result.Disposition == constants.GroupControlAlreadyTarget && report.PauseConfirmed
	if result.RunGroupID != state.ExecutionID || (!committed && !already) {
		return configuration("checkpoint_product_expiry", "pause_unconfirmed")
	}
	if mode == constants.ProductQuiesceExpiry {
		releaseErr := c.owner.ReleaseDefinition(ctx, state.ExecutionID, state.Generation)
		if currentErr := c.current(ctx, live, state, mode); currentErr != nil {
			return currentErr
		}
		if releaseErr != nil {
			return xerror.WithOperation("release_product_definition", releaseErr)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// The final publication is serialized with End and fresh gate checks.
	if err := c.currentLocked(ctx, live, state, mode); err != nil {
		return err
	}
	if mode == constants.ProductQuiesceExpiry {
		c.state.Phase = constants.ProductAuthorizationExpired
		c.transitionLocked()
	} else {
		c.budgetPaused = true
	}
	return nil
}

func (c *Controller) current(ctx, live context.Context, state Snapshot, mode constants.ProductQuiesceMode) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentLocked(ctx, live, state, mode)
}

func (c *Controller) currentLocked(ctx, live context.Context, state Snapshot, mode constants.ProductQuiesceMode) error {
	if ownerStopped(live) || c.state.Phase == constants.ProductAuthorizationEnded {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("quiesce_product_execution", context.Cause(ctx))
	}
	now, err := c.nowLocked()
	if err != nil {
		return err
	}
	c.advanceLocked(now)
	if state.Generation != c.state.Generation || state.ExecutionID != c.state.ExecutionID {
		return configuration("quiesce_product_execution", "report_stale")
	}
	if mode == constants.ProductQuiesceBudget && c.state.Phase == constants.ProductAuthorizationExpiring {
		return hardExpiry
	}
	return nil
}

func ownerStopped(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, bounded := ctx.Deadline()
	return bounded && !time.Now().Before(deadline)
}

func (c *Controller) timer(duration time.Duration) (Timer, error) {
	timer := c.clock.NewTimer(duration)
	if nilDependency(timer) {
		return nil, configuration("monitor_product_authorization", "timer_invalid")
	}
	if timer.C() == nil {
		timer.Stop()
		return nil, configuration("monitor_product_authorization", "timer_invalid")
	}
	return timer, nil
}
