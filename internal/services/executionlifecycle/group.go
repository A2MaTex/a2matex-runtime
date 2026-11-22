package executionlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

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

// DispatchGroup consumes confirmed admission under the existing start lease.
// H1 keeps live dispatch single-device; H2-H4 add scheduling and expand admission.
func (l *Service) DispatchGroup(id string, worker *runner.Runner, devices []runner.Request) (err error) {
	const operation = "handoff_admitted_group"
	if len(devices) != constants.MaxStartDevices {
		return lifecycleError(operation, xerror.ErrExecutionLifecycleInvalid)
	}
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
		device := g.devices[0]
		if _, exists := l.workers[device.DeviceExecutionID]; exists {
			l.mu.Unlock()
			return lifecycleError(operation, xerror.ErrExecutionStaleState)
		}
		index, ok := g.queue.take()
		if !ok {
			l.mu.Unlock()
			return lifecycleError(operation, xerror.ErrExecutionLifecycleInvalid)
		}
		device = g.devices[index]
		ctx, cancel := context.WithCancel(g.ctx)
		l.group, l.workers[device.DeviceExecutionID], registered = g, cancel, true
		l.mu.Unlock()
		go func() {
			defer cancel()
			_, _ = l.runRegistered(ctx, worker, device, g.close) // G11 publishes failures before ownership is released.
			g.cancel()
			l.mu.Lock()
			releaseErr := g.queue.release(device.DeviceExecutionID)
			if releaseErr != nil {
				l.recordFailure(releaseErr)
			}
			l.group = nil
			close(g.done)
			l.signalDrained()
			l.mu.Unlock()
			if releaseErr != nil {
				l.Fail(releaseErr)
			}
		}()
		return nil
	}
}
