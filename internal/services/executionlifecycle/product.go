package executionlifecycle

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productauthorization"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// ProductGroup is the protected graph epoch of the existing process execution owner.
// Logical members survive parked worker returns. Only terminal cleanup retires it.
type ProductGroup struct {
	lifecycle                                           *Service
	id                                                  string
	mu                                                  sync.Mutex
	checkpointMu                                        sync.Mutex
	loaded                                              *productworkflow.Loaded
	worker                                              *runner.Runner
	devices                                             []runner.Request
	states                                              []*runner.Continuation
	terminal                                            []bool
	store                                               *variables.Store
	controller                                          *productauthorization.Controller
	ctx                                                 context.Context
	cancel                                              context.CancelFunc
	epochCancel                                         context.CancelFunc
	epochDone                                           chan struct{}
	park                                                chan struct{}
	parkClosed                                          bool
	wake                                                chan struct{}
	done                                                chan struct{}
	monitorDone                                         chan struct{}
	controls                                            sync.WaitGroup
	hold, userPause, paused, expiring, cancelled, ended bool
	pending                                             string
	failure                                             error
}

func (l *Service) PrepareProduct(id string, loaded *productworkflow.Loaded, worker *runner.Runner, devices []runner.Request, grant *executionlease.Verified) (*ProductGroup, error) {
	if loaded == nil || loaded.Document == nil || worker == nil || len(devices) == 0 {
		return nil, xerror.ErrExecutionLifecycleInvalid
	}
	l.mu.Lock()
	owner := l.ownerContext
	l.mu.Unlock()
	if owner == nil {
		return nil, xerror.ErrExecutionStopping
	}
	ids := make([]string, len(devices))
	states := make([]*runner.Continuation, len(devices))
	for i, d := range devices {
		if d.RunGroupID != id || !execution.ValidID(d.DeviceExecutionID) {
			return nil, xerror.ErrExecutionLifecycleInvalid
		}
		ids[i] = d.DeviceExecutionID
		states[i] = &runner.Continuation{}
	}
	store, err := variables.New(id, ids)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(owner)
	p := &ProductGroup{lifecycle: l, id: id, loaded: loaded, worker: worker, devices: devices, states: states, terminal: make([]bool, len(devices)), store: store, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), monitorDone: make(chan struct{})}
	for i, device := range devices {
		deviceID := device.DeviceExecutionID
		device.Variables = bindings.VariableView{RunGroupID: id, DeviceExecutionID: deviceID, Lookup: func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
			return store.Get(ctx, deviceID, scope, name)
		}}
		if err := states[i].Initialize(ctx, device, loaded.Document.EntryNodeID); err != nil {
			cancel()
			for _, s := range states {
				s.Close()
			}
			_ = store.Close(context.Background())
			return nil, err
		}
	}
	p.controller, err = productauthorization.New(grant, p, p, productauthorization.Options{})
	if err != nil {
		cancel()
		for _, state := range states {
			state.Close()
		}
		_ = store.Close(context.Background())
		return nil, err
	}
	return p, nil
}
func (p *ProductGroup) CheckInitial(ctx context.Context, timeout int64) error {
	return p.controller.CheckInitial(ctx, timeout)
}
func (p *ProductGroup) Authorization(ctx context.Context) (productauthorization.Snapshot, error) {
	return p.controller.Observe(ctx)
}

// Abort is for an untransferred candidate, never a live group.
func (p *ProductGroup) Abort() {
	p.cancel()
	p.controller.End()
	p.loaded.Close()
	for _, d := range p.devices {
		for _, raw := range d.WorkflowInputs {
			clear(raw)
		}
		clear(d.WorkflowInputs)
	}
	for _, s := range p.states {
		s.Close()
	}
	_ = p.store.Close(context.Background())
}
func (l *Service) DispatchProduct(p *ProductGroup) error {
	l.mu.Lock()
	owner := l.ownerContext
	l.mu.Unlock()
	if owner == nil {
		return xerror.ErrExecutionStopping
	}
	join, cancel := l.cleanupContext(owner)
	defer cancel()
	for {
		l.mu.Lock()
		if err := l.gateError(); err != nil {
			stopping := l.started && l.failure == nil && (l.prepared || owner.Err() != nil)
			l.mu.Unlock()
			if stopping {
				p.Abort()
				return nil
			} // Existing stop authority settles committed queued rows.
			return err
		}
		var done <-chan struct{}
		if previous := l.product; previous != nil {
			if previous.id == p.id {
				l.mu.Unlock()
				return xerror.ErrExecutionStaleState
			}
			done = previous.done
		}
		if previous := l.group; previous != nil {
			done = previous.done
		}
		if done != nil {
			l.mu.Unlock()
			select {
			case <-done:
				continue
			case <-join.Done():
				return join.Err()
			}
		}
		l.product = p
		l.admissions++
		l.mu.Unlock()
		go p.run()
		return nil
	}
}
func (l *Service) ProductAuthorization(ctx context.Context, id string) (productauthorization.Snapshot, error) {
	l.mu.Lock()
	p := l.product
	l.mu.Unlock()
	if p == nil || p.id != id {
		return productauthorization.Snapshot{}, xerror.ErrExecutionNotFound
	}
	return p.Authorization(ctx)
}
func (p *ProductGroup) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *ProductGroup) fenceLocked() {
	p.hold = true
	if p.park != nil && !p.parkClosed {
		close(p.park)
		p.parkClosed = true
	}
}
func (p *ProductGroup) run() {
	go func() {
		defer close(p.monitorDone)
		if err := p.controller.Run(p.ctx); err != nil {
			p.Fail(err)
		}
	}()
	defer p.cleanup()
	for {
		p.mu.Lock()
		if p.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		if p.hold {
			p.mu.Unlock()
			select {
			case <-p.ctx.Done():
				return
			case <-p.wake:
				continue
			}
		}
		epoch, cancel := context.WithCancel(p.ctx)
		p.epochCancel = cancel
		p.epochDone = make(chan struct{})
		p.park = make(chan struct{})
		p.parkClosed = false
		done := p.epochDone
		worker := p.worker
		var wg sync.WaitGroup
		for i := range p.devices {
			if !p.terminal[i] {
				wg.Add(1)
				go func(index int) { defer wg.Done(); p.runDevice(epoch, worker, index) }(i)
			}
		}
		p.mu.Unlock()
		wg.Wait()
		worker = nil
		cancel()
		p.mu.Lock()
		close(done)
		all := true
		for _, terminal := range p.terminal {
			all = all && terminal
		}
		p.mu.Unlock()
		if all {
			return
		}
		// A worker parks only after a hold was published or a policy gate denied it.
		p.mu.Lock()
		holding := p.hold
		p.mu.Unlock()
		if !holding && p.ctx.Err() == nil {
			p.Fail(xerror.ErrExecutionLifecycleInvalid)
			return
		}
	}
}
func (p *ProductGroup) runDevice(ctx context.Context, worker *runner.Runner, index int) {
	p.mu.Lock()
	request := p.devices[index]
	state := p.states[index]
	park := p.park
	p.mu.Unlock()
	request.State = state
	request.Park = park
	request.VariableStore = p.store
	deviceID, store := request.DeviceExecutionID, p.store
	request.Variables = bindings.VariableView{RunGroupID: p.id, DeviceExecutionID: deviceID, Lookup: func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
		return store.Get(ctx, deviceID, scope, name)
	}}
	request.Control = func() attempts.Control {
		control := p.lifecycle.groupControl(p.id)
		p.mu.Lock()
		control.GroupCancellation = control.GroupCancellation || p.cancelled
		p.mu.Unlock()
		return control
	}
	var decision productauthorization.Decision
	check := func(ctx context.Context, timeout int64) (bool, error) {
		p.mu.Lock()
		held := p.hold
		p.mu.Unlock()
		if held {
			return false, nil
		}
		var err error
		decision, err = p.controller.BeforeAttempt(ctx, timeout)
		if err != nil {
			return false, err
		}
		if decision.Kind != constants.ProductAttemptAllow {
			p.mu.Lock()
			p.fenceLocked()
			p.mu.Unlock()
			return false, nil
		}
		return true, nil
	}
	request.Permission = func(ctx context.Context, timeout int64, _ execution.Boundary) (bool, error) {
		return check(ctx, timeout)
	}
	request.BeforeBegin = check
	request.BeforeInvoke = func(ctx context.Context, timeout int64) error {
		fresh, err := p.controller.BeforeDispatch(ctx, timeout, decision)
		if err != nil {
			return err
		}
		if fresh.Kind != constants.ProductAttemptAllow {
			return xerror.ErrExecutionControlState
		}
		return nil
	}
	result, err := worker.Run(ctx, request)
	p.mu.Lock()
	if err != nil {
		p.failure = err
	}
	if result.DeviceStatus != "" {
		p.terminal[index] = true
	}
	p.mu.Unlock()
	if err != nil && p.ctx.Err() == nil {
		p.Fail(err)
	}
}
