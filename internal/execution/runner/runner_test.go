package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type attemptFunc func(context.Context, attempts.Request) (attempts.Result, error)

func (f attemptFunc) Run(ctx context.Context, r attempts.Request) (attempts.Result, error) {
	return f(ctx, r)
}

type noCheckpoints struct{}

func (noCheckpoints) BeginAttempt(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
	return execution.CheckpointResult{}, errors.New("unexpected checkpoint")
}
func (noCheckpoints) FinishAttempt(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	return execution.CheckpointResult{}, errors.New("unexpected checkpoint")
}

func document(t *testing.T) *ir.Document {
	t.Helper()
	data, err := os.ReadFile("testdata/workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc ir.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return &doc
}
func request() Request {
	return Request{RunGroupID: uuid.NewString(), DeviceExecutionID: uuid.NewString(), ADBSerial: "emulator-5554", WorkflowInputs: ir.Object{"duration": json.RawMessage(`7`)}}
}
func components(t *testing.T, handler registry.Handler) (*nodecontract.Registry, *registry.Registry, Dependencies) {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if handler == nil {
		handler = delay.Invoke
	}
	handlers, err := registry.New(catalog, []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: start.Invoke}, {Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke}, {Type: "flow.delay", RuntimeHandler: "flow.delay", Run: handler},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := bindings.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	o, err := outputs.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	i, err := invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, handlers, Dependencies{Bindings: b, Outputs: o, Invoker: i, Checkpoints: noCheckpoints{}}
}
func makeRunner(t *testing.T, doc *ir.Document) *Runner {
	t.Helper()
	_, _, deps := components(t, nil)
	r, err := New(t.Context(), doc, deps)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func success(call attempts.Request, sequence int64) attempts.Result {
	next := map[string]string{"start": "producer", "producer": "consumer", "consumer": "end"}[call.Node.ID]
	output := json.RawMessage(`{}`)
	if call.Node.Type == "flow.delay" {
		output = json.RawMessage(`{"durationMs":7}`)
	}
	return attempts.Result{Began: true, Finished: true, Sequence: sequence, Cursor: "10", NextNodeID: next, Outcome: outcomes.Result{Status: constants.ExecutionSucceeded, Output: output}}
}
func checkHandoff(t *testing.T, result Result, err error) {
	t.Helper()
	if err == nil || result.DeviceStatus != "" || result.TerminalError != nil {
		t.Fatalf("unfinished work reported terminal: %+v %v", result, err)
	}
}

func TestConstructionAndRequestGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*ir.Document)
	}{
		{"no nodes", func(d *ir.Document) { d.Nodes = nil }}, {"duplicate", func(d *ir.Document) { d.Nodes[1].ID = d.Nodes[0].ID }},
		{"bad ID", func(d *ir.Document) { d.Nodes[0].ID = "private invalid ID" }}, {"missing entry", func(d *ir.Document) { d.EntryNodeID = "missing" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := document(t)
			tc.change(d)
			_, _, deps := components(t, nil)
			r, err := New(t.Context(), d, deps)
			if r != nil || !errors.Is(err, xerror.ErrRunnerInvalid) || strings.Contains(err.Error(), "private") {
				t.Fatalf("guard: %v %v", r, err)
			}
		})
	}
	_, _, deps := components(t, nil)
	if r, err := New(t.Context(), nil, deps); r != nil || !errors.Is(err, xerror.ErrRunnerInvalid) {
		t.Fatalf("nil doc: %v %v", r, err)
	}
	deps.Invoker = nil
	if r, err := New(t.Context(), document(t), deps); r != nil || !errors.Is(err, xerror.ErrAttemptInvalid) {
		t.Fatalf("nil dependency: %v %v", r, err)
	}
	r := makeRunner(t, document(t))
	r.attempts = attemptFunc(func(context.Context, attempts.Request) (attempts.Result, error) {
		t.Fatal("invalid request dispatched")
		return attempts.Result{}, nil
	})
	for _, tc := range []struct {
		name   string
		change func(*Request)
	}{
		{"group", func(r *Request) { r.RunGroupID = "invalid" }}, {"device", func(r *Request) { r.DeviceExecutionID = "invalid" }},
		{"serial", func(r *Request) { r.ADBSerial = " " }}, {"nil inputs", func(r *Request) { r.WorkflowInputs = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := request()
			tc.change(&req)
			got, err := r.Run(t.Context(), req)
			if !reflect.DeepEqual(got, Result{}) || !errors.Is(err, xerror.ErrRunnerInvalid) {
				t.Fatalf("request guard: %+v %v", got, err)
			}
		})
	}
	for _, r := range []*Runner{nil, {}} {
		if _, err := r.Run(t.Context(), request()); !errors.Is(err, xerror.ErrRunnerInvalid) {
			t.Fatal(err)
		}
	}
}

func TestSequenceOutputPublicationAndOwnedInputs(t *testing.T) {
	t.Parallel()
	r := makeRunner(t, document(t))
	req := request()
	ids := map[string]bool{}
	calls := 0
	r.attempts = attemptFunc(func(_ context.Context, call attempts.Request) (attempts.Result, error) {
		calls++
		if !execution.ValidID(call.Ref.AttemptID) || ids[call.Ref.AttemptID] || call.AttemptNumber != 1 || call.PreviousAttemptID != "" || call.ADBSerial != req.ADBSerial {
			t.Fatalf("identity: %+v", call)
		}
		ids[call.Ref.AttemptID] = true
		wantState := constants.ExecutionRunning
		if calls == 1 {
			wantState = constants.ExecutionQueued
		}
		if call.ExpectedDeviceStatus != wantState {
			t.Fatal(call.ExpectedDeviceStatus)
		}
		switch call.Node.ID {
		case "start":
			if len(call.Values.CommittedOutputs) != 0 {
				t.Fatal("shared output view")
			}
			call.Values.WorkflowInputs["duration"][0] = '8'
		case "producer":
			if string(call.Values.WorkflowInputs["duration"]) != "8" {
				t.Fatal("lost execution-local copy")
			}
		case "consumer":
			if string(call.Values.CommittedOutputs["producer"]["durationMs"]) != "7" {
				t.Fatal("committed snapshot unavailable")
			}
		}
		return success(call, int64(calls)), nil
	})
	got, err := r.Run(t.Context(), req)
	if err != nil || got.DeviceStatus != constants.ExecutionSucceeded || got.TerminalError != nil || got.NextNodeID != "" || calls != 4 || got.LastAttempt.Ref.NodeID != "end" || got.LastAttempt.Sequence != 4 || string(req.WorkflowInputs["duration"]) != "7" {
		t.Fatalf("workflow: %+v %v calls=%d inputs=%s", got, err, calls, req.WorkflowInputs["duration"])
	}
}

func TestErrorsAndInvalidContinuationsStopDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*attempts.Result)
		cause  error
	}{
		{"unconfirmed begin", func(a *attempts.Result) { *a = attempts.Result{} }, xerror.ErrExecutionCommitUnknown},
		{"unconfirmed finish", func(a *attempts.Result) { *a = attempts.Result{Began: true, Sequence: 1} }, xerror.ErrCheckpointFatal},
		{"missing acknowledgement", func(a *attempts.Result) { a.Finished = false }, nil},
		{"missing target", func(a *attempts.Result) { a.NextNodeID = "missing" }, nil},
		{"ordinary revisit", func(a *attempts.Result) { a.NextNodeID = "start" }, nil},
		{"stop with continuation", func(a *attempts.Result) { a.Outcome.Stop = true }, nil},
		{"bad snapshot", func(a *attempts.Result) { a.Outcome.Output = json.RawMessage(`null`) }, nil},
		{"bad retry policy", func(a *attempts.Result) { a.Retry = &execution.RetryDecision{MaxAttempts: 3}; a.NextNodeID = "start" }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := makeRunner(t, document(t))
			calls := 0
			req := request()
			r.attempts = attemptFunc(func(_ context.Context, call attempts.Request) (attempts.Result, error) {
				calls++
				result := success(call, 1)
				tc.change(&result)
				return result, tc.cause
			})
			got, err := r.Run(t.Context(), req)
			checkHandoff(t, got, err)
			if calls != 1 || got.LastAttempt.Ref.NodeID != "start" || !execution.ValidID(got.LastAttempt.Ref.AttemptID) {
				t.Fatalf("evidence: %+v calls=%d", got, calls)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("classification lost: %v", err)
			}
		})
	}
	view := map[string]ir.Object{"node": {}}
	if err := publishOutput("node", json.RawMessage(`{}`), view); !errors.Is(err, xerror.ErrRunnerInvalid) {
		t.Fatal("duplicate output accepted")
	}
}

func TestLifecycleAndTerminalRace(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"before first", "after continuation", "after terminal"} {
		t.Run(when, func(t *testing.T) {
			r := makeRunner(t, document(t))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := request()
			accepted := when == "before first"
			req.Control = func() attempts.Control { return attempts.Control{GroupCancellation: accepted} }
			calls := 0
			r.attempts = attemptFunc(func(_ context.Context, call attempts.Request) (attempts.Result, error) {
				calls++
				a := success(call, int64(calls))
				if when == "after continuation" || call.Node.ID == "end" {
					accepted = true
					cancel()
				}
				return a, nil
			})
			got, err := r.Run(ctx, req)
			if when == "after terminal" {
				if err != nil || got.DeviceStatus != constants.ExecutionSucceeded || calls != 4 {
					t.Fatalf("terminal changed: %+v %v", got, err)
				}
			} else {
				checkHandoff(t, got, err)
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				want := 0
				if when == "after continuation" {
					want = 1
				}
				if calls != want || got.LastAttempt.Finished != (want == 1) {
					t.Fatalf("handoff: %+v calls=%d", got, calls)
				}
			}
		})
	}
	for _, status := range []string{constants.ExecutionFailed, constants.ExecutionTimedOut, constants.ExecutionCancelled} {
		t.Run(status, func(t *testing.T) {
			r := makeRunner(t, document(t))
			r.attempts = attemptFunc(func(_ context.Context, call attempts.Request) (attempts.Result, error) {
				a := success(call, 1)
				a.NextNodeID = ""
				a.Outcome = outcomes.Result{Status: status, Stop: true}
				if status != constants.ExecutionCancelled {
					a.Outcome.Error = &xerror.CommandError{Code: constants.NodeTimedOut, Message: constants.NodeTimedOutMessage}
				}
				return a, nil
			})
			got, err := r.Run(t.Context(), request())
			want := constants.ExecutionFailed
			if status == constants.ExecutionCancelled {
				want = status
			}
			if err != nil || got.DeviceStatus != want {
				t.Fatalf("terminal projection: %+v %v", got, err)
			}
		})
	}
}

func TestRetryWaitIdentityAndCancellation(t *testing.T) {
	t.Parallel()
	for _, stop := range []string{"none", "during wait", "after wait"} {
		t.Run(stop, func(t *testing.T) {
			doc := document(t)
			doc.Nodes[1].RetryPolicy = ir.RetryPolicy{MaxAttempts: 2, DelayMS: 1000}
			r := makeRunner(t, doc)
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				req := request()
				var failedAt time.Time
				var failedID string
				producerCalls := 0
				req.Control = func() attempts.Control {
					return attempts.Control{GroupCancellation: stop == "after wait" && !failedAt.IsZero() && !time.Now().Before(failedAt.Add(time.Second))}
				}
				r.attempts = attemptFunc(func(_ context.Context, call attempts.Request) (attempts.Result, error) {
					a := success(call, 1)
					if call.Node.ID == "producer" {
						producerCalls++
						if producerCalls == 1 {
							failedAt = time.Now()
							failedID = call.Ref.AttemptID
							a.Outcome = outcomes.Result{Status: constants.ExecutionFailed, Error: &xerror.CommandError{Code: constants.CommandStartFailed, Message: "Command could not start", Retryable: true}}
							a.NextNodeID = "producer"
							a.Retry = &execution.RetryDecision{MaxAttempts: 2, DelayMS: 1000}
							if stop == "during wait" {
								time.AfterFunc(500*time.Millisecond, cancel)
							}
						} else if call.AttemptNumber != 2 || call.PreviousAttemptID != failedID || call.Ref.AttemptID == failedID || time.Since(failedAt) != time.Second {
							t.Fatalf("retry ordering/identity: %+v elapsed=%v", call, time.Since(failedAt))
						}
					}
					return a, nil
				})
				got, err := r.Run(ctx, req)
				if stop == "none" {
					if err != nil || got.DeviceStatus != constants.ExecutionSucceeded || producerCalls != 2 {
						t.Fatalf("retry: %+v %v", got, err)
					}
				} else {
					checkHandoff(t, got, err)
					if !errors.Is(err, context.Canceled) || producerCalls != 1 || !got.LastAttempt.Finished || got.NextNodeID != "producer" {
						t.Fatalf("wait handoff: %+v %v", got, err)
					}
				}
			})
		})
	}
}
