package executionlifecycle

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// The lifecycle mutex protects cancellation ownership, deadline and completion.
type groupCancellation struct {
	ctx      context.Context
	release  context.CancelFunc
	deadline time.Time
	timer    *time.Timer
	finished bool
}

// Cancel stops only this group. Its existing finalizer joins and settles it.
func (l *Service) Cancel(ctx context.Context, id string) (constants.GroupControlDisposition, error) {
	const operation = "cancel_execution_group"
	if err := executionContextError(ctx); err != nil {
		return "", lifecycleError(operation, err)
	}
	if !execution.ValidID(id) {
		return "", lifecycleError(operation, xerror.ErrExecutionInvalidInput)
	}
	l.mu.Lock()
	if err := executionContextError(ctx); err != nil {
		l.mu.Unlock()
		return "", lifecycleError(operation, err)
	}
	if err := l.gateError(); err != nil {
		l.mu.Unlock()
		return "", lifecycleError(operation, err)
	}
	g := l.group
	if g == nil || g.id != id || !g.registered {
		l.mu.Unlock()
		return "", lifecycleError(operation, xerror.ErrExecutionControlState)
	}
	if g.cancellation != nil {
		l.mu.Unlock()
		return constants.GroupControlAccepted, nil // Includes committed rows awaiting joined cleanup.
	}
	if g.retiring || g.live == 0 || g.ctx.Err() != nil {
		l.mu.Unlock()
		return "", lifecycleError(operation, xerror.ErrExecutionControlState)
	}
	deadline := time.Now().Add(constants.CheckpointTimeout)
	completion, release := context.WithDeadline(context.WithoutCancel(g.ctx), deadline)
	op := &groupCancellation{ctx: completion, release: release, deadline: deadline}
	g.cancellation = op
	g.queue.stop()
	l.admissions++ // Held until the sole finalizer has settled and retired the group.
	op.timer = time.AfterFunc(time.Until(deadline), func() { l.expireCancellation(g, op) })
	l.mu.Unlock()
	g.cancel() // Authority and the attempt fence were published before signalling workers.
	return constants.GroupControlAccepted, nil
}

// shorten requires the lifecycle mutex; repeated stop signals never extend a budget.
func (op *groupCancellation) shorten(deadline time.Time) {
	if !op.finished && deadline.Before(op.deadline) {
		op.deadline = deadline
		op.timer.Reset(max(time.Until(deadline), 0))
	}
}

func (l *Service) expireCancellation(g *groupExecution, op *groupCancellation) {
	l.mu.Lock()
	if op.finished || l.group != g {
		l.mu.Unlock()
		return
	}
	// A timer already firing can race an earlier Reset; recheck the current deadline.
	if remaining := time.Until(op.deadline); remaining > 0 {
		op.timer.Reset(remaining)
		l.mu.Unlock()
		return
	}
	l.recordFailure(lifecycleError("join_cancelled_group", context.DeadlineExceeded))
	cancels := l.cancellations()
	l.mu.Unlock()
	op.release()
	for _, cancel := range cancels {
		cancel()
	}
}

// Called only by retireGroup after workers and older controls have returned.
func (l *Service) settleGroupCancellation(g *groupExecution) {
	l.mu.Lock()
	op, failure := g.cancellation, l.failure
	l.mu.Unlock()
	if op == nil || failure != nil {
		return
	}
	ctx, cancel := l.groupCleanupContext(g)
	defer cancel()
	result, err := l.checkpoints.CancelUnfinished(ctx, execution.CancellationRequest{RunGroupID: g.id, Now: time.Now().UTC()})
	if err == nil {
		switch result.Status {
		case constants.ExecutionSucceeded, constants.ExecutionFailed, constants.ExecutionCancelled, constants.ExecutionInterrupted:
			if result.RunGroupID != g.id {
				err = xerror.ErrExecutionStaleState
			}
		default:
			err = xerror.ErrExecutionStaleState
		}
	}
	if err != nil {
		l.Fail(lifecycleError("settle_cancelled_group", err))
	}
}

func (l *Service) groupCleanupContext(g *groupExecution) (context.Context, context.CancelFunc) {
	l.mu.Lock()
	op := g.cancellation
	if op != nil {
		deadline := op.deadline
		if !l.deadline.IsZero() && l.deadline.Before(deadline) {
			deadline = l.deadline
		}
		l.mu.Unlock()
		return context.WithDeadline(op.ctx, deadline)
	}
	l.mu.Unlock()
	return l.cleanupContext(g.ctx)
}
