package executionlifecycle

import (
	"context"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// BeginStart covers both the admission transaction and its committed-work handoff.
func (l *Service) BeginStart(ctx context.Context) (context.Context, func(), error) {
	release, err := l.BeginAdmission(ctx)
	if err != nil {
		return nil, nil, err
	}
	l.mu.Lock()
	owner := l.admissionContext
	l.mu.Unlock()
	admission, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(owner, cancel)
	var once sync.Once
	return admission, func() {
		once.Do(func() { stop(); cancel(); release() })
	}, nil
}

// Dispatch registers a worker before acknowledging handoff. Its lifetime belongs
// to the Runtime; request cancellation cannot abandon confirmed admission.
// A nil return consumes onExit, even when stop prevents worker registration.
func (l *Service) Dispatch(worker *runner.Runner, request runner.Request, onExit func(context.Context) error) error {
	l.mu.Lock()
	owner := l.ownerContext
	stopping := l.started && l.failure == nil && (l.prepared || owner.Err() != nil)
	l.mu.Unlock()
	if stopping {
		// The outstanding start lease retains these queued rows for G11/D6 settlement.
		return l.cleanupRun(owner, onExit)
	}
	if owner == nil {
		return lifecycleError("handoff_admitted_run", xerror.ErrExecutionLifecycleInvalid)
	}
	ctx, cancel, err := l.beginRun(owner, worker, request)
	if err != nil {
		// A stop can win between the check and worker registration.
		l.mu.Lock()
		stopping = l.failure == nil && (l.prepared || owner.Err() != nil)
		l.mu.Unlock()
		if stopping {
			return l.cleanupRun(owner, onExit)
		}
		return err
	}
	go func() {
		defer cancel()
		_, _ = l.runRegistered(ctx, worker, request, onExit) // runRegistered publishes failures to Wait.
	}()
	return nil
}

func (l *Service) cleanupRun(ctx context.Context, onExit func(context.Context) error) error {
	if onExit == nil {
		return nil
	}
	cleanup, cancel := l.cleanupContext(ctx)
	defer cancel()
	err := xerror.WithOperation("release_run_variables", onExit(cleanup))
	if err != nil {
		l.Fail(err)
	}
	return err
}

func (l *Service) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), constants.CheckpointTimeout)
	l.mu.Lock()
	deadline := l.deadline
	l.mu.Unlock()
	if !deadline.IsZero() {
		bounded, stop := context.WithDeadline(cleanup, deadline)
		return bounded, func() { stop(); cancel() }
	}
	return cleanup, cancel
}

// Fail publishes the infrastructure-stop fence without waiting on its caller's lease.
func (l *Service) Fail(err error) {
	if err == nil {
		err = xerror.ErrExecutionLifecycleInvalid
	}
	l.mu.Lock()
	l.recordFailure(err)
	cancels := l.cancellations()
	l.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
