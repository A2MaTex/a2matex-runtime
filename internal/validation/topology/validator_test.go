package topology

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

func TestValidateWorkLimits(t *testing.T) {
	t.Parallel()

	atLimits := validGraph()
	atLimits.Nodes = make([]editorgraph.Node, validation.EditorGraphMaxNodes)
	for index := range atLimits.Nodes {
		atLimits.Nodes[index] = testNode(fmt.Sprintf("node_%03d", index), "flow.delay")
	}
	atLimits.Nodes[0] = testNode("start", "flow.start")
	atLimits.Nodes[len(atLimits.Nodes)-1] = testNode("end", "flow.end")
	atLimits.EntryNodeID = "start"
	atLimits.Edges = make([]editorgraph.Edge, validation.EditorGraphMaxEdges)
	for index := range atLimits.Edges {
		atLimits.Edges[index] = editorgraph.Edge{
			ID:           fmt.Sprintf("edge_%04d", index),
			SourceNodeID: "start",
			SourcePort:   "success",
			TargetNodeID: "end",
		}
	}
	result := Validate(atLimits)
	if !result.Continue || hasCode(result.Diagnostics, validation.CodeGraphNodeLimitExceeded) || hasCode(result.Diagnostics, validation.CodeGraphEdgeLimitExceeded) {
		t.Fatalf("exact limits were rejected: %+v", result)
	}

	tests := []struct {
		name      string
		nodeCount int
		edgeCount int
		wantCodes []validation.Code
	}{
		{name: "nodes", nodeCount: validation.EditorGraphMaxNodes + 1, wantCodes: []validation.Code{validation.CodeGraphNodeLimitExceeded}},
		{name: "edges", edgeCount: validation.EditorGraphMaxEdges + 1, wantCodes: []validation.Code{validation.CodeGraphEdgeLimitExceeded}},
		{name: "both", nodeCount: validation.EditorGraphMaxNodes + 1, edgeCount: validation.EditorGraphMaxEdges + 1, wantCodes: []validation.Code{validation.CodeGraphNodeLimitExceeded, validation.CodeGraphEdgeLimitExceeded}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			graph := validGraph()
			if test.nodeCount > 0 {
				graph.Nodes = make([]editorgraph.Node, test.nodeCount)
			}
			if test.edgeCount > 0 {
				graph.Edges = make([]editorgraph.Edge, test.edgeCount)
			}
			got := Validate(graph)
			if got.Continue || !reflect.DeepEqual(diagnosticCodes(got.Diagnostics), test.wantCodes) {
				t.Fatalf("result = %+v, want only %v", got, test.wantCodes)
			}
		})
	}
}

func TestValidateIdentityAndStartRules(t *testing.T) {
	t.Parallel()

	graph := validGraph()
	graph.Nodes = append(graph.Nodes, graph.Nodes[1], graph.Nodes[1])
	graph.Edges = append(graph.Edges, graph.Edges[0], graph.Edges[0])
	result := Validate(graph)
	assertDiagnostic(t, result.Diagnostics, validation.CodeNodeIDDuplicated, validation.Path{"nodes", 2, "id"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeNodeIDDuplicated, validation.Path{"nodes", 3, "id"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeEdgeIDDuplicated, validation.Path{"edges", 1, "id"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeEdgeIDDuplicated, validation.Path{"edges", 2, "id"})

	tests := []struct {
		name  string
		graph *editorgraph.Graph
		codes []validation.Code
	}{
		{name: "one matching start", graph: validGraph()},
		{name: "wrong entry", graph: mutate(validGraph(), func(graph *editorgraph.Graph) { graph.EntryNodeID = "end" }), codes: []validation.Code{validation.CodeEntryNodeInvalid}},
		{name: "no start", graph: mutate(validGraph(), func(graph *editorgraph.Graph) { graph.Nodes[0].Type = "flow.delay" }), codes: []validation.Code{validation.CodeStartNodeCountInvalid, validation.CodeEntryNodeInvalid}},
		{name: "multiple starts", graph: mutate(validGraph(), func(graph *editorgraph.Graph) { graph.Nodes = append(graph.Nodes, testNode("start_2", "flow.start")) }), codes: []validation.Code{validation.CodeStartNodeCountInvalid, validation.CodeEntryNodeInvalid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Validate(test.graph)
			for _, code := range test.codes {
				if !hasCode(got.Diagnostics, code) {
					t.Fatalf("diagnostics = %+v, want %s", got.Diagnostics, code)
				}
			}
			if len(test.codes) == 0 && len(got.Diagnostics) != 0 {
				t.Fatalf("valid graph diagnostics = %+v", got.Diagnostics)
			}
			if !got.Continue {
				t.Fatal("non-limit topology result did not allow C4")
			}
		})
	}
}

func TestValidateEndpointsTerminalAndReachability(t *testing.T) {
	t.Parallel()

	graph := validGraph()
	graph.Edges = append(graph.Edges,
		editorgraph.Edge{ID: "missing_source", SourceNodeID: "unknown", SourcePort: "success", TargetNodeID: "end"},
		editorgraph.Edge{ID: "missing_target", SourceNodeID: "start", SourcePort: "success", TargetNodeID: "unknown"},
		editorgraph.Edge{ID: "missing_both", SourceNodeID: "left", SourcePort: "success", TargetNodeID: "right"},
		editorgraph.Edge{ID: "from_end", SourceNodeID: "end", SourcePort: "success", TargetNodeID: "unknown"},
	)
	result := Validate(graph)
	assertDiagnostic(t, result.Diagnostics, validation.CodeEdgeSourceNodeNotFound, validation.Path{"edges", 1, "sourceNodeId"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeEdgeTargetNodeNotFound, validation.Path{"edges", 2, "targetNodeId"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeEdgeSourceNodeNotFound, validation.Path{"edges", 3, "sourceNodeId"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeEdgeTargetNodeNotFound, validation.Path{"edges", 3, "targetNodeId"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeEndNodeHasOutgoingEdge, validation.Path{"edges", 4, "sourceNodeId"})
	assertDiagnostic(t, result.Diagnostics, validation.CodeEdgeTargetNodeNotFound, validation.Path{"edges", 4, "targetNodeId"})
	if hasCode(result.Diagnostics, validation.CodeEndNodeUnreachable) {
		t.Fatalf("missing-endpoint edges changed valid reachability: %+v", result.Diagnostics)
	}

	disconnected := validGraph()
	disconnected.Nodes = append(disconnected.Nodes, testNode("separate", "flow.delay"))
	disconnectedResult := Validate(disconnected)
	assertDiagnostic(t, disconnectedResult.Diagnostics, validation.CodeNodeUnreachable, validation.Path{"nodes", 2})
	if hasCode(disconnectedResult.Diagnostics, validation.CodeEndNodeUnreachable) {
		t.Fatalf("reachable End was not recognized: %+v", disconnectedResult.Diagnostics)
	}

	noReachableEnd := validGraph()
	noReachableEnd.Edges = nil
	noReachableEndResult := Validate(noReachableEnd)
	if !hasCode(noReachableEndResult.Diagnostics, validation.CodeNodeUnreachable) || !hasCode(noReachableEndResult.Diagnostics, validation.CodeEndNodeUnreachable) {
		t.Fatalf("disconnected graph diagnostics = %+v", noReachableEndResult.Diagnostics)
	}
}

func TestValidateCyclesAcrossAllComponents(t *testing.T) {
	t.Parallel()

	self := validGraph()
	self.Nodes = append(self.Nodes, testNode("self", "flow.delay"))
	self.Edges = append(self.Edges, editorgraph.Edge{ID: "self_edge", SourceNodeID: "self", SourcePort: "success", TargetNodeID: "self"})
	assertDiagnostic(t, Validate(self).Diagnostics, validation.CodeGraphCycleDetected, validation.Path{"edges", 1})

	multiple := validGraph()
	multiple.Nodes = append(multiple.Nodes, testNode("a", "flow.delay"), testNode("b", "flow.delay"))
	multiple.Edges = append(multiple.Edges,
		editorgraph.Edge{ID: "a_b", SourceNodeID: "a", SourcePort: "success", TargetNodeID: "b"},
		editorgraph.Edge{ID: "b_a", SourceNodeID: "b", SourcePort: "success", TargetNodeID: "a"},
		editorgraph.Edge{ID: "b_self", SourceNodeID: "b", SourcePort: "success", TargetNodeID: "b"},
	)
	got := Validate(multiple)
	if countCode(got.Diagnostics, validation.CodeGraphCycleDetected) != 2 {
		t.Fatalf("cycle diagnostics = %+v", got.Diagnostics)
	}
}

func TestValidateIsDeterministicPureAndLinearAtLimit(t *testing.T) {
	t.Parallel()

	graph := chainGraph(validation.EditorGraphMaxNodes)
	original := cloneGraph(graph)
	first := Validate(graph)
	second := Validate(graph)
	if len(first.Diagnostics) != 0 || !first.Continue {
		t.Fatalf("near-limit valid graph result = %+v", first)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated results differ: first=%+v second=%+v", first, second)
	}
	if !reflect.DeepEqual(graph, original) {
		t.Fatal("Validate mutated the input graph")
	}
}

func validGraph() *editorgraph.Graph {
	return &editorgraph.Graph{
		SchemaVersion: 1,
		EntryNodeID:   "start",
		Nodes:         []editorgraph.Node{testNode("start", "flow.start"), testNode("end", "flow.end")},
		Edges: []editorgraph.Edge{{
			ID:           "edge",
			SourceNodeID: "start",
			SourcePort:   "success",
			TargetNodeID: "end",
		}},
	}
}

func chainGraph(count int) *editorgraph.Graph {
	graph := &editorgraph.Graph{SchemaVersion: 1, EntryNodeID: "node_000"}
	graph.Nodes = make([]editorgraph.Node, count)
	for index := range graph.Nodes {
		nodeType := "flow.delay"
		if index == 0 {
			nodeType = "flow.start"
		} else if index == count-1 {
			nodeType = "flow.end"
		}
		graph.Nodes[index] = testNode(fmt.Sprintf("node_%03d", index), nodeType)
	}
	graph.Edges = make([]editorgraph.Edge, count-1)
	for index := range graph.Edges {
		graph.Edges[index] = editorgraph.Edge{
			ID:           fmt.Sprintf("edge_%03d", index),
			SourceNodeID: graph.Nodes[index].ID,
			SourcePort:   "success",
			TargetNodeID: graph.Nodes[index+1].ID,
		}
	}
	return graph
}

func testNode(id, nodeType string) editorgraph.Node {
	return editorgraph.Node{ID: id, Type: nodeType, Config: editorgraph.JSONObject{}, Bindings: editorgraph.JSONObject{}, ErrorPolicy: editorgraph.ErrorPolicyStop}
}

func mutate(graph *editorgraph.Graph, apply func(*editorgraph.Graph)) *editorgraph.Graph {
	apply(graph)
	return graph
}

func cloneGraph(graph *editorgraph.Graph) *editorgraph.Graph {
	clone := *graph
	clone.Nodes = append([]editorgraph.Node(nil), graph.Nodes...)
	clone.Edges = append([]editorgraph.Edge(nil), graph.Edges...)
	return &clone
}

func diagnosticCodes(diagnostics []validation.Diagnostic) []validation.Code {
	codes := make([]validation.Code, len(diagnostics))
	for index, diagnostic := range diagnostics {
		codes[index] = diagnostic.Code
	}
	return codes
}

func hasCode(diagnostics []validation.Diagnostic, code validation.Code) bool {
	return countCode(diagnostics, code) > 0
}

func countCode(diagnostics []validation.Diagnostic, code validation.Code) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			count++
		}
	}
	return count
}

func assertDiagnostic(t *testing.T, diagnostics []validation.Diagnostic, code validation.Code, path validation.Path) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code && reflect.DeepEqual(diagnostic.Path, path) {
			return
		}
	}
	t.Fatalf("diagnostics = %+v, want %s at %#v", diagnostics, code, path)
}
