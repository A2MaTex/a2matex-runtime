package executionlifecycle

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productauthorization"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

func TestProductExpiryJoinsEarlierResumeBeforeBoundaryReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		id, devices := groupDevices(1)
		epochDone := make(chan struct{})
		close(epochDone)
		p := &ProductGroup{id: id, ctx: context.Background(), devices: devices, states: []*runner.Continuation{{Result: runner.Result{NextNodeID: "next"}, Expected: constants.ExecutionRunning}}, terminal: []bool{false}, loaded: &productworkflow.Loaded{Document: &ir.Document{EntryNodeID: "next"}}, paused: true, epochDone: epochDone}
		p.controls.Add(1)
		reports := make(chan productauthorization.Quiescence, 1)
		failures := make(chan error, 1)
		go func() {
			report, err := p.Quiesce(t.Context(), id, constants.ProductInitialGeneration, constants.ProductQuiesceExpiry)
			reports <- report
			failures <- err
		}()
		synctest.Wait()
		select {
		case <-reports:
			t.Fatal("expiry reported stale pause before earlier Resume joined")
		default:
		}
		// An already accepted Resume commits QUEUED before observing the expiry fence.
		p.mu.Lock()
		p.paused = false
		p.states[0].Resume()
		p.mu.Unlock()
		p.controls.Done()
		report := <-reports
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
		if report.PauseConfirmed || len(report.Members) != 1 || report.Members[0].Status != constants.ExecutionQueued {
			t.Fatal("stale checkpoint report", report)
		}
	})
}

func TestProductDefinitionReleaseRequiresExactSettledOwner(t *testing.T) {
	p := &ProductGroup{id: uuid.NewString(), loaded: &productworkflow.Loaded{Document: &ir.Document{EntryNodeID: "next"}}}
	if err := p.ReleaseDefinition(t.Context(), p.id, constants.ProductInitialGeneration); err == nil || p.loaded == nil {
		t.Fatal("released before pause")
	}
	p.paused, p.expiring = true, true
	if err := p.ReleaseDefinition(t.Context(), uuid.NewString(), constants.ProductInitialGeneration); err == nil || p.loaded == nil {
		t.Fatal("released wrong owner")
	}
	if err := p.ReleaseDefinition(t.Context(), p.id, constants.ProductInitialGeneration); err != nil || p.loaded != nil || p.worker != nil {
		t.Fatal(err)
	}
}
