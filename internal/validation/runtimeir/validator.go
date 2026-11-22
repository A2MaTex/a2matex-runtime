// Package runtimeir validates executable workflows before run admission.
package runtimeir

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/topology"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type InputValues map[string]json.RawMessage

type Result struct {
	Validation   validation.Result
	RawIR        json.RawMessage
	Document     *ir.Document
	DeviceInputs []InputValues
	FailureCode  string
}

type Validator struct {
	definitions    *nodecontract.Registry
	handlers       *registry.Registry
	runtimeVersion string
}

func New(definitions *nodecontract.Registry, handlers *registry.Registry, runtimeVersion string) (*Validator, error) {
	if definitions == nil || handlers == nil {
		return nil, xerror.WithOperation("configure_ir_admission", xerror.ErrRuntimeIRDependencies)
	}
	return &Validator{definitions: definitions, handlers: handlers, runtimeVersion: runtimeVersion}, nil
}

type checks struct {
	ctx        context.Context
	findings   validation.Collector
	semantic   bool
	structural bool
}

// Validate returns owned checked data, expected rejection diagnostics, or an operational error.
func (v *Validator) Validate(ctx context.Context, rawIR json.RawMessage, deviceInputs []json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, xerror.WithOperation("check_admission_context", err)
	}
	if len(rawIR) > constants.RuntimeIRMaxContentBytes {
		return Result{}, xerror.WithOperation("bound_runtime_ir", xerror.ErrRuntimeIRPayloadTooLarge)
	}
	remaining := constants.RunRequestMaxContentBytes - len(rawIR)
	for _, raw := range deviceInputs {
		if len(raw) > remaining {
			return Result{}, xerror.WithOperation("bound_admission_inputs", xerror.ErrRuntimeIRPayloadTooLarge)
		}
		remaining -= len(raw)
	}
	c := &checks{ctx: ctx}
	if len(deviceInputs) < 1 {
		c.contract(validation.Path{"devices"})
		if err := ctx.Err(); err != nil {
			return Result{}, xerror.WithOperation("check_admission_context", err)
		}
		return c.result(nil, nil, nil), nil
	}
	document := c.decode(rawIR)
	if err := ctx.Err(); err != nil {
		return Result{}, xerror.WithOperation("decode_runtime_ir", err)
	}
	if document == nil {
		return c.result(nil, nil, nil), nil
	}
	schema, err := nodecontract.DecodeWorkflowInputs(ctx, document.InputSchema, validation.Path{"runtimeIr", "inputSchema"}, c.add)
	if err != nil {
		return Result{}, xerror.WithOperation("validate_workflow_input_schema", err)
	}
	graph := topology.View{EntryNodeID: document.EntryNodeID, Edges: document.Edges}
	for _, node := range document.Nodes {
		graph.Nodes = append(graph.Nodes, topology.Node{ID: node.ID, Type: node.Type})
	}
	topologyResult, err := topology.ValidateView(ctx, graph)
	if err != nil {
		return Result{}, xerror.WithOperation("validate_ir_graph", err)
	}
	for _, finding := range topologyResult.Diagnostics {
		finding.Path = appendPath(validation.Path{"runtimeIr"}, finding.Path...)
		c.add(finding)
	}
	c.checkStartIncoming(document)
	if err := v.definitions.ValidateIR(ctx, document, schema, len(topologyResult.Diagnostics) == 0, c.add); err != nil {
		return Result{}, xerror.WithOperation("validate_ir_node_contracts", err)
	}
	inputs := c.validateInputs(document, schema, deviceInputs, v.definitions)
	v.compatibility(c, document)
	if err := ctx.Err(); err != nil {
		return Result{}, xerror.WithOperation("validate_runtime_ir", err)
	}
	return c.result(document, rawIR, inputs), nil
}

func (c *checks) add(finding validation.Diagnostic) {
	if c.ctx.Err() != nil {
		return
	}
	for index, part := range finding.Path {
		if name, ok := part.(string); ok && !ir.ValidIdentifier(name) {
			finding.Path = finding.Path[:index]
			break
		}
	}
	switch finding.Code {
	case validation.CodeRuntimeIRFormatUnsupported, validation.CodeGraphSchemaVersionUnsupported,
		validation.CodeNodeTypeNotActive, validation.CodeNodeRuntimeHandlerUnavailable, validation.CodeRuntimeVersionUnsupported:
	default:
		c.semantic = true
	}
	c.findings.Add(finding)
}

func (c *checks) contract(path validation.Path) {
	c.structural = true
	c.add(validation.NewDiagnostic(validation.CodeRuntimeIRContractInvalid, path, "", ""))
}

func (c *checks) result(document *ir.Document, rawIR json.RawMessage, inputs []InputValues) Result {
	result := Result{Validation: c.findings.Result()}
	if result.Validation.Status == validation.StatusInvalid {
		result.FailureCode = constants.RuntimeIncompatible
		if c.semantic {
			result.FailureCode = constants.ValidationFailed
		}
		return result
	}
	result.Document, result.DeviceInputs = document, inputs
	result.RawIR = bytes.Clone(rawIR)
	return result
}

func (c *checks) checkStartIncoming(document *ir.Document) {
	starts := make(map[string]bool)
	for _, node := range document.Nodes {
		if node.Type == "flow.start" {
			starts[node.ID] = true
		}
	}
	for index, edge := range document.Edges {
		if starts[edge.TargetNodeID] {
			c.add(validation.NewDiagnostic(validation.CodeStartNodeHasIncomingEdge,
				validation.Path{"runtimeIr", "edges", index, "targetNodeId"}, edge.TargetNodeID, edge.ID))
		}
	}
}
