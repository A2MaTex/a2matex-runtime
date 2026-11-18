// Package routing selects ordinary successors from an admitted workflow.
package routing

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Decision proposes a next position; G9 must confirm its checkpoint before advancing.
type Decision struct {
	NextNodeID   string
	DeviceStatus string
}

// Router is an immutable index that may be shared by executions of one workflow.
type Router struct {
	nodes map[string]route
}

type route struct {
	nodeType, errorPolicy      string
	successNodeID, errorNodeID string
}

// New copies routing metadata. It does not replace G1's whole-document admission.
// Callers must keep the document immutable during construction.
func New(ctx context.Context, document ir.Document) (*Router, error) {
	const operation = "configure_workflow_routing"
	fail := func(reason string) (*Router, error) {
		return nil, routingError(ctx, operation, reason)
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation(operation, err)
	}
	if len(document.Nodes) == 0 || len(document.Nodes) > constants.RuntimeIRMaxNodes ||
		len(document.Edges) > constants.RuntimeIRMaxEdges {
		return fail("routing_document_invalid")
	}

	nodes := make(map[string]route, len(document.Nodes))
	for _, node := range document.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, xerror.WithOperation(operation, err)
		}
		_, duplicate := nodes[node.ID]
		validPolicy := node.ErrorPolicy == constants.ErrorPolicyStop || node.ErrorPolicy == constants.ErrorPolicyErrorBranch
		if !ir.ValidIdentifier(node.ID) || duplicate || !validPolicy {
			return fail("routing_document_invalid")
		}
		nodes[node.ID] = route{nodeType: node.Type, errorPolicy: node.ErrorPolicy}
	}

	seen := make(map[[2]string]struct{}, len(document.Edges))
	for _, edge := range document.Edges {
		if err := ctx.Err(); err != nil {
			return nil, xerror.WithOperation(operation, err)
		}
		node, sourceExists := nodes[edge.SourceNodeID]
		_, targetExists := nodes[edge.TargetNodeID]
		key := [2]string{edge.SourceNodeID, edge.SourcePort}
		_, duplicate := seen[key]
		if !sourceExists || !targetExists || duplicate || edge.SourceNodeID == edge.TargetNodeID {
			return fail("routing_edges_invalid")
		}
		seen[key] = struct{}{}
		switch node.nodeType {
		case constants.EndNodeType:
			return fail("routing_edges_invalid")
		case constants.SwitchNodeType:
			// Dynamic port coverage is admitted by G1 and executed by G15.
			continue
		}
		switch edge.SourcePort {
		case constants.NodeSuccessPort:
			node.successNodeID = edge.TargetNodeID
		case constants.NodeErrorPort:
			if node.errorPolicy != constants.ErrorPolicyErrorBranch {
				return fail("routing_edges_invalid")
			}
			node.errorNodeID = edge.TargetNodeID
		default:
			return fail("routing_edges_invalid")
		}
		nodes[edge.SourceNodeID] = node
	}

	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, xerror.WithOperation(operation, err)
		}
		switch node.nodeType {
		case constants.EndNodeType:
			if node.errorPolicy != constants.ErrorPolicyStop {
				return fail("routing_edges_invalid")
			}
		case constants.SwitchNodeType:
			continue
		default:
			if node.successNodeID == "" || (node.errorPolicy == constants.ErrorPolicyErrorBranch && node.errorNodeID == "") {
				return fail("routing_edges_invalid")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation(operation, err)
	}
	return &Router{nodes: nodes}, nil
}

// Select consumes a final ordinary outcome after the caller has declined retry.
// Cancellation, interruption, Switch evaluation, and persistence errors bypass it.
func (r *Router) Select(ctx context.Context, nodeID, attemptStatus string) (Decision, error) {
	const operation = "select_workflow_route"
	fail := func(reason string) (Decision, error) {
		return Decision{}, routingError(ctx, operation, reason)
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, xerror.WithOperation(operation, err)
	}
	if r == nil || len(r.nodes) == 0 {
		return fail("routing_state_invalid")
	}
	node, exists := r.nodes[nodeID]
	if !exists {
		return fail("routing_node_unavailable")
	}
	if node.nodeType == constants.SwitchNodeType {
		return fail("routing_switch_unsupported")
	}

	var decision Decision
	switch attemptStatus {
	case constants.ExecutionSucceeded:
		decision = Decision{NextNodeID: node.successNodeID, DeviceStatus: constants.ExecutionRunning}
		if node.nodeType == constants.EndNodeType {
			decision = Decision{DeviceStatus: constants.ExecutionSucceeded}
		}
	case constants.ExecutionFailed, constants.ExecutionTimedOut:
		decision = Decision{DeviceStatus: constants.ExecutionFailed}
		if node.errorPolicy == constants.ErrorPolicyErrorBranch {
			decision = Decision{NextNodeID: node.errorNodeID, DeviceStatus: constants.ExecutionRunning}
		}
	default:
		return fail("routing_outcome_unsupported")
	}
	if decision.DeviceStatus == constants.ExecutionRunning && decision.NextNodeID == "" {
		return fail("routing_state_invalid")
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, xerror.WithOperation(operation, err)
	}
	return decision, nil
}

func routingError(ctx context.Context, operation, reason string) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation(operation, err)
	}
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrRoutingInvalid}
}
