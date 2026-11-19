package executionlifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Service fences admission and joins owner-bound execution before storage closes.
type Service struct {
	checkpoints                         *services.CheckpointService
	mu                                  sync.Mutex
	started, closed, prepared, shutting bool
	accepted                            bool
	deadline                            time.Time
	rootDone                            <-chan struct{}
	ownerContext                        context.Context
	admissionContext                    context.Context
	cancelAdmissions                    context.CancelFunc
	workers                             map[string]context.CancelFunc
	admissions                          int
	drained, done, shutdownDone         chan struct{}
	drainOnce, doneOnce                 sync.Once
	failure, shutdownErr                error
}

func New(checkpoints *services.CheckpointService) *Service {
	return &Service{checkpoints: checkpoints, workers: make(map[string]context.CancelFunc),
		drained: make(chan struct{}), done: make(chan struct{}), shutdownDone: make(chan struct{})}
}

func (l *Service) Start(ctx context.Context) error {
	if err := executionContextError(ctx); err != nil {
		return xerror.WithOperation("start_execution_lifecycle", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.checkpoints == nil || l.started || l.closed {
		return lifecycleError("start_execution_lifecycle", xerror.ErrExecutionLifecycleInvalid)
	}
	l.started, l.rootDone = true, ctx.Done()
	l.ownerContext = ctx
	l.admissionContext, l.cancelAdmissions = context.WithCancel(ctx)
	return nil
}

func (l *Service) Wait() error {
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failure
}

func (l *Service) BeginAdmission(ctx context.Context) (func(), error) {
	const operation = "begin_execution_admission"
	if err := executionContextError(ctx); err != nil {
		return nil, lifecycleError(operation, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gateError(); err != nil {
		return nil, lifecycleError(operation, err)
	}
	l.admissions++
	var once sync.Once
	return func() { once.Do(func() { l.mu.Lock(); defer l.mu.Unlock(); l.admissions--; l.signalDrained() }) }, nil
}

func (l *Service) Run(ctx context.Context, worker *runner.Runner, request runner.Request) (runner.Result, error) {
	workerCtx, cancel, err := l.beginRun(ctx, worker, request)
	if err != nil {
		return runner.Result{}, err
	}
	defer cancel()
	return l.runRegistered(workerCtx, worker, request, nil)
}

func (l *Service) beginRun(ctx context.Context, worker *runner.Runner, request runner.Request) (context.Context, context.CancelFunc, error) {
	const operation = "run_managed_execution"
	if err := executionContextError(ctx); err != nil {
		return nil, nil, lifecycleError(operation, err)
	}
	if worker == nil || request.Control != nil || !execution.ValidID(request.RunGroupID) ||
		!execution.ValidID(request.DeviceExecutionID) || strings.TrimSpace(request.ADBSerial) == "" || request.WorkflowInputs == nil {
		return nil, nil, lifecycleError(operation, xerror.ErrExecutionLifecycleInvalid)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gateError(); err != nil {
		cancel()
		return nil, nil, lifecycleError(operation, err)
	}
	if _, exists := l.workers[request.DeviceExecutionID]; exists {
		cancel()
		return nil, nil, lifecycleError(operation, xerror.ErrExecutionStaleState)
	}
	if len(l.workers) >= constants.MaxAdmissionDevices {
		cancel()
		return nil, nil, lifecycleError(operation, xerror.ErrExecutionLifecycleInvalid)
	}
	l.workers[request.DeviceExecutionID] = cancel
	return workerCtx, cancel, nil
}

func (l *Service) runRegistered(workerCtx context.Context, worker *runner.Runner, request runner.Request, onExit func(context.Context) error) (runner.Result, error) {
	const operation = "run_managed_execution"
	request.Control = l.control
	result, err := worker.Run(workerCtx, request)
	// No execution body or synchronous checkpoint callback may outlive its RAM.
	cleanupErr := l.cleanupRun(workerCtx, onExit)
	l.mu.Lock()
	delete(l.workers, request.DeviceExecutionID)
	rootStopped := false
	select {
	case <-l.rootDone:
		rootStopped = true
	default:
	}
	expected := (l.prepared || rootStopped) && xerror.ExecutionFailureCode(err) == "" &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
	if cleanupErr != nil {
		err, expected = errors.Join(err, cleanupErr), false
	}
	var cancelOthers []context.CancelFunc
	if err != nil && !expected {
		l.recordFailure(err)
		cancelOthers = l.cancellations()
	}
	l.signalDrained()
	l.mu.Unlock()
	for _, stop := range cancelOthers {
		stop()
	}
	return result, xerror.WithOperation(operation, err)
}

// PrepareStop is nonblocking. Publish authority before explicitly cancelling workers.
func (l *Service) PrepareStop(reason constants.ExecutionStopReason, deadline time.Time) {
	l.mu.Lock()
	l.closed = true
	if l.cancelAdmissions != nil {
		l.cancelAdmissions()
	}
	valid := reason == constants.ExecutionStopIntentional || reason == constants.ExecutionStopOwnerLost || reason == constants.ExecutionStopFailure
	if !valid || deadline.IsZero() {
		l.recordFailure(lifecycleError("prepare_execution_stop", xerror.ErrExecutionLifecycleInvalid))
	}
	if !l.prepared {
		l.prepared = true
		l.accepted = valid && !deadline.IsZero() && reason == constants.ExecutionStopIntentional && l.failure == nil
	}
	if !deadline.IsZero() && (l.deadline.IsZero() || deadline.Before(l.deadline)) {
		l.deadline = deadline
	}
	cancels := l.cancellations()
	l.signalDrained()
	l.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (l *Service) Shutdown(ctx context.Context) (err error) {
	l.mu.Lock()
	if l.shutting {
		deadline := l.deadline
		l.mu.Unlock()
		if !deadline.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
		select {
		case <-l.shutdownDone:
			l.mu.Lock()
			defer l.mu.Unlock()
			return l.shutdownErr
		case <-ctx.Done():
			return lifecycleError("join_execution_workers", ctx.Err())
		}
	}
	l.shutting = true
	valid := l.started && l.checkpoints != nil && l.prepared && !l.deadline.IsZero()
	deadline := l.deadline
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if err != nil {
			l.recordFailure(err)
		}
		err = l.failure
		l.shutdownErr = err
		l.doneOnce.Do(func() { close(l.done) })
		close(l.shutdownDone)
	}()
	if !valid {
		l.PrepareStop(constants.ExecutionStopFailure, time.Time{})
		return lifecycleError("prepare_execution_stop", xerror.ErrExecutionLifecycleInvalid)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-l.drained:
	case <-ctx.Done():
		return lifecycleError("join_execution_workers", ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return lifecycleError("join_execution_workers", err)
	}
	l.mu.Lock()
	accepted, failure := l.accepted, l.failure
	l.mu.Unlock()
	if failure != nil {
		return failure
	}
	if !accepted {
		return nil
	}
	_, err = l.checkpoints.CancelUnfinished(ctx, execution.CancellationRequest{Now: time.Now().UTC()})
	return err
}

func (l *Service) control() attempts.Control {
	l.mu.Lock()
	defer l.mu.Unlock()
	return attempts.Control{GroupCancellation: l.accepted, ShutdownDeadline: l.deadline}
}

// The following helpers require mu. They never perform I/O or invoke cancellation callbacks.
func (l *Service) gateError() error {
	if l.closed {
		return xerror.ErrExecutionStopping
	}
	if !l.started {
		return xerror.ErrExecutionLifecycleInvalid
	}
	if l.ownerContext.Err() != nil {
		return xerror.ErrExecutionStopping
	}
	return nil
}

func (l *Service) signalDrained() {
	if l.closed && len(l.workers) == 0 && l.admissions == 0 {
		l.drainOnce.Do(func() { close(l.drained) })
	}
}

func (l *Service) recordFailure(err error) {
	if l.failure == nil {
		l.failure = err
	}
	l.closed = true
	if l.cancelAdmissions != nil {
		l.cancelAdmissions()
	}
	l.doneOnce.Do(func() { close(l.done) })
}

func (l *Service) cancellations() []context.CancelFunc {
	result := make([]context.CancelFunc, 0, len(l.workers))
	for _, cancel := range l.workers {
		result = append(result, cancel)
	}
	return result
}

func lifecycleError(operation string, err error) error { return xerror.WithOperation(operation, err) }

func executionContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
