package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Exercise the real G1 -> G9/G10 -> D4 -> G13 path. Only the Delay
// invocation is controlled; result calculation, persistence and HTTP are real.
func TestG21RunnerFinalResults(t *testing.T) {
	for _, tc := range []struct {
		name, want, code string
		failures, max    int
		branch, timeout  bool
		cancel           bool
		history          []string
	}{
		{name: "success", want: "SUCCEEDED", max: 1, history: []string{"SUCCEEDED", "SUCCEEDED", "SUCCEEDED", "SUCCEEDED"}},
		{name: "stop", want: "FAILED", code: constants.CommandFailed, failures: 1, max: 1, history: []string{"SUCCEEDED", "FAILED"}},
		{name: "handled failure reaches End", want: "SUCCEEDED", code: constants.CommandFailed, failures: 1, max: 1, branch: true, history: []string{"SUCCEEDED", "FAILED", "SUCCEEDED"}},
		{name: "timeout stops", want: "FAILED", code: constants.NodeTimedOut, failures: 1, max: 2, timeout: true, history: []string{"SUCCEEDED", "TIMED_OUT"}},
		{name: "handled timeout reaches End", want: "SUCCEEDED", code: constants.NodeTimedOut, failures: 1, max: 2, branch: true, timeout: true, history: []string{"SUCCEEDED", "TIMED_OUT", "SUCCEEDED"}},
		{name: "retry succeeds", want: "SUCCEEDED", code: constants.CommandStartFailed, failures: 1, max: 2, history: []string{"SUCCEEDED", "FAILED", "SUCCEEDED", "SUCCEEDED", "SUCCEEDED"}},
		{name: "retries exhausted", want: "FAILED", code: constants.CommandStartFailed, failures: 2, max: 2, history: []string{"SUCCEEDED", "FAILED", "FAILED"}},
		{name: "exhausted retries handled", want: "SUCCEEDED", code: constants.CommandStartFailed, failures: 2, max: 2, branch: true, history: []string{"SUCCEEDED", "FAILED", "FAILED", "SUCCEEDED"}},
		{name: "accepted active cancellation", want: "CANCELLED", max: 1, cancel: true, history: []string{"SUCCEEDED", "CANCELLED"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := snapshotStore(t, 1)
			raw, err := os.ReadFile("../execution/runner/testdata/workflow.json")
			if err != nil {
				t.Fatal(err)
			}
			var doc ir.Document
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			doc.Nodes[1].RetryPolicy.MaxAttempts = int64(tc.max)
			if tc.branch {
				doc.Nodes[1].ErrorPolicy = constants.ErrorPolicyErrorBranch
				doc.Edges = append(doc.Edges, ir.Edge{ID: "handled", SourceNodeID: "producer", SourcePort: "error", TargetNodeID: "end"})
			}
			if tc.timeout {
				doc.Nodes[1].TimeoutMS = 10
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			accepted, calls := false, 0
			worker := finalResultRunner(t, f, &doc, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				calls++
				if tc.cancel {
					accepted = true
					cancel()
					return nil, ctx.Err()
				}
				if calls <= tc.failures {
					if tc.timeout {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return nil, xerror.CommandError{Code: tc.code, Message: fmt.Sprintf("Controlled final-result failure %d", calls), Retryable: tc.code == constants.CommandStartFailed}
				}
				return delay.Invoke(ctx, call)
			})
			f.admit(t)
			device := f.admission.Devices[0]
			result, err := worker.Run(ctx, runner.Request{RunGroupID: f.admission.RunGroupID, DeviceExecutionID: device.DeviceExecutionID,
				ADBSerial: device.ADBSerial, WorkflowInputs: ir.Object{"duration": []byte(`0`)},
				Control: func() attempts.Control { return attempts.Control{GroupCancellation: accepted} }})
			if err != nil || result.DeviceStatus != tc.want || result.NextNodeID != "" || !result.LastAttempt.Finished {
				t.Fatalf("runner result: %+v %v", result, err)
			}
			wantError := ""
			if tc.want == constants.ExecutionFailed {
				wantError = tc.code
			}
			if wantError == "" && result.TerminalError != nil || wantError != "" && (result.TerminalError == nil || result.TerminalError.Code != wantError) {
				t.Fatalf("runner terminal error: %+v", result.TerminalError)
			}
			history := assertFinalResult(t, f, tc.want, wantError, tc.history)
			last := history[len(history)-1]
			if result.LastAttempt.Ref.AttemptID != last.ID || result.LastAttempt.Sequence != last.Sequence || result.LastAttempt.Cursor != f.get(t, f.admission.RunGroupID).Cursor {
				t.Fatal("runner completion evidence does not identify the final commit")
			}
			if tc.want == constants.ExecutionFailed {
				if stored := assertFinalError(t, last.Error, wantError); *result.TerminalError != stored {
					t.Fatal("runner returned an earlier/different attempt error")
				}
			}
			if tc.want == constants.ExecutionSucceeded && last.NodeID != "end" {
				t.Fatal("workflow succeeded without reaching End")
			}
			if *last.FinishedAt != *f.get(t, f.admission.RunGroupID).DeviceExecutions[0].FinishedAt {
				t.Fatal("runner final attempt and device finished at different times")
			}
		})
	}
}

func finalResultRunner(t *testing.T, f *snapshotFixture, doc *ir.Document, run registry.Handler) *runner.Runner {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := registry.New(catalog, []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: start.Invoke},
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: run},
		{Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke},
	})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := runtimeir.New(catalog, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := validator.Validate(t.Context(), raw, []json.RawMessage{[]byte(`{"duration":0}`)})
	if err != nil || checked.Validation.Status != "VALID" {
		t.Fatalf("G1: %+v %v", checked, err)
	}
	resolver, err := bindings.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	invoker, err := invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	output, err := outputs.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := runner.New(t.Context(), checked.Document, runner.Dependencies{Bindings: resolver, Invoker: invoker, Outputs: output, Checkpoints: f.checkpoints})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestG21StoppedAndInterruptedFinalResults(t *testing.T) {
	for _, stop := range []string{"intentional", "startup recovery"} {
		for _, boundary := range []string{"queued", "between nodes", "active attempt"} {
			t.Run(stop+"/"+boundary, func(t *testing.T) {
				f := snapshotStore(t, 1)
				f.admit(t)
				var expected []string
				if boundary != "queued" {
					begin := f.begin(t, 0)
					if boundary == "between nodes" {
						_, err := f.checkpoints.FinishAttempt(t.Context(), execution.FinishAttemptRequest{AttemptRef: begin.AttemptRef,
							Status: constants.ExecutionSucceeded, NextNodeID: "next", Output: []byte(`{}`), Now: time.Now().UTC()})
						if err != nil {
							t.Fatal(err)
						}
						expected = []string{constants.ExecutionSucceeded}
					}
				}
				want, code := constants.ExecutionCancelled, ""
				settle := func() {
					t.Helper()
					if stop == "startup recovery" {
						recovery := services.NewExecutionRecovery(f.repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
						if err := recovery.Start(t.Context()); err != nil {
							t.Fatal(err)
						}
						if err := recovery.Shutdown(t.Context()); err != nil {
							t.Fatal(err)
						}
						return
					}
					lifetime := executionlifecycle.New(f.checkpoints)
					if err := lifetime.Start(t.Context()); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					deadline, _ := ctx.Deadline()
					lifetime.PrepareStop(constants.ExecutionStopIntentional, deadline)
					if err := lifetime.Shutdown(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if stop == "startup recovery" {
					want, code = constants.ExecutionInterrupted, constants.ExecutionInterruptedCode
				}
				if boundary == "active attempt" {
					expected = []string{want}
				}
				settle()
				assertFinalResult(t, f, want, code, expected)
				before := f.get(t, f.admission.RunGroupID)
				settle()
				if after := f.get(t, f.admission.RunGroupID); !sameSnapshotJSON(t, before, after) {
					t.Fatal("repeated shutdown/recovery rewrote a terminal result")
				}
			})
		}
	}
}

func assertFinalResult(t *testing.T, f *snapshotFixture, status, errorCode string, expected []string) []execution.NodeAttempt {
	t.Helper()
	before := f.counts(t)
	got := f.get(t, f.admission.RunGroupID)
	want := execution.RunSummary{Total: 1}
	switch status {
	case constants.ExecutionSucceeded:
		want.Succeeded = 1
	case constants.ExecutionFailed:
		want.Failed = 1
	case constants.ExecutionCancelled:
		want.Cancelled = 1
	case constants.ExecutionInterrupted:
		want.Interrupted = 1
	}
	device := got.DeviceExecutions[0]
	if got.RunGroup.Status != status || got.RunGroup.Summary != want || device.Status != status || device.NextNodeID != nil || got.RunGroup.FinishedAt == nil || device.FinishedAt == nil {
		t.Fatalf("terminal snapshot: %+v", got)
	}
	if *got.RunGroup.FinishedAt != *device.FinishedAt || *device.FinishedAt < device.CreatedAt || len(expected) == 0 && device.StartedAt != nil || len(expected) > 0 && (device.StartedAt == nil || *device.StartedAt > *device.FinishedAt) {
		t.Fatalf("terminal timestamps: %+v", got)
	}
	apiError := assertFinalError(t, device.TerminalError, errorCode)
	var groupStatus, deviceStatus string
	var finished, started sql.NullInt64
	var next sql.NullString
	var terminal []byte
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT g.status,d.status,d.next_node_id,d.started_at,d.finished_at,d.terminal_error_json
		FROM run_groups g JOIN device_executions d ON d.run_group_id=g.id WHERE g.id=?`, f.admission.RunGroupID).
		Scan(&groupStatus, &deviceStatus, &next, &started, &finished, &terminal); err != nil {
		t.Fatal(err)
	}
	stamp, err := time.Parse(time.RFC3339Nano, *device.FinishedAt)
	if err != nil || !finished.Valid || stamp.UnixMilli() != finished.Int64 || groupStatus != status || deviceStatus != status || next.Valid || started.Valid != (device.StartedAt != nil) {
		t.Fatal("SQLite terminal state does not match API snapshot", err)
	}
	if storedError := assertFinalError(t, terminal, errorCode); storedError != apiError {
		t.Fatal("SQLite and HTTP terminal errors disagree")
	}
	if started.Valid {
		stamp, err := time.Parse(time.RFC3339Nano, *device.StartedAt)
		if err != nil || stamp.UnixMilli() != started.Int64 {
			t.Fatal("SQLite and HTTP start timestamps disagree", err)
		}
	}
	page, err := f.repo.Attempts(t.Context(), device.ID, 0, constants.MaxAttemptPageSize)
	if err != nil || len(page.Attempts) != len(expected) {
		t.Fatalf("attempt history: %+v %v", page, err)
	}
	observed := historyPage(t, f, device.ID, "", 200)
	wantPage, _ := json.Marshal(page)
	gotPage, _ := json.Marshal(observed)
	if string(gotPage) != string(wantPage) {
		t.Fatal("attempt API changed retries or terminal history", string(gotPage), string(wantPage))
	}
	for i, attempt := range page.Attempts {
		if attempt.Status != expected[i] || attempt.Sequence != int64(i+1) || attempt.FinishedAt == nil || *attempt.FinishedAt < attempt.StartedAt {
			t.Fatalf("attempt %d: %+v", i, attempt)
		}
	}
	if status == constants.ExecutionFailed {
		if lastError := assertFinalError(t, page.Attempts[len(page.Attempts)-1].Error, errorCode); lastError != apiError {
			t.Fatal("terminal error is not the last failed attempt's error")
		}
	}
	events, err := f.repo.ReplayEvents(t.Context(), 0, 0)
	if err != nil || events.More || len(events.Events) < 2 {
		t.Fatalf("events: %+v %v", events, err)
	}
	groupTerminals, deviceTerminals := 0, 0
	for _, event := range events.Events {
		var payload struct {
			Status string
			Error  json.RawMessage
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if event.Type == constants.EventRunGroupStateChanged && payload.Status == status {
			groupTerminals++
		}
		if event.Type == constants.EventDeviceStateChanged && payload.Status == status {
			deviceTerminals++
			if eventError := assertFinalError(t, payload.Error, errorCode); eventError != apiError {
				t.Fatal("terminal device event and snapshot errors disagree")
			}
		}
	}
	last := events.Events[len(events.Events)-1]
	previous := events.Events[len(events.Events)-2]
	if groupTerminals != 1 || deviceTerminals != 1 || last.Type != constants.EventRunGroupStateChanged || previous.Type != constants.EventDeviceStateChanged || last.Cursor != got.Cursor || last.OccurredAt != *got.RunGroup.FinishedAt {
		t.Fatalf("terminal event publication: %+v", events)
	}
	repository, err := f.repo.SnapshotByID(t.Context(), f.admission.RunGroupID)
	if err != nil || !sameSnapshotJSON(t, got, repository) {
		t.Fatal("repository and HTTP snapshots disagree", err)
	}
	snapshotRequest(t, f.router, f.token, "active", 404)
	if after := f.get(t, f.admission.RunGroupID); !sameSnapshotJSON(t, got, after) || before != f.counts(t) {
		t.Fatal("reading final results changed stored state or cursor")
	}
	return page.Attempts
}

func assertFinalError(t *testing.T, raw json.RawMessage, code string) xerror.CommandError {
	t.Helper()
	if code == "" {
		if len(raw) != 0 && string(raw) != "null" {
			t.Fatalf("successful/cancelled result retained error: %s", raw)
		}
		return xerror.CommandError{}
	}
	var got xerror.CommandError
	if err := json.Unmarshal(raw, &got); err != nil || got.Code != code || got.Message == "" {
		t.Fatalf("terminal error: %s %v", raw, err)
	}
	// Verify that only the public A6 fields crossed the HTTP boundary.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 3 || fields["code"] == nil || fields["message"] == nil || fields["retryable"] == nil {
		t.Fatal("unexpected terminal error fields", fields, err)
	}
	return got
}
