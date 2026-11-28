package executionlifecycle

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// The operation pointer and fresh resume gate identify one live control generation.
type groupControl struct{ pause bool }

func (l *Service) Pause(ctx context.Context, id string) (constants.GroupControlDisposition, error) {
	if disposition, found, err := l.productAction(ctx, id, "PAUSE"); found {
		return disposition, err
	}
	return l.requestControl(ctx, id, true)
}

func (l *Service) Resume(ctx context.Context, id string) (constants.GroupControlDisposition, error) {
	if disposition, found, err := l.productAction(ctx, id, "RESUME"); found {
		return disposition, err
	}
	return l.requestControl(ctx, id, false)
}

func (l *Service) requestControl(ctx context.Context, id string, pause bool) (constants.GroupControlDisposition, error) {
	const operation = "request_group_control"
	if err := executionContextError(ctx); err != nil {
		return "", lifecycleError(operation, err)
	}
	if !execution.ValidID(id) {
		return "", lifecycleError(operation, xerror.ErrExecutionInvalidInput)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := executionContextError(ctx); err != nil {
		return "", lifecycleError(operation, err)
	}
	if err := l.gateError(); err != nil {
		return "", lifecycleError(operation, err)
	}
	g := l.group
	if g == nil || g.id != id || !g.registered || g.retiring || g.cancellation != nil || g.ctx.Err() != nil {
		return "", lifecycleError(operation, xerror.ErrExecutionControlState)
	}
	if g.live == 0 {
		return "", lifecycleError(operation, xerror.ErrExecutionControlState)
	}
	if g.control != nil {
		if g.control.pause == pause {
			return constants.GroupControlAccepted, nil
		}
		return "", lifecycleError(operation, xerror.ErrExecutionControlState)
	}
	if g.paused == pause {
		return constants.GroupControlAlreadyTarget, nil
	}
	op := &groupControl{pause: pause}
	g.control = op
	if pause {
		g.resume = make(chan struct{})
	}
	l.admissions++ // Accepted control must drain before SQLite closes, just like a start lease.
	g.controls.Add(1)
	go l.runControl(g, op)
	return constants.GroupControlAccepted, nil
}

// notifyBoundary requires the lifecycle mutex. Signals coalesce; no producer waits.
func (g *groupExecution) notifyBoundary() {
	select {
	case g.changed <- struct{}{}:
	default:
	}
}

func (l *Service) awaitBoundary(ctx context.Context, g *groupExecution, boundary execution.Boundary) (bool, error) {
	resumed := false
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return false, err
		}
		if l.closed || l.group != g || g.cancellation != nil {
			l.mu.Unlock()
			return false, context.Canceled // Existing G11 authority distinguishes stop from failure.
		}
		if saved, found := g.boundaries[boundary.DeviceExecutionID]; found {
			boundary = saved // A rapid later pause can already have committed another generation.
		}
		if !g.paused && (g.control == nil || !g.control.pause) {
			delete(g.boundaries, boundary.DeviceExecutionID)
			l.mu.Unlock()
			return resumed, nil
		}
		g.boundaries[boundary.DeviceExecutionID] = boundary
		g.notifyBoundary()
		gate := g.resume
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-gate:
			resumed = true
			boundary.Status = constants.ExecutionQueued
		}
	}
}

func (l *Service) runControl(g *groupExecution, op *groupControl) {
	defer func() {
		l.mu.Lock()
		if g.control == op {
			g.control = nil
		}
		l.admissions--
		l.signalDrained()
		l.mu.Unlock()
		g.controls.Done()
	}()
	var members []execution.Boundary
	for {
		l.mu.Lock()
		if l.closed || l.group != g || g.cancellation != nil || g.ctx.Err() != nil || g.control != op {
			l.mu.Unlock()
			return
		}
		if g.live == 0 {
			l.mu.Unlock()
			return // Normal terminal completion beat the requested target.
		}
		if len(g.boundaries) == g.live {
			for _, id := range g.queue.members {
				if boundary, ready := g.boundaries[id]; ready {
					members = append(members, boundary)
				}
			}
			l.mu.Unlock()
			break
		}
		l.mu.Unlock()
		select {
		case <-g.ctx.Done():
			return
		case <-g.changed:
		}
	}
	ctx, cancel := l.cleanupContext(g.ctx)
	defer cancel()
	request := execution.GroupControlRequest{RunGroupID: g.id, Now: time.Now().UTC(), Members: members}
	var result execution.GroupControlResult
	var err error
	if op.pause {
		result, err = l.checkpoints.PauseGroup(ctx, request)
	} else {
		result, err = l.checkpoints.ResumeGroup(ctx, request)
	}
	if err != nil {
		l.Fail(lifecycleError("checkpoint_group_control", err))
		return
	}
	l.mu.Lock()
	stopped := l.closed || l.group != g || g.cancellation != nil || g.ctx.Err() != nil
	if !stopped && result.Disposition == constants.GroupControlCommitted {
		g.paused = op.pause
		for id, boundary := range g.boundaries {
			boundary.Status = constants.ExecutionQueued
			if op.pause {
				boundary.Status = constants.ExecutionPaused
			}
			g.boundaries[id] = boundary
		}
		if !op.pause {
			close(g.resume) // Only a confirmed Resume may grant continuation.
		}
	}
	l.mu.Unlock()
	if !stopped && result.Disposition != constants.GroupControlCommitted {
		l.Fail(lifecycleError("confirm_group_control", xerror.ErrExecutionStaleState))
	}
}
