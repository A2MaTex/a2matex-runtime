package executionlifecycle

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productauthorization"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (p *ProductGroup) Quiesce(ctx context.Context, id string, generation uint64, mode constants.ProductQuiesceMode) (productauthorization.Quiescence, error) {
	return p.quiesce(ctx, id, generation, mode, true)
}

func (p *ProductGroup) quiesce(ctx context.Context, id string, generation uint64, mode constants.ProductQuiesceMode, joinControls bool) (productauthorization.Quiescence, error) {
	p.mu.Lock()
	if id != p.id || generation != constants.ProductInitialGeneration {
		p.mu.Unlock()
		return productauthorization.Quiescence{}, xerror.ErrExecutionStaleState
	}
	p.fenceLocked()
	if mode == constants.ProductQuiesceExpiry {
		p.expiring = true
		if p.epochCancel != nil {
			p.epochCancel()
		}
	}
	done := p.epochDone
	p.mu.Unlock()
	if done != nil {
		select {
		case <-ctx.Done():
			return productauthorization.Quiescence{}, ctx.Err()
		case <-done:
		}
	}
	// Expiry fences new controls before joining accepted controls. A prior Resume
	// may have committed after our worker join; read boundaries only after it ends.
	if joinControls {
		p.controls.Wait()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return productauthorization.Quiescence{}, p.failure
	}
	if p.cancelled || p.ctx.Err() != nil {
		return productauthorization.Quiescence{}, context.Canceled
	}
	report := productauthorization.Quiescence{ExecutionID: id, Generation: generation, PauseConfirmed: p.paused}
	for i, s := range p.states {
		if p.terminal[i] {
			continue
		}
		result := s.Result
		if result.LastAttempt.Began && !result.LastAttempt.Finished {
			return productauthorization.Quiescence{}, xerror.ErrExecutionStaleState
		}
		status := s.Expected
		if p.paused {
			status = constants.ExecutionPaused
		}
		next := result.NextNodeID
		if next == "" {
			next = p.loaded.Document.EntryNodeID
			status = constants.ExecutionQueued
		}
		report.Members = append(report.Members, execution.Boundary{DeviceExecutionID: p.devices[i].DeviceExecutionID, Status: status, NextNodeID: next, Sequence: result.LastAttempt.Sequence, AttemptID: result.LastAttempt.Ref.AttemptID})
	}
	report.Terminal = len(report.Members) == 0
	return report, nil
}
func (p *ProductGroup) Pause(ctx context.Context, r execution.GroupControlRequest) (execution.GroupControlResult, error) {
	p.checkpointMu.Lock()
	defer p.checkpointMu.Unlock()
	result, err := p.lifecycle.checkpoints.PauseGroup(ctx, r)
	if err != nil {
		return result, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	valid := result.RunGroupID == p.id && (result.Disposition == constants.GroupControlCommitted || (result.Disposition == constants.GroupControlAlreadyTarget && p.paused))
	if !valid {
		return result, xerror.ErrExecutionStaleState
	}
	p.paused = true
	return result, nil
}
func (p *ProductGroup) ReleaseDefinition(ctx context.Context, id string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Serialize with user controls so none retains a graph-bearing callback.
	p.controls.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if id != p.id || generation != constants.ProductInitialGeneration || !p.paused || !p.expiring {
		return xerror.ErrExecutionStaleState
	}
	p.worker = nil
	p.loaded.Close()
	p.loaded = nil
	for i := range p.devices {
		for _, raw := range p.devices[i].WorkflowInputs {
			clear(raw)
		}
		clear(p.devices[i].WorkflowInputs)
		p.devices[i].WorkflowInputs = nil
	}
	return nil
}
func (p *ProductGroup) Fail(err error) { p.lifecycle.Fail(err) }
func (p *ProductGroup) cleanup() {
	p.mu.Lock()
	p.ended = true
	p.mu.Unlock()
	p.controller.End()
	p.cancel()
	<-p.monitorDone
	p.controls.Wait()
	p.mu.Lock()
	cancelled := p.cancelled
	p.ended = true
	p.mu.Unlock()
	ctx, cancel := p.lifecycle.cleanupContext(p.ctx)
	defer cancel()
	if cancelled {
		if _, err := p.lifecycle.checkpoints.CancelUnfinished(ctx, execution.CancellationRequest{RunGroupID: p.id, Now: time.Now().UTC()}); err != nil {
			p.lifecycle.Fail(err)
		}
	}
	p.worker = nil
	p.loaded.Close()
	p.loaded = nil
	for _, s := range p.states {
		s.Close()
	}
	for i := range p.devices {
		for _, raw := range p.devices[i].WorkflowInputs {
			clear(raw)
		}
		p.devices[i].WorkflowInputs = nil
	}
	if err := p.store.Close(ctx); err != nil {
		p.lifecycle.Fail(err)
	}
	p.lifecycle.mu.Lock()
	if p.lifecycle.product == p {
		p.lifecycle.product = nil
	}
	p.lifecycle.admissions--
	close(p.done)
	p.lifecycle.signalDrained()
	p.lifecycle.mu.Unlock()
}
