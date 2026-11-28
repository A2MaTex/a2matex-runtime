package executionlifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (l *Service) productAction(ctx context.Context, id, action string) (constants.GroupControlDisposition, bool, error) {
	l.mu.Lock()
	p := l.product
	gate := l.gateError()
	l.mu.Unlock()
	if p == nil || p.id != id {
		return "", false, nil
	}
	if gate != nil {
		return "", true, gate
	}
	if err := ctx.Err(); err != nil {
		return "", true, err
	}
	if action == constants.RunActionResume {
		auth, err := p.controller.Observe(ctx)
		if err != nil {
			return "", true, err
		}
		if auth.Phase == constants.ProductAuthorizationExpiring || auth.Phase == constants.ProductAuthorizationExpired {
			return "", true, xerror.ErrProductAuthorizationExpired
		}
		if auth.BudgetBlocked || auth.Phase == constants.ProductAuthorizationEnded {
			return "", true, xerror.ErrExecutionControlState
		}
	}
	p.mu.Lock()
	if p.ended {
		p.mu.Unlock()
		return "", true, xerror.ErrExecutionControlState
	}
	if action == constants.RunActionCancel {
		p.cancelled = true
		p.fenceLocked()
		p.mu.Unlock()
		p.controller.End()
		p.cancel()
		return constants.GroupControlAccepted, true, nil
	}
	if p.pending != "" {
		same := p.pending == action
		p.mu.Unlock()
		if same {
			return constants.GroupControlAccepted, true, nil
		}
		return "", true, xerror.ErrExecutionControlState
	}
	if p.expiring {
		p.mu.Unlock()
		return "", true, xerror.ErrProductAuthorizationExpired
	}
	if action == constants.RunActionPause && p.userPause {
		p.mu.Unlock()
		return constants.GroupControlAlreadyTarget, true, nil
	}
	if action == constants.RunActionResume && p.userPause && p.paused {
		timeouts := make([]int64, 0, len(p.states))
		for i, state := range p.states {
			if !p.terminal[i] {
				timeout, ok := p.worker.NodeTimeout(state.Result.NextNodeID)
				if !ok {
					p.mu.Unlock()
					return "", true, xerror.ErrExecutionLifecycleInvalid
				}
				timeouts = append(timeouts, timeout)
			}
		}
		p.mu.Unlock()
		for _, timeout := range timeouts {
			decision, err := p.controller.BeforeAttempt(ctx, timeout)
			if err != nil {
				return "", true, err
			}
			if decision.Kind != constants.ProductAttemptAllow {
				return "", true, xerror.ErrExecutionControlState
			}
		}
		p.mu.Lock()
		if p.expiring || p.cancelled || p.ended || p.pending != "" {
			p.mu.Unlock()
			return "", true, xerror.ErrExecutionControlState
		}
	}
	if action == constants.RunActionResume && !p.userPause {
		p.mu.Unlock()
		return constants.GroupControlAlreadyTarget, true, nil
	}
	p.pending = action
	p.controls.Add(1)
	if action == constants.RunActionPause {
		p.userPause = true
		p.fenceLocked()
	}
	p.mu.Unlock()
	go func() {
		defer p.controls.Done()
		defer func() { p.mu.Lock(); p.pending = ""; p.mu.Unlock() }()
		work, cancel := l.cleanupContext(p.ctx)
		defer cancel()
		if action == constants.RunActionPause {
			report, err := p.quiesce(work, p.id, constants.ProductInitialGeneration, constants.ProductQuiesceBudget, false)
			if err == nil && !report.Terminal {
				_, err = p.Pause(work, execution.GroupControlRequest{RunGroupID: p.id, Now: time.Now().UTC(), Members: report.Members})
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				p.Fail(err)
			}
			return
		}
		report, err := p.quiesce(work, p.id, constants.ProductInitialGeneration, constants.ProductQuiesceBudget, false)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				p.Fail(err)
			}
			return
		}
		if report.Terminal {
			return
		}
		p.checkpointMu.Lock()
		result, resumeErr := l.checkpoints.ResumeGroup(work, execution.GroupControlRequest{RunGroupID: p.id, Now: time.Now().UTC(), Members: report.Members})
		p.checkpointMu.Unlock()
		err = resumeErr
		if err == nil && (result.RunGroupID != p.id || result.Disposition != constants.GroupControlCommitted) {
			err = xerror.ErrExecutionStaleState
		}
		if err != nil {
			p.Fail(err)
			return
		}
		auth, observeErr := p.controller.Observe(work)
		allowResume := observeErr == nil && !auth.BudgetBlocked && (auth.Phase == constants.ProductAuthorizationActive || auth.Phase == constants.ProductAuthorizationRenewalRequired)
		p.mu.Lock()
		// Record the acknowledged durable state even if expiry won while
		// Resume committed. Expiry joins us before collecting fresh evidence.
		p.paused = false
		for i, s := range p.states {
			if !p.terminal[i] {
				s.Resume()
			}
		}
		if allowResume && !p.expiring && !p.cancelled && !p.ended {
			p.userPause = false
			p.hold = false
		}
		p.mu.Unlock()
		p.signal()
	}()
	return constants.GroupControlAccepted, true, nil
}
