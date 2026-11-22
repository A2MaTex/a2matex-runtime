package executionlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// groupExecution owns live resources, not a second copy of persisted run state.
type groupExecution struct {
	id      string
	ctx     context.Context
	cancel  context.CancelFunc
	worker  *runner.Runner
	devices []runner.Request
	store   *variables.Store
	queue   *deviceQueue
	done    chan struct{}
}

func newGroup(ctx context.Context, id string, worker *runner.Runner, devices []runner.Request) (*groupExecution, error) {
	invalid := func() (*groupExecution, error) {
		return nil, lifecycleError("configure_execution_group", xerror.ErrExecutionLifecycleInvalid)
	}
	if !execution.ValidID(id) || worker == nil || len(devices) == 0 {
		return invalid()
	}
	ids := make([]string, len(devices))
	seenIDs, seenSerials := make(map[string]bool, len(devices)), make(map[string]bool, len(devices))
	owned := make([]runner.Request, len(devices))
	for i, device := range devices {
		view := device.Variables
		if device.RunGroupID != id || !execution.ValidID(device.DeviceExecutionID) || seenIDs[device.DeviceExecutionID] ||
			!adb.ValidSerial(device.ADBSerial) || seenSerials[device.ADBSerial] || device.WorkflowInputs == nil ||
			device.Control != nil || device.VariableStore != nil || view.RunGroupID != "" || view.DeviceExecutionID != "" || view.Lookup != nil {
			return invalid()
		}
		ids[i], seenIDs[device.DeviceExecutionID], seenSerials[device.ADBSerial] = device.DeviceExecutionID, true, true
		owned[i] = device
		owned[i].WorkflowInputs = make(ir.Object, len(device.WorkflowInputs))
		for name, value := range device.WorkflowInputs {
			owned[i].WorkflowInputs[name] = bytes.Clone(value)
		}
	}
	queue, err := newDeviceQueue(ids)
	if err != nil {
		return nil, err
	}
	store, err := variables.New(id, ids)
	if err != nil {
		return nil, lifecycleError("configure_execution_group", err)
	}
	for i := range owned {
		deviceID := owned[i].DeviceExecutionID
		owned[i].VariableStore = store
		owned[i].Variables = bindings.VariableView{RunGroupID: id, DeviceExecutionID: deviceID,
			Lookup: func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
				return store.Get(ctx, deviceID, scope, name)
			}}
	}
	groupCtx, cancel := context.WithCancel(ctx)
	return &groupExecution{id: id, ctx: groupCtx, cancel: cancel, worker: worker, devices: owned, store: store, queue: queue, done: make(chan struct{})}, nil
}

// close runs only after execution and its synchronous checkpoint callbacks return.
func (g *groupExecution) close(ctx context.Context) error {
	if err := g.store.Close(ctx); err != nil {
		return lifecycleError("release_group_resources", err)
	}
	g.worker, g.devices, g.store = nil, nil, nil
	return nil
}

// DispatchGroup consumes the complete confirmed admission under the existing start lease.
func (l *Service) DispatchGroup(id string, worker *runner.Runner, devices []runner.Request) (err error) {
	const operation = "handoff_admitted_group"
	l.mu.Lock()
	owner := l.ownerContext
	l.mu.Unlock()
	if owner == nil {
		return lifecycleError(operation, xerror.ErrExecutionLifecycleInvalid)
	}
	g, err := newGroup(owner, id, worker, devices)
	if err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			err = errors.Join(err, l.cleanupRun(owner, g.close))
			g.cancel()
		}
	}()
	joinCtx, cancelJoin := l.cleanupContext(owner)
	defer cancelJoin()
	for {
		l.mu.Lock()
		if gateErr := l.gateError(); gateErr != nil {
			stopping := l.started && l.failure == nil && (l.prepared || owner.Err() != nil)
			l.mu.Unlock()
			if stopping {
				g.queue.stop()
				return nil // The start lease retains admitted rows for G11/D6 settlement.
			}
			return lifecycleError(operation, gateErr)
		}
		if previous := l.group; previous != nil {
			stop := l.admissionContext.Done()
			l.mu.Unlock()
			if previous.id == id {
				return lifecycleError(operation, xerror.ErrExecutionStaleState)
			}
			// A terminal checkpoint can precede the prior worker's return and cleanup.
			select {
			case <-previous.done:
				continue
			case <-stop:
				continue
			case <-joinCtx.Done():
				return lifecycleError("join_execution_group", joinCtx.Err())
			}
		}
		for _, device := range g.devices {
			if _, exists := l.workers[device.DeviceExecutionID]; exists {
				l.mu.Unlock()
				return lifecycleError(operation, xerror.ErrExecutionStaleState)
			}
		}
		l.group, registered = g, true
		l.mu.Unlock()
		return l.startGroup(g)
	}
}

// The producer finishes all Add calls before starting the group's final Wait.
func (l *Service) startGroup(g *groupExecution) error {
	const operation = "register_group_worker"
	var workers sync.WaitGroup
	defer func() { go l.retireGroup(g, &workers) }()
	for {
		l.mu.Lock()
		if err := l.gateError(); err != nil {
			g.queue.stop()
			stopping := l.failure == nil && (l.prepared || l.ownerContext.Err() != nil)
			if l.failure != nil {
				err = l.failure
			}
			l.mu.Unlock()
			if stopping {
				return nil // G11/D6 settles members left pending after the stop fence.
			}
			return lifecycleError(operation, err)
		}
		index, ok := g.queue.take()
		if !ok {
			if g.queue.next == len(g.queue.members) {
				l.mu.Unlock()
				return nil
			}
			err := lifecycleError(operation, xerror.ErrExecutionStaleState)
			l.recordFailure(err)
			l.mu.Unlock()
			l.Fail(err)
			return err
		}
		device := g.devices[index]
		if _, exists := l.workers[device.DeviceExecutionID]; exists {
			err := lifecycleError(operation, errors.Join(xerror.ErrExecutionStaleState, g.queue.release(device.DeviceExecutionID)))
			l.recordFailure(err)
			l.mu.Unlock()
			l.Fail(err)
			return err
		}
		ctx, cancel := context.WithCancel(g.ctx)
		l.workers[device.DeviceExecutionID] = cancel
		workers.Add(1)
		l.mu.Unlock()
		go func() {
			defer workers.Done()
			defer cancel()
			_, _ = l.runRegistered(ctx, g.worker, device, nil) // Publishes fatal errors before another registration.
			l.mu.Lock()
			err := g.queue.release(device.DeviceExecutionID)
			if err != nil {
				l.recordFailure(err)
			}
			l.mu.Unlock()
			if err != nil {
				l.Fail(err)
			}
		}()
	}
}

func (l *Service) retireGroup(g *groupExecution, workers *sync.WaitGroup) {
	workers.Wait()
	l.mu.Lock()
	valid := l.group == g && len(g.queue.active) == 0
	for _, id := range g.queue.members {
		if _, exists := l.workers[id]; exists {
			valid = false
		}
	}
	if !valid {
		l.recordFailure(lifecycleError("retire_execution_group", xerror.ErrExecutionStaleState))
	}
	l.mu.Unlock()
	if !valid {
		l.Fail(lifecycleError("retire_execution_group", xerror.ErrExecutionStaleState))
	}
	// The last returning member cannot close RAM while siblings/callbacks use it.
	_ = l.cleanupRun(g.ctx, g.close) // Cleanup failures are published before retirement.
	g.cancel()
	l.mu.Lock()
	current := l.group == g
	if current {
		l.group = nil
	} else {
		l.recordFailure(lifecycleError("retire_execution_group", xerror.ErrExecutionStaleState))
	}
	close(g.done)
	l.signalDrained()
	l.mu.Unlock()
	if !current {
		l.Fail(lifecycleError("retire_execution_group", xerror.ErrExecutionStaleState))
	}
}
