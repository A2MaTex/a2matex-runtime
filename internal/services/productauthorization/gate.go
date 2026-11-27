package productauthorization

import (
	"context"
	"sync"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Controller struct {
	mu              sync.Mutex
	owner           Owner
	writer          PauseWriter
	clock           Clock
	state           Snapshot
	lastNow         time.Time
	failure         error
	failureReported bool
	started         bool
	runCancel       context.CancelFunc
	runContext      context.Context
	budgetPaused    bool
	wake            chan struct{}
	changed         chan struct{}
}

func New(grant *executionlease.Verified, owner Owner, writer PauseWriter, options Options) (*Controller, error) {
	if grant == nil || nilDependency(owner) || nilDependency(writer) {
		return nil, configuration("create_product_authorization", "dependency_invalid")
	}
	claims := grant.Claims()
	if !execution.ValidID(claims.ExecutionID) || !execution.ValidID(claims.RuntimeSessionID) ||
		!execution.ValidID(claims.LeaseFamilyID) || claims.Sequence < constants.ExecutionLeaseInitialSequence ||
		grant.SigningKeyID() == "" || grant.RefreshAt().IsZero() || !grant.ExpiresAt().After(grant.RefreshAt()) {
		return nil, configuration("create_product_authorization", "grant_invalid")
	}
	clock := options.Clock
	if clock == nil {
		clock = realClock{}
	} else if nilDependency(clock) {
		return nil, configuration("create_product_authorization", "clock_invalid")
	}
	c := &Controller{owner: owner, writer: writer, clock: clock, wake: make(chan struct{}, 1), changed: make(chan struct{}, 1),
		state: Snapshot{Phase: constants.ProductAuthorizationActive, Generation: 1, Revision: 1,
			ExecutionID: claims.ExecutionID, RuntimeSessionID: claims.RuntimeSessionID, LeaseFamilyID: claims.LeaseFamilyID,
			Sequence: claims.Sequence, RefreshAt: grant.RefreshAt(), ExpiresAt: grant.ExpiresAt()}}
	if _, err := c.nowLocked(); err != nil {
		return nil, err
	}
	return c, nil
}

// CheckInitial checks admission without creating a renewal transition or hold.
func (c *Controller) CheckInitial(ctx context.Context, timeoutMS int64) error {
	if err := validateCheck(ctx, timeoutMS); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now, err := c.nowLocked()
	if err != nil {
		return err
	}
	if c.state.Phase != constants.ProductAuthorizationActive || c.state.BudgetBlocked ||
		budget(timeoutMS) > c.state.ExpiresAt.Sub(now) {
		return &xerror.OperationError{Operation: "check_product_initial_budget", Detail: "budget_insufficient", Err: xerror.ErrExecutionLeaseInvalid}
	}
	return nil
}

func (c *Controller) BeforeAttempt(ctx context.Context, timeoutMS int64) (Decision, error) {
	return c.check(ctx, timeoutMS, nil)
}

func (c *Controller) BeforeDispatch(ctx context.Context, timeoutMS int64, earlier Decision) (Decision, error) {
	return c.check(ctx, timeoutMS, &earlier)
}

func (c *Controller) check(ctx context.Context, timeoutMS int64, earlier *Decision) (Decision, error) {
	if err := validateCheck(ctx, timeoutMS); err != nil {
		return Decision{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if earlier != nil && (earlier.issuer != c || earlier.Kind != constants.ProductAttemptAllow ||
		earlier.Generation != c.state.Generation || earlier.Revision == 0 || earlier.Revision > c.state.Revision) {
		return Decision{}, configuration("check_product_dispatch", "decision_invalid")
	}
	now, err := c.nowLocked()
	if err != nil {
		return Decision{}, err
	}
	c.advanceLocked(now)
	kind := constants.ProductAttemptAllow
	switch c.state.Phase {
	case constants.ProductAuthorizationEnded:
		kind = constants.ProductAttemptEnded
	case constants.ProductAuthorizationExpiring, constants.ProductAuthorizationExpired:
		kind = constants.ProductAttemptExpired
	default:
		if !c.state.BudgetBlocked && budget(timeoutMS) > c.state.ExpiresAt.Sub(now) {
			c.renewLocked(constants.ProductRenewalAttemptBudget)
			c.state.BudgetBlocked = true
			c.transitionLocked()
		}
		if c.state.BudgetBlocked {
			kind = constants.ProductAttemptHold
		}
	}
	return Decision{Kind: kind, Generation: c.state.Generation, Revision: c.state.Revision, issuer: c}, nil
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Changed is a coalesced invalidation hint, never a durable event stream.
func (c *Controller) Changed() <-chan struct{} { return c.changed }

func (c *Controller) End() {
	c.mu.Lock()
	if c.state.Phase != constants.ProductAuthorizationEnded {
		c.state.Phase = constants.ProductAuthorizationEnded
		c.transitionLocked()
	}
	cancel := c.runCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func validateCheck(ctx context.Context, timeoutMS int64) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("check_product_attempt_budget", err)
	}
	if timeoutMS < 1 || timeoutMS > validation.NodeTimeoutMaxMS {
		return configuration("check_product_attempt_budget", "timeout_invalid")
	}
	return nil
}

func budget(timeoutMS int64) time.Duration {
	return time.Duration(timeoutMS)*time.Millisecond + constants.ProductAttemptBudgetMargin
}

// Clock access is serialized with all permission decisions.
func (c *Controller) nowLocked() (time.Time, error) {
	if c.failure != nil {
		return time.Time{}, c.failure
	}
	now := c.clock.Now()
	if now.IsZero() || (!c.lastNow.IsZero() && now.Before(c.lastNow)) {
		c.failure = configuration("check_product_authorization_clock", "clock_invalid")
		c.state.Phase = constants.ProductAuthorizationEnded
		c.transitionLocked()
		return time.Time{}, c.failure
	}
	c.lastNow = now
	return now, nil
}

func (c *Controller) advanceLocked(now time.Time) {
	if c.state.Phase != constants.ProductAuthorizationEnded && c.runContext != nil && ownerStopped(c.runContext) {
		c.state.Phase = constants.ProductAuthorizationEnded
		c.transitionLocked()
	}
	switch c.state.Phase {
	case constants.ProductAuthorizationEnded, constants.ProductAuthorizationExpired, constants.ProductAuthorizationExpiring:
		return
	}
	if !now.Before(c.state.ExpiresAt) {
		c.state.Phase = constants.ProductAuthorizationExpiring
		c.transitionLocked()
	} else if !now.Before(c.state.RefreshAt) {
		c.renewLocked(constants.ProductRenewalScheduled)
	}
}

func (c *Controller) renewLocked(reason constants.ProductRenewalReason) {
	if c.state.RenewalReason == "" {
		c.state.Phase, c.state.RenewalReason = constants.ProductAuthorizationRenewalRequired, reason
		c.transitionLocked()
	}
}

func (c *Controller) transitionLocked() {
	c.state.Revision++
	for _, channel := range []chan struct{}{c.wake, c.changed} {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}
