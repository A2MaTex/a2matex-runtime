package runner

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestGraphFreeParkResumeKeepsOutputsAndVisits(t *testing.T) {
	doc := document(t)
	r := makeRunner(t, doc)
	req := request()
	req.State = &Continuation{}
	calls := 0
	hold := true
	r.attempts = attemptFunc(func(_ context.Context, call attempts.Request) (attempts.Result, error) {
		calls++
		return success(call, int64(calls)), nil
	})
	req.Permission = func(_ context.Context, _ int64, b execution.Boundary) (bool, error) {
		return b.NextNodeID != "consumer" || !hold, nil
	}
	got, err := r.Run(t.Context(), req)
	if err != nil || !got.Parked || calls != 2 || req.State.Values.CommittedOutputs["producer"] == nil || !req.State.Visited["producer"] {
		t.Fatal(got, err, calls)
	}
	hold = false
	req.State.Resume()
	got, err = r.Run(t.Context(), req)
	if err != nil || got.DeviceStatus != constants.ExecutionSucceeded || calls != 4 {
		t.Fatal(got, err, calls)
	}
	req.State.Close()
	if req.State.Values.WorkflowInputs != nil || req.State.Visited != nil {
		t.Fatal("state retained")
	}
}
func TestPendingRetryParksWithoutRestartingBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := makeRunner(t, document(t))
		req := request()
		req.State = &Continuation{}
		park := make(chan struct{})
		req.Park = park
		calls := 0
		r.attempts = attemptFunc(func(_ context.Context, call attempts.Request) (attempts.Result, error) {
			calls++
			if calls == 2 {
				return attempts.Result{Began: true, Finished: true, Sequence: 2, Cursor: "10", NextNodeID: call.Node.ID, Outcome: outcomes.Result{Status: constants.ExecutionFailed, Error: &xerror.CommandError{Code: constants.CommandFailed, Message: "controlled", Retryable: true}}, Retry: &execution.RetryDecision{MaxAttempts: int(call.Node.RetryPolicy.MaxAttempts), DelayMS: int(call.Node.RetryPolicy.DelayMS)}}, nil
			}
			return success(call, int64(calls)), nil
		})
		// Use the existing valid retry policy and force the pause during its wait.
		n := r.nodes["producer"]
		n.RetryPolicy = ir.RetryPolicy{MaxAttempts: 2, DelayMS: 5000}
		r.nodes[n.ID] = n
		close(park)
		got, err := r.Run(t.Context(), req)
		if err != nil || !got.Parked || req.State.Number != 2 || req.State.PreviousID == "" {
			t.Fatal(got, err, req.State.Number)
		}
		due := req.State.RetryDue
		time.Sleep(3 * time.Second)
		req.Park = nil
		req.State.Resume()
		got, err = r.Run(t.Context(), req)
		if err != nil || got.DeviceStatus != constants.ExecutionSucceeded || time.Now().Before(due) || time.Now().After(due) {
			t.Fatal(got, err, time.Now(), due)
		}
	})
}

func TestQueuedContinuationOwnsInputsBeforeFirstEpoch(t *testing.T) {
	req := request()
	state := &Continuation{}
	if err := state.Initialize(t.Context(), req, "start"); err != nil {
		t.Fatal(err)
	}
	if state.Result.NextNodeID != "start" || state.Expected != constants.ExecutionQueued || state.Number != 1 {
		t.Fatal("queued position lost")
	}
	req.WorkflowInputs["duration"][0] = '9'
	if string(state.Values.WorkflowInputs["duration"]) != "7" {
		t.Fatal("queued input aliases preparation")
	}
	state.Close()
	if state.Values.WorkflowInputs != nil {
		t.Fatal("queued input retained")
	}
}
