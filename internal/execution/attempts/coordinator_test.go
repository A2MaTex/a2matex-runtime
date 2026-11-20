package attempts

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/routing"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type checkpointStub struct {
	begin            func(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error)
	finish           func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error)
	begins, finishes int
	last             execution.FinishAttemptRequest
}

func (s *checkpointStub) BeginAttempt(ctx context.Context, r execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
	s.begins++
	if s.begin != nil {
		return s.begin(ctx, r)
	}
	return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "3"}, nil
}
func (s *checkpointStub) FinishAttempt(ctx context.Context, r execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	s.finishes++
	s.last = r
	if s.finish != nil {
		return s.finish(ctx, r)
	}
	return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "5"}, nil
}

func requestFor(kind string) Request {
	n := ir.Node{ID: "node", Type: kind, RuntimeHandler: kind, Config: ir.Object{}, Inputs: ir.Object{},
		TimeoutMS: 1000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop}
	return Request{Ref: execution.AttemptRef{RunGroupID: uuid.NewString(), DeviceExecutionID: uuid.NewString(), AttemptID: uuid.NewString(), NodeID: n.ID},
		Node: n, ExpectedDeviceStatus: constants.ExecutionQueued, AttemptNumber: 1, ADBSerial: "emulator-5554"}
}
func dependencies(t *testing.T, r Request, handler registry.Handler, checkpoints Checkpoints) Dependencies {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
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
	h, err := registry.New(catalog, []registry.Registration{{Type: r.Node.Type, RuntimeHandler: r.Node.RuntimeHandler, Run: handler}})
	if err != nil {
		t.Fatal(err)
	}
	i, err := invocation.New(h)
	if err != nil {
		t.Fatal(err)
	}
	doc := ir.Document{Nodes: []ir.Node{r.Node}}
	if r.Node.Type != constants.EndNodeType {
		doc.Nodes = append(doc.Nodes, ir.Node{ID: "end", Type: constants.EndNodeType, ErrorPolicy: constants.ErrorPolicyStop})
		ports := []string{constants.NodeSuccessPort}
		if r.Node.Type == constants.SwitchNodeType {
			var rules []struct{ ID string }
			if err := json.Unmarshal(r.Node.Config["rules"], &rules); err != nil {
				t.Fatal(err)
			}
			ports = []string{"default"}
			for _, rule := range rules {
				ports = append(ports, rule.ID)
			}
		}
		for _, port := range ports {
			doc.Edges = append(doc.Edges, ir.Edge{SourceNodeID: r.Node.ID, SourcePort: port, TargetNodeID: "end"})
		}
		if r.Node.ErrorPolicy == constants.ErrorPolicyErrorBranch {
			doc.Edges = append(doc.Edges, ir.Edge{SourceNodeID: r.Node.ID, SourcePort: constants.NodeErrorPort, TargetNodeID: "end"})
		}
	}
	routes, err := routing.New(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	return Dependencies{Bindings: b, Invoker: i, Outputs: o, Routes: routes, Checkpoints: checkpoints}
}
func coordinator(t *testing.T, d Dependencies) *Coordinator {
	t.Helper()
	c, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func emptyHandler(context.Context, registry.Invocation) (map[string]any, error) {
	return map[string]any{}, nil
}
func assertUnreleased(t *testing.T, r Result, began bool) {
	t.Helper()
	if r.Began != began || r.Finished || r.Cursor != "" || r.NextNodeID != "" || r.Retry != nil || r.Outcome.Status != "" || r.Outcome.Output != nil || r.Outcome.Error != nil || r.Outcome.Stop ||
		(began && r.Sequence != 1) || (!began && r.Sequence != 0) {
		t.Fatalf("unconfirmed result released data: %+v", r)
	}
}

func TestForeignVariableViewRejectedBeforeBegin(t *testing.T) {
	for _, foreignGroup := range []bool{true, false} {
		r := requestFor("flow.start")
		r.Values.Variables = bindings.VariableView{RunGroupID: r.Ref.RunGroupID, DeviceExecutionID: r.Ref.DeviceExecutionID,
			Lookup: func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error) {
				t.Fatal("foreign view read")
				return nil, false, nil
			}}
		if foreignGroup {
			r.Values.Variables.RunGroupID = uuid.NewString()
		} else {
			r.Values.Variables.DeviceExecutionID = uuid.NewString()
		}
		checkpoints := &checkpointStub{}
		c := coordinator(t, dependencies(t, r, emptyHandler, checkpoints))
		got, err := c.Run(t.Context(), r)
		if !errors.Is(err, xerror.ErrAttemptInvalid) || checkpoints.begins != 0 {
			t.Fatal(got, err)
		}
		assertUnreleased(t, got, false)
	}
}

func TestNewAndRunGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*Dependencies)
	}{
		{"bindings", func(d *Dependencies) { d.Bindings = nil }}, {"invoker", func(d *Dependencies) { d.Invoker = nil }},
		{"outputs", func(d *Dependencies) { d.Outputs = nil }}, {"routes", func(d *Dependencies) { d.Routes = nil }}, {"checkpoints", func(d *Dependencies) { d.Checkpoints = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := dependencies(t, requestFor("flow.start"), emptyHandler, &checkpointStub{})
			tc.change(&d)
			c, err := New(d)
			if c != nil || !errors.Is(err, xerror.ErrAttemptInvalid) {
				t.Fatalf("constructor: %v, %v", c, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*Request)
	}{
		{"identity", func(r *Request) { r.Ref.AttemptID = "private invalid ID" }}, {"node pairing", func(r *Request) { r.Node.ID = "other" }},
		{"device status", func(r *Request) { r.ExpectedDeviceStatus = constants.ExecutionSucceeded }},
		{"attempt zero", func(r *Request) { r.AttemptNumber = 0 }}, {"attempt exhausted", func(r *Request) { r.AttemptNumber = 2 }},
		{"overflow policy", func(r *Request) { r.Node.RetryPolicy.MaxAttempts = math.MaxInt64 }},
		{"negative delay", func(r *Request) { r.Node.RetryPolicy.DelayMS = -1 }}, {"first predecessor", func(r *Request) { r.PreviousAttemptID = uuid.NewString() }},
		{"missing predecessor", func(r *Request) { r.Node.RetryPolicy.MaxAttempts = 2; r.AttemptNumber = 2 }},
		{"self predecessor", func(r *Request) {
			r.Node.RetryPolicy.MaxAttempts = 2
			r.AttemptNumber = 2
			r.PreviousAttemptID = r.Ref.AttemptID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := requestFor("flow.start")
			s := &checkpointStub{}
			calls := 0
			c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
				calls++
				return map[string]any{}, nil
			}, s))
			tc.change(&r)
			got, err := c.Run(t.Context(), r)
			if !errors.Is(err, xerror.ErrAttemptInvalid) || s.begins != 0 || calls != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("guard: %+v %v", got, err)
			}
			assertUnreleased(t, got, false)
		})
	}
	for _, kind := range []string{constants.WaitVariableNodeType, constants.LogNodeType} {
		t.Run(kind, func(t *testing.T) {
			r := requestFor("flow.start")
			s := &checkpointStub{}
			c := coordinator(t, dependencies(t, r, emptyHandler, s))
			r.Node.Type = kind
			got, err := c.Run(t.Context(), r)
			if !errors.Is(err, xerror.ErrAttemptInvalid) || s.begins != 0 {
				t.Fatalf("scope: %+v %v", got, err)
			}
		})
	}
	for _, c := range []*Coordinator{nil, {}} {
		if _, err := c.Run(t.Context(), requestFor("flow.start")); !errors.Is(err, xerror.ErrAttemptInvalid) {
			t.Fatal(err)
		}
	}
}

func TestCheckpointDispositionsNeverAuthorizeUnconfirmedWork(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"begin", "finish"} {
		for _, tc := range []struct {
			name   string
			result execution.CheckpointResult
			err    error
		}{
			{"stale", execution.CheckpointResult{Disposition: constants.CheckpointStale}, nil},
			{"already finalized", execution.CheckpointResult{Disposition: constants.CheckpointAlreadyFinalized, Sequence: 1}, nil},
			{"invalid cursor", execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "private cursor"}, nil},
			{"zero sequence", execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Cursor: "1"}, nil},
			{"zero cursor", execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "0"}, nil},
			{"unknown commit", execution.CheckpointResult{}, xerror.ErrExecutionCommitUnknown},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				r := requestFor("flow.start")
				s := &checkpointStub{}
				calls := 0
				if phase == "begin" {
					s.begin = func(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
						return tc.result, tc.err
					}
				} else {
					s.finish = func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
						return tc.result, tc.err
					}
				}
				c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
					calls++
					return map[string]any{}, nil
				}, s))
				got, err := c.Run(t.Context(), r)
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatalf("disposition: %+v %v", got, err)
				}
				assertUnreleased(t, got, phase == "finish")
				wantCalls := 0
				if phase == "finish" {
					wantCalls = 1
				}
				if calls != wantCalls || s.finishes != wantCalls {
					t.Fatalf("calls=%d finishes=%d", calls, s.finishes)
				}
			})
		}
	}
	r := requestFor("flow.start")
	s := &checkpointStub{finish: func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
		return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 2, Cursor: "6"}, nil
	}}
	got, err := coordinator(t, dependencies(t, r, emptyHandler, s)).Run(t.Context(), r)
	if err == nil {
		t.Fatal("mismatched sequence accepted")
	}
	assertUnreleased(t, got, true)
}

func TestCancellationBoundaries(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"entry", "preparation", "after begin", "handler", "freeze"} {
		for _, accepted := range []bool{false, true} {
			t.Run(when+"/accepted="+map[bool]string{false: "false", true: "true"}[accepted], func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				r := requestFor("flow.start")
				s := &checkpointStub{}
				calls, reads := 0, 0
				stopped := false
				stop := func() {
					stopped = true
					if !accepted {
						cancel()
					}
				}
				deadline := time.Now().Add(time.Minute)
				r.Control = func() Control {
					reads++
					if (when == "entry" && reads == 1) || (when == "preparation" && reads == 2) || (when == "freeze" && reads == 5) {
						stop()
					}
					return Control{GroupCancellation: accepted && stopped, ShutdownDeadline: deadline}
				}
				if when == "after begin" {
					s.begin = func(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
						stop()
						return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "3"}, nil
					}
				}
				c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
					calls++
					if when == "handler" {
						stop()
					}
					return map[string]any{}, nil
				}, s))
				got, err := c.Run(ctx, r)
				beforeBegin := when == "entry" || when == "preparation"
				if beforeBegin || !accepted {
					if !errors.Is(err, context.Canceled) || s.finishes != 0 {
						t.Fatalf("cancel handoff: %+v %v finishes=%d", got, err, s.finishes)
					}
					assertUnreleased(t, got, !beforeBegin)
				} else if err != nil || !got.Finished || got.Outcome.Status != constants.ExecutionCancelled || !got.Outcome.Stop || got.Outcome.Output != nil || got.Outcome.Error != nil || s.last.NextNodeID != "" || s.last.Retry != nil || !s.last.GroupCancellation || s.last.ShutdownDeadline != deadline {
					t.Fatalf("accepted stop: %+v %+v %v", got, s.last, err)
				}
				wantCalls := 0
				if when == "handler" || when == "freeze" {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("dispatches=%d want=%d", calls, wantCalls)
				}
			})
		}
	}
}

func TestParentDeadlineAndInfrastructurePriority(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		r := requestFor("flow.start")
		r.Node.TimeoutMS = 2000
		s := &checkpointStub{}
		c := coordinator(t, dependencies(t, r, func(ctx context.Context, _ registry.Invocation) (map[string]any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, s))
		got, err := c.Run(ctx, r)
		if err != nil || got.Outcome.Status != constants.ExecutionTimedOut || !got.Outcome.Stop || got.NextNodeID != "" || s.finishes != 1 {
			t.Fatalf("parent deadline: %+v %v", got, err)
		}
	})
	for _, tc := range []struct {
		name     string
		cause    error
		code     string
		finishes int
	}{
		{"internal", errors.New("private handler text"), constants.InternalError, 1},
		{"persistence", xerror.ErrExecutionPersistence, constants.PersistenceFailed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := requestFor("flow.start")
			accepted := false
			r.Control = func() Control { return Control{GroupCancellation: accepted} }
			s := &checkpointStub{}
			c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
				accepted = true
				return nil, tc.cause
			}, s))
			got, err := c.Run(t.Context(), r)
			if s.finishes != tc.finishes {
				t.Fatalf("finishes=%d", s.finishes)
			}
			if tc.finishes == 0 {
				if !errors.Is(err, xerror.ErrExecutionPersistence) {
					t.Fatal(err)
				}
				assertUnreleased(t, got, true)
			} else if err != nil || got.Outcome.Error == nil || got.Outcome.Error.Code != tc.code || !got.Outcome.Stop || !s.last.GroupCancellation || strings.Contains(string(s.last.Error), "private") {
				t.Fatalf("priority: %+v %v", got, err)
			}
		})
	}
}

func TestRouteInvariantStopsWithoutRoutingItsOwnFailure(t *testing.T) {
	t.Parallel()
	r := requestFor("flow.start")
	s := &checkpointStub{}
	d := dependencies(t, r, emptyHandler, s)
	other := requestFor("flow.end")
	other.Node.ID = "other"
	d.Routes = dependencies(t, other, emptyHandler, s).Routes
	got, err := coordinator(t, d).Run(t.Context(), r)
	if err != nil || got.Outcome.Error == nil || got.Outcome.Error.Code != constants.InternalError || !got.Outcome.Stop || got.NextNodeID != "" {
		t.Fatalf("route invariant: %+v %v", got, err)
	}
}

func TestWholeEventCeilingAndBackwardClock(t *testing.T) {
	t.Parallel()
	r := requestFor("flow.start")
	s := &checkpointStub{}
	d := dependencies(t, r, emptyHandler, s)
	now := time.Now()
	reads := 0
	d.Now = func() time.Time {
		reads++
		if reads == 1 {
			return now
		}
		return now.Add(-time.Hour)
	}
	got, err := coordinator(t, d).Run(t.Context(), r)
	if err != nil || !got.Finished || !s.last.Now.Equal(now) {
		t.Fatalf("clock clamp: %+v %v", got, err)
	}
	// G3 accepts this string schema; a real Screenshot also requires its owned stage.
	// No production catalog extension is needed to exercise the A6 projection.
	large := map[string]any{"artifactId": strings.Repeat("x", constants.MaxEventBytes-100)}
	checked, err := d.Outputs.Validate(t.Context(), constants.ScreenshotNodeType, large)
	if err != nil || len(checked) >= constants.MaxEventBytes {
		t.Fatalf("G3 ceiling fixture: %d %v", len(checked), err)
	}
	if successFits(r, now, checked) {
		t.Fatal("output-only ceiling bypassed full event ceiling")
	}
	if !successFits(r, now, json.RawMessage(`{}`)) {
		t.Fatal("small success event rejected")
	}
	// Exercise completion with the string schema only, without invoking a handler
	// or submitting synthetic artifact metadata to a checkpoint.
	r.Node.Type = constants.ScreenshotNodeType
	finish, outcome, err := coordinator(t, d).completion(t.Context(), r, invocation.Observation{Output: large}, nil, now)
	if err != nil || outcome.Status != constants.ExecutionFailed || outcome.Error == nil ||
		outcome.Error.Code != constants.NodeOutputInvalid || outcome.Error.Retryable || outcome.Output != nil ||
		finish.NextNodeID != "" || finish.Retry != nil {
		t.Fatalf("oversized success was not converted to a safe failure: %+v %+v %v", outcome, finish, err)
	}
}
