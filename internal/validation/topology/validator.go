package topology

import (
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

type Result struct {
	Diagnostics []validation.Diagnostic
	Continue    bool
}

type indexedEdge struct {
	edge  editorgraph.Edge
	index int
}

func Validate(graph *editorgraph.Graph) Result {
	var diagnostics []validation.Diagnostic
	if len(graph.Nodes) > validation.EditorGraphMaxNodes {
		diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeGraphNodeLimitExceeded, validation.Path{"nodes"}, "", ""))
	}
	if len(graph.Edges) > validation.EditorGraphMaxEdges {
		diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeGraphEdgeLimitExceeded, validation.Path{"edges"}, "", ""))
	}
	if len(diagnostics) > 0 {
		return Result{Diagnostics: diagnostics, Continue: false}
	}

	nodeByID := make(map[string]editorgraph.Node, len(graph.Nodes))
	uniqueNodeIDs := make([]string, 0, len(graph.Nodes))
	for index, node := range graph.Nodes {
		if _, exists := nodeByID[node.ID]; exists {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeNodeIDDuplicated,
				validation.Path{"nodes", index, "id"},
				node.ID,
				"",
			))
			continue
		}
		nodeByID[node.ID] = node
		uniqueNodeIDs = append(uniqueNodeIDs, node.ID)
	}

	edgeIDs := make(map[string]struct{}, len(graph.Edges))
	for index, edge := range graph.Edges {
		if _, exists := edgeIDs[edge.ID]; exists {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeEdgeIDDuplicated,
				validation.Path{"edges", index, "id"},
				"",
				edge.ID,
			))
			continue
		}
		edgeIDs[edge.ID] = struct{}{}
	}

	var startNode *editorgraph.Node
	startCount := 0
	for index := range graph.Nodes {
		if graph.Nodes[index].Type == "flow.start" {
			startNode = &graph.Nodes[index]
			startCount++
		}
	}
	if startCount != 1 {
		diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeStartNodeCountInvalid, validation.Path{"nodes"}, "", ""))
		startNode = nil
	}
	if startNode == nil || graph.EntryNodeID != startNode.ID {
		diagnostics = append(diagnostics, validation.NewDiagnostic(
			validation.CodeEntryNodeInvalid,
			validation.Path{"entryNodeId"},
			graph.EntryNodeID,
			"",
		))
	}

	adjacency := make(map[string][]indexedEdge, len(nodeByID))
	for _, nodeID := range uniqueNodeIDs {
		adjacency[nodeID] = nil
	}
	for index, edge := range graph.Edges {
		source, sourceExists := nodeByID[edge.SourceNodeID]
		_, targetExists := nodeByID[edge.TargetNodeID]
		if !sourceExists {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeEdgeSourceNodeNotFound,
				validation.Path{"edges", index, "sourceNodeId"},
				edge.SourceNodeID,
				edge.ID,
			))
		}
		if !targetExists {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeEdgeTargetNodeNotFound,
				validation.Path{"edges", index, "targetNodeId"},
				edge.TargetNodeID,
				edge.ID,
			))
		}
		if sourceExists && source.Type == "flow.end" {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeEndNodeHasOutgoingEdge,
				validation.Path{"edges", index, "sourceNodeId"},
				source.ID,
				edge.ID,
			))
		}
		if sourceExists && targetExists {
			adjacency[source.ID] = append(adjacency[source.ID], indexedEdge{edge: edge, index: index})
		}
	}

	reachable := make(map[string]struct{}, len(nodeByID))
	if startNode != nil {
		pending := []string{startNode.ID}
		for len(pending) > 0 {
			last := len(pending) - 1
			nodeID := pending[last]
			pending = pending[:last]
			if _, visited := reachable[nodeID]; visited {
				continue
			}
			reachable[nodeID] = struct{}{}
			for _, edge := range adjacency[nodeID] {
				pending = append(pending, edge.edge.TargetNodeID)
			}
		}
	}
	for index, node := range graph.Nodes {
		if _, ok := reachable[node.ID]; !ok {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeNodeUnreachable,
				validation.Path{"nodes", index},
				node.ID,
				"",
			))
		}
	}
	reachableEnd := false
	for _, node := range graph.Nodes {
		_, isReachable := reachable[node.ID]
		if node.Type == "flow.end" && isReachable {
			reachableEnd = true
			break
		}
	}
	if !reachableEnd {
		diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeEndNodeUnreachable, validation.Path{"nodes"}, "", ""))
	}

	diagnostics = append(diagnostics, cycleDiagnostics(uniqueNodeIDs, adjacency)...)
	return Result{Diagnostics: diagnostics, Continue: true}
}

type visitState uint8

const (
	visiting visitState = iota + 1
	visited
)

type frame struct {
	nodeID   string
	nextEdge int
}

func cycleDiagnostics(nodeIDs []string, adjacency map[string][]indexedEdge) []validation.Diagnostic {
	states := make(map[string]visitState, len(nodeIDs))
	var diagnostics []validation.Diagnostic
	for _, root := range nodeIDs {
		if states[root] != 0 {
			continue
		}
		states[root] = visiting
		stack := []frame{{nodeID: root}}
		for len(stack) > 0 {
			current := &stack[len(stack)-1]
			edges := adjacency[current.nodeID]
			if current.nextEdge >= len(edges) {
				states[current.nodeID] = visited
				stack = stack[:len(stack)-1]
				continue
			}
			edge := edges[current.nextEdge]
			current.nextEdge++
			switch states[edge.edge.TargetNodeID] {
			case visiting:
				diagnostics = append(diagnostics, validation.NewDiagnostic(
					validation.CodeGraphCycleDetected,
					validation.Path{"edges", edge.index},
					edge.edge.TargetNodeID,
					edge.edge.ID,
				))
			case 0:
				states[edge.edge.TargetNodeID] = visiting
				stack = append(stack, frame{nodeID: edge.edge.TargetNodeID})
			}
		}
	}
	return diagnostics
}
