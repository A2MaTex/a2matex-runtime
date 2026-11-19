package routing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/routing"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// These fixtures contain routing metadata, not complete admission requests.
func workflow() ir.Document {
	return ir.Document{
		Nodes: []ir.Node{
			{ID: "start", Type: "flow.start", ErrorPolicy: constants.ErrorPolicyStop},
			{ID: "launch", Type: "android.launch_app", ErrorPolicy: constants.ErrorPolicyErrorBranch},
			{ID: "tap", Type: "android.tap", ErrorPolicy: constants.ErrorPolicyStop},
			{ID: "back", Type: "android.press_back", ErrorPolicy: constants.ErrorPolicyStop},
			{ID: "end", Type: constants.EndNodeType, ErrorPolicy: constants.ErrorPolicyStop},
		},
		Edges: []ir.Edge{
			edge("start", constants.NodeSuccessPort, "launch"),
			edge("launch", constants.NodeSuccessPort, "tap"),
			edge("launch", constants.NodeErrorPort, "back"),
			edge("tap", constants.NodeSuccessPort, "end"),
			edge("back", constants.NodeSuccessPort, "end"),
		},
	}
}

func edge(source, port, target string) ir.Edge {
	return ir.Edge{ID: source + "_" + port, SourceNodeID: source, SourcePort: port, TargetNodeID: target}
}

func newRouter(t *testing.T, document ir.Document) *routing.Router {
	t.Helper()
	r, err := routing.New(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func checkFailure(t *testing.T, err error, operation, reason string) {
	t.Helper()
	var failure *xerror.OperationError
	var command xerror.CommandError
	if !errors.Is(err, xerror.ErrRoutingInvalid) || !errors.As(err, &failure) ||
		failure.Operation != operation || failure.Detail != reason || errors.As(err, &command) {
		t.Fatalf("unexpected routing failure: %v (%+v)", err, failure)
	}
	if err.Error() != operation+": workflow routing state is invalid" {
		t.Fatalf("unsafe error text: %v", err)
	}
}

func TestNewRejectsAmbiguousRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		change       func(*ir.Document)
	}{
		{"empty nodes", "routing_document_invalid", func(d *ir.Document) { d.Nodes = nil }},
		{"too many nodes", "routing_document_invalid", func(d *ir.Document) {
			d.Nodes = make([]ir.Node, constants.RuntimeIRMaxNodes+1)
		}},
		{"too many edges", "routing_document_invalid", func(d *ir.Document) {
			d.Edges = make([]ir.Edge, constants.RuntimeIRMaxEdges+1)
		}},
		{"empty node id", "routing_document_invalid", func(d *ir.Document) { d.Nodes[0].ID = "" }},
		{"invalid node id", "routing_document_invalid", func(d *ir.Document) { d.Nodes[0].ID = "protected value\n" }},
		{"duplicate node", "routing_document_invalid", func(d *ir.Document) { d.Nodes[1].ID = "start" }},
		{"unknown policy", "routing_document_invalid", func(d *ir.Document) { d.Nodes[1].ErrorPolicy = "protected policy" }},
		{"policy case", "routing_document_invalid", func(d *ir.Document) { d.Nodes[0].ErrorPolicy = "stop" }},
		{"missing source", "routing_edges_invalid", func(d *ir.Document) { d.Edges[0].SourceNodeID = "protected source" }},
		{"missing target", "routing_edges_invalid", func(d *ir.Document) { d.Edges[0].TargetNodeID = "protected target" }},
		{"self edge", "routing_edges_invalid", func(d *ir.Document) { d.Edges[0].TargetNodeID = "start" }},
		{"duplicate port same target", "routing_edges_invalid", func(d *ir.Document) { d.Edges = append(d.Edges, d.Edges[1]) }},
		{"duplicate port different target", "routing_edges_invalid", func(d *ir.Document) {
			d.Edges = append(d.Edges, edge("launch", constants.NodeSuccessPort, "back"))
		}},
		{"unknown port", "routing_edges_invalid", func(d *ir.Document) {
			d.Nodes[0].Type = "protected node type"
			d.Edges[0].SourcePort = "protected port"
		}},
		{"port case", "routing_edges_invalid", func(d *ir.Document) { d.Edges[0].SourcePort = "Success" }},
		{"missing success", "routing_edges_invalid", func(d *ir.Document) { d.Edges = d.Edges[1:] }},
		{"missing required error", "routing_edges_invalid", func(d *ir.Document) { d.Edges = slices.Delete(d.Edges, 2, 3) }},
		{"stop with error", "routing_edges_invalid", func(d *ir.Document) { d.Nodes[1].ErrorPolicy = constants.ErrorPolicyStop }},
		{"end with edge", "routing_edges_invalid", func(d *ir.Document) {
			d.Edges = append(d.Edges, edge("end", constants.NodeSuccessPort, "tap"))
		}},
		{"end error policy", "routing_edges_invalid", func(d *ir.Document) { d.Nodes[4].ErrorPolicy = constants.ErrorPolicyErrorBranch }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			document := workflow()
			tc.change(&document)
			r, err := routing.New(context.Background(), document)
			if r != nil {
				t.Fatal("constructor returned a partial router")
			}
			checkFailure(t, err, "configure_workflow_routing", tc.reason)
		})
	}
}

func TestNewAtNodeLimit(t *testing.T) {
	t.Parallel()
	document := ir.Document{Nodes: make([]ir.Node, constants.RuntimeIRMaxNodes)}
	for i := range document.Nodes {
		id := fmt.Sprintf("node_%d", i)
		document.Nodes[i] = ir.Node{ID: id, Type: "flow.delay", ErrorPolicy: constants.ErrorPolicyStop}
		if i > 0 {
			document.Edges = append(document.Edges, edge(document.Nodes[i-1].ID, constants.NodeSuccessPort, id))
		}
	}
	document.Nodes[len(document.Nodes)-1].Type = constants.EndNodeType
	r := newRouter(t, document)
	for _, node := range document.Nodes {
		decision, err := r.Select(context.Background(), node.ID, constants.ExecutionSucceeded)
		if err != nil {
			t.Fatal(err)
		}
		if node.Type == constants.EndNodeType {
			if decision != (routing.Decision{DeviceStatus: constants.ExecutionSucceeded}) {
				t.Fatalf("unexpected terminal decision: %+v", decision)
			}
		} else if decision.NextNodeID == "" || decision.DeviceStatus != constants.ExecutionRunning {
			t.Fatalf("unexpected continuation: %+v", decision)
		}
	}
}

func TestSelectDecisions(t *testing.T) {
	t.Parallel()
	r := newRouter(t, workflow())
	for _, tc := range []struct {
		name, nodeID, status string
		want                 routing.Decision
	}{
		{"start success", "start", constants.ExecutionSucceeded, routing.Decision{NextNodeID: "launch", DeviceStatus: constants.ExecutionRunning}},
		{"error branch node success", "launch", constants.ExecutionSucceeded, routing.Decision{NextNodeID: "tap", DeviceStatus: constants.ExecutionRunning}},
		{"error branch failure", "launch", constants.ExecutionFailed, routing.Decision{NextNodeID: "back", DeviceStatus: constants.ExecutionRunning}},
		{"error branch timeout", "launch", constants.ExecutionTimedOut, routing.Decision{NextNodeID: "back", DeviceStatus: constants.ExecutionRunning}},
		{"ordinary success", "tap", constants.ExecutionSucceeded, routing.Decision{NextNodeID: "end", DeviceStatus: constants.ExecutionRunning}},
		{"stop failure", "tap", constants.ExecutionFailed, routing.Decision{DeviceStatus: constants.ExecutionFailed}},
		{"stop timeout", "tap", constants.ExecutionTimedOut, routing.Decision{DeviceStatus: constants.ExecutionFailed}},
		{"end success", "end", constants.ExecutionSucceeded, routing.Decision{DeviceStatus: constants.ExecutionSucceeded}},
		{"end failure", "end", constants.ExecutionFailed, routing.Decision{DeviceStatus: constants.ExecutionFailed}},
		{"end timeout", "end", constants.ExecutionTimedOut, routing.Decision{DeviceStatus: constants.ExecutionFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision, err := r.Select(context.Background(), tc.nodeID, tc.status)
			if err != nil || decision != tc.want {
				t.Fatalf("Select: %+v, %v; want %+v", decision, err, tc.want)
			}
		})
	}
}

func TestHandledFailureCanReachSuccessfulEnd(t *testing.T) {
	t.Parallel()
	r := newRouter(t, workflow())
	nodeID := "start"
	for _, step := range []struct{ nodeID, outcome, next string }{
		{"start", constants.ExecutionSucceeded, "launch"},
		{"launch", constants.ExecutionFailed, "back"},
		{"back", constants.ExecutionSucceeded, "end"},
		{"end", constants.ExecutionSucceeded, ""},
	} {
		if nodeID != step.nodeID {
			t.Fatalf("reached %q, want %q", nodeID, step.nodeID)
		}
		decision, err := r.Select(context.Background(), nodeID, step.outcome)
		want := routing.Decision{NextNodeID: step.next, DeviceStatus: constants.ExecutionRunning}
		if step.next == "" {
			want.DeviceStatus = constants.ExecutionSucceeded
		}
		if err != nil || decision != want {
			t.Fatalf("handled path: %+v, %v; want %+v", decision, err, want)
		}
		nodeID = decision.NextNodeID
	}
}

func TestSelectFailures(t *testing.T) {
	t.Parallel()
	r := newRouter(t, workflow())
	for _, tc := range []struct{ name, nodeID, status, reason string }{
		{"unknown node", "protected node", constants.ExecutionSucceeded, "routing_node_unavailable"},
		{"node case", "Launch", constants.ExecutionSucceeded, "routing_node_unavailable"},
		{"node whitespace", "launch ", constants.ExecutionSucceeded, "routing_node_unavailable"},
		{"running", "launch", constants.ExecutionRunning, "routing_outcome_unsupported"},
		{"cancelled", "launch", constants.ExecutionCancelled, "routing_outcome_unsupported"},
		{"interrupted", "launch", constants.ExecutionInterrupted, "routing_outcome_unsupported"},
		{"queued", "launch", constants.ExecutionQueued, "routing_outcome_unsupported"},
		{"paused", "launch", constants.ExecutionPaused, "routing_outcome_unsupported"},
		{"empty status", "launch", "", "routing_outcome_unsupported"},
		{"unknown status", "launch", "protected status", "routing_outcome_unsupported"},
		{"status case", "launch", "succeeded", "routing_outcome_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision, err := r.Select(context.Background(), tc.nodeID, tc.status)
			if decision != (routing.Decision{}) {
				t.Fatal("failure returned a usable decision")
			}
			checkFailure(t, err, "select_workflow_route", tc.reason)
		})
	}
	for _, tc := range []struct {
		name   string
		router *routing.Router
	}{
		{name: "nil router"},
		{name: "zero router", router: &routing.Router{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := tc.router.Select(context.Background(), "launch", constants.ExecutionSucceeded)
			if decision != (routing.Decision{}) {
				t.Fatal("uninitialized router returned a decision")
			}
			checkFailure(t, err, "select_workflow_route", "routing_state_invalid")
		})
	}
}

func TestSwitchRoutesAndGuards(t *testing.T) {
	const rule = "bfbb31bc-d4b5-44ec-b9eb-a09d68e74083"
	doc := workflow()
	doc.Nodes[1].Type = constants.SwitchNodeType
	doc.Nodes[1].Config = ir.Object{"rules": []byte(`[{"id":"` + rule + `","operator":"EQ"}]`)}
	doc.Edges[1].SourcePort = rule
	doc.Edges = append(doc.Edges, edge("launch", "default", "end"))
	r := newRouter(t, doc)
	for _, tc := range []struct{ status, port, next, deviceStatus string }{
		{constants.ExecutionSucceeded, rule, "tap", constants.ExecutionRunning},
		{constants.ExecutionSucceeded, "default", "end", constants.ExecutionRunning},
		{constants.ExecutionFailed, "", "back", constants.ExecutionRunning},
		{constants.ExecutionTimedOut, "", "back", constants.ExecutionRunning},
	} {
		got, err := r.SelectSwitch(t.Context(), "launch", tc.status, tc.port)
		if err != nil || got.NextNodeID != tc.next || got.DeviceStatus != tc.deviceStatus {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
	}
	for _, port := range []string{"", "success", "error", "unknown"} {
		if _, err := r.SelectSwitch(t.Context(), "launch", constants.ExecutionSucceeded, port); err == nil {
			t.Fatalf("accepted port %q", port)
		}
	}
	for _, status := range []string{constants.ExecutionCancelled, constants.ExecutionInterrupted, constants.ExecutionRunning} {
		if _, err := r.SelectSwitch(t.Context(), "launch", status, rule); err == nil {
			t.Fatalf("accepted status %s", status)
		}
	}
	if _, err := r.SelectSwitch(t.Context(), "start", constants.ExecutionSucceeded, "default"); err == nil {
		t.Fatal("ordinary node accepted Switch selection")
	}
	for _, bad := range []string{"missing default", "unknown port", "duplicate port", "missing rules"} {
		d := doc
		d.Nodes = append([]ir.Node(nil), doc.Nodes...)
		d.Edges = append([]ir.Edge(nil), doc.Edges...)
		switch bad {
		case "missing default":
			d.Edges = d.Edges[:len(d.Edges)-1]
		case "unknown port":
			d.Edges[1].SourcePort = "unknown"
		case "duplicate port":
			d.Edges = append(d.Edges, d.Edges[1])
		case "missing rules":
			d.Nodes[1].Config = nil
		}
		if _, err := routing.New(t.Context(), d); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	doc.Nodes[1].ErrorPolicy = constants.ErrorPolicyStop
	doc.Edges = append(doc.Edges[:2:2], doc.Edges[3:]...)
	r = newRouter(t, doc)
	got, err := r.SelectSwitch(t.Context(), "launch", constants.ExecutionFailed, "")
	if err != nil || got.NextNodeID != "" || got.DeviceStatus != constants.ExecutionFailed {
		t.Fatalf("STOP: %+v %v", got, err)
	}
}

func TestOrdinarySelectionRejectsSwitch(t *testing.T) {
	t.Parallel()
	document := workflow()
	document.Nodes[1].Type = constants.SwitchNodeType
	document.Nodes[1].Config = ir.Object{"rules": []byte(`[{"id":"bfbb31bc-d4b5-44ec-b9eb-a09d68e74083","operator":"LT"}]`)}
	document.Edges[1].SourcePort = "bfbb31bc-d4b5-44ec-b9eb-a09d68e74083"
	document.Edges = append(document.Edges, edge("launch", "default", "tap"))
	r := newRouter(t, document)
	for _, status := range []string{
		constants.ExecutionSucceeded, constants.ExecutionFailed, constants.ExecutionTimedOut,
		constants.ExecutionCancelled, constants.ExecutionInterrupted, constants.ExecutionRunning, "",
	} {
		t.Run("outcome "+status, func(t *testing.T) {
			decision, err := r.Select(context.Background(), "launch", status)
			if decision != (routing.Decision{}) {
				t.Fatal("ordinary routing selected a Switch branch")
			}
			checkFailure(t, err, "select_workflow_route", "routing_switch_unsupported")
		})
	}
	document.Edges = append(document.Edges, document.Edges[1])
	partial, err := routing.New(context.Background(), document)
	if partial != nil {
		t.Fatal("duplicate Switch port produced a router")
	}
	checkFailure(t, err, "configure_workflow_routing", "routing_edges_invalid")
}

func TestRouteOwnershipAndIsolation(t *testing.T) {
	t.Parallel()
	document := workflow()
	r := newRouter(t, document)
	document.Nodes[1].Type = constants.EndNodeType
	document.Nodes[1].ErrorPolicy = constants.ErrorPolicyStop
	document.Edges[1].TargetNodeID = "back"
	slices.Reverse(document.Nodes)
	slices.Reverse(document.Edges)
	otherDocument := workflow()
	otherDocument.Edges[1].TargetNodeID = "back"
	otherDocument.Nodes[1].Type = "future.ordinary_handler"
	slices.Reverse(otherDocument.Nodes)
	slices.Reverse(otherDocument.Edges)
	other := newRouter(t, otherDocument)
	for i := range 32 {
		t.Run(fmt.Sprintf("reader %d", i), func(t *testing.T) {
			t.Parallel()
			got, err := r.Select(context.Background(), "launch", constants.ExecutionSucceeded)
			want := routing.Decision{NextNodeID: "tap", DeviceStatus: constants.ExecutionRunning}
			if err != nil || got != want {
				t.Fatalf("source mutation or shared state changed route: %+v, %v", got, err)
			}
			got.NextNodeID = "caller mutation"
			got, err = other.Select(context.Background(), "launch", constants.ExecutionSucceeded)
			want.NextNodeID = "back"
			if err != nil || got != want {
				t.Fatalf("workflow isolation or generic node type failed: %+v, %v", got, err)
			}
			got, err = other.Select(context.Background(), "launch", constants.ExecutionFailed)
			if err != nil || got != want {
				t.Fatalf("success and error ports sharing target: %+v, %v", got, err)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	document := workflow()
	r := newRouter(t, document)
	// Sweep every observable context checkpoint without sleeps or scheduler races.
	checks := 2 + 2*len(document.Nodes) + len(document.Edges)
	for at := 1; at <= checks; at++ {
		ctx, cancel := context.WithCancel(context.Background())
		controlled := &cancelOnCheck{Context: ctx, cancel: cancel, at: int32(at)}
		partial, err := routing.New(controlled, document)
		cancel()
		if partial != nil {
			t.Fatalf("constructor check %d returned a partial router", at)
		}
		checkContextError(t, err, "configure_workflow_routing", context.Canceled)
	}
	for _, tc := range []struct{ name, nodeID, status string }{
		{"success", "launch", constants.ExecutionSucceeded},
		{"terminal", "end", constants.ExecutionSucceeded},
		{"unknown node", "protected node", constants.ExecutionSucceeded},
		{"unsupported outcome", "launch", constants.ExecutionCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			controlled := &cancelOnCheck{Context: ctx, cancel: cancel, at: 2}
			decision, err := r.Select(controlled, tc.nodeID, tc.status)
			if decision != (routing.Decision{}) {
				t.Fatal("cancelled selection returned a decision")
			}
			checkContextError(t, err, "select_workflow_route", context.Canceled)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	controlled := &cancelOnCheck{Context: ctx, cancel: cancel, at: 2}
	partial, err := routing.New(controlled, ir.Document{})
	cancel()
	if partial != nil {
		t.Fatal("cancelled invalid document produced a router")
	}
	checkContextError(t, err, "configure_workflow_routing", context.Canceled)
	preCancelled, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelDeadline()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		cause error
	}{
		{"pre-cancelled", preCancelled, context.Canceled},
		{"expired deadline", expired, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			partial, err := routing.New(tc.ctx, document)
			if partial != nil {
				t.Fatal("inactive context produced a router")
			}
			checkContextError(t, err, "configure_workflow_routing", tc.cause)
			decision, err := r.Select(tc.ctx, "launch", constants.ExecutionSucceeded)
			if decision != (routing.Decision{}) {
				t.Fatal("inactive context produced a decision")
			}
			checkContextError(t, err, "select_workflow_route", tc.cause)
		})
	}
}

func checkContextError(t *testing.T, err error, operation string, cause error) {
	t.Helper()
	var failure *xerror.OperationError
	if !errors.Is(err, cause) || errors.Is(err, xerror.ErrRoutingInvalid) ||
		!errors.As(err, &failure) || failure.Operation != operation {
		t.Fatalf("lost context error: %v (%+v)", err, failure)
	}
}

type cancelOnCheck struct {
	context.Context
	cancel context.CancelFunc
	at     int32
	checks atomic.Int32
}

func (c *cancelOnCheck) Err() error {
	if c.checks.Add(1) == c.at {
		c.cancel()
	}
	return c.Context.Err()
}
