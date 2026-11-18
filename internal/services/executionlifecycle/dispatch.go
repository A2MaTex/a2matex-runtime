package executionlifecycle

import (
	"context"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
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
func (l *Service) Dispatch(worker *runner.Runner, request runner.Request) error {
	l.mu.Lock()
	owner := l.ownerContext
	stopping := l.started && l.failure == nil && (l.prepared || owner.Err() != nil)
	l.mu.Unlock()
	if stopping {
		// The outstanding start lease retains these queued rows for G11/D6 settlement.
		return nil
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
			return nil
		}
		return err
	}
	go func() {
		defer cancel()
		_, _ = l.runRegistered(ctx, worker, request) // runRegistered publishes failures to Wait.
	}()
	return nil
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
