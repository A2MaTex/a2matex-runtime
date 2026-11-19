// Package bindings resolves admitted input sources into owned handler values.
package bindings

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Values is the read-only view belonging to one device execution.
// G9/G10 publish only confirmed successful snapshots into CommittedOutputs.
type Values struct {
	WorkflowInputs   ir.Object
	CommittedOutputs map[string]ir.Object
	Variables        VariableView
}

type Resolver struct {
	definitions *nodecontract.Registry
}

func New(definitions *nodecontract.Registry) (*Resolver, error) {
	if definitions == nil {
		return nil, xerror.WithOperation("configure_input_bindings", xerror.ErrInputBindingDependencies)
	}
	return &Resolver{definitions: definitions}, nil
}

// Resolve checks one destination and returns a copy of its selected scalar JSON.
// The caller keeps the admitted node and execution values immutable during the call.
func (r *Resolver) Resolve(ctx context.Context, node ir.Node, field string, values Values) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("resolve_input_binding", err)
	}
	safeField := ""
	fail := func(reason, message string) (json.RawMessage, error) {
		return nil, bindingError(ctx, "resolve_input_binding", safeField, reason, message)
	}
	contract, known := r.definitions.Input(node.Type, node.Config, field)
	if !known {
		return fail("input_definition_unavailable", "Node input definition is unavailable")
	}
	safeField = field
	raw, present := node.Inputs[field]
	if !present {
		return fail("binding_missing", "Node input binding is unavailable")
	}
	var source ir.Object
	if !utf8.Valid(raw) || json.Unmarshal(raw, &source) != nil || source == nil {
		return fail("binding_invalid", "Input binding does not match its contract")
	}
	var kind string
	if json.Unmarshal(source["source"], &kind) != nil || kind == "" {
		return fail("binding_invalid", "Input binding does not match its contract")
	}
	switch kind {
	case constants.InputSourceLiteral, constants.InputSourceWorkflowInput, constants.InputSourceNodeOutput,
		constants.InputSourceFlowVariable, constants.InputSourceGlobalVariable:
	default:
		return fail("source_unsupported", "Input binding source is not supported")
	}
	if !slices.Contains(contract.Sources, kind) {
		return fail("source_unsupported", "Input binding source is not supported")
	}

	var value json.RawMessage
	switch kind {
	case constants.InputSourceLiteral:
		var exists bool
		value, exists = source["value"]
		if len(source) != 2 || !exists {
			return fail("binding_invalid", "Input binding does not match its contract")
		}
	case constants.InputSourceWorkflowInput:
		var name string
		if len(source) != 2 || json.Unmarshal(source["inputName"], &name) != nil || !ir.ValidFieldName(name) {
			return fail("binding_invalid", "Input binding does not match its contract")
		}
		var exists bool
		value, exists = values.WorkflowInputs[name]
		if !exists {
			return fail("workflow_input_missing", "Workflow input value is unavailable")
		}
	case constants.InputSourceNodeOutput:
		var producerID, outputField string
		if len(source) != 3 || json.Unmarshal(source["nodeId"], &producerID) != nil || !ir.ValidIdentifier(producerID) ||
			json.Unmarshal(source["field"], &outputField) != nil || !ir.ValidFieldName(outputField) {
			return fail("binding_invalid", "Input binding does not match its contract")
		}
		producer, exists := values.CommittedOutputs[producerID]
		if !exists || producer == nil {
			return fail("node_output_unavailable", "Previous node output is unavailable")
		}
		value, exists = producer[outputField]
		if !exists {
			return fail("node_output_field_missing", "Previous node output field is unavailable")
		}
	case constants.InputSourceFlowVariable, constants.InputSourceGlobalVariable:
		var err error
		value, err = resolveVariable(ctx, source, kind, field, values.Variables)
		if err != nil {
			return nil, err
		}
	}
	if !utf8.Valid(value) {
		return fail("input_value_invalid", "Resolved input does not satisfy its field contract")
	}
	if _, scalar := nodecontract.ScalarType(value); !scalar || !r.definitions.ValidInputValue(node.Type, node.Config, field, value) {
		return fail("input_value_invalid", "Resolved input does not satisfy its field contract")
	}
	resolved := bytes.Clone(value)
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("resolve_input_binding", err)
	}
	return resolved, nil
}

// ResolveInputs assembles an ordinary node's supplied inputs in lexical field order.
// Admission owns required-field presence; a failure returns no partial map.
func (r *Resolver) ResolveInputs(ctx context.Context, node ir.Node, values Values) (ir.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("resolve_node_inputs", err)
	}
	if _, known := r.definitions.CompilationDefinition(node.Type); !known {
		return nil, bindingError(ctx, "resolve_node_inputs", "", "input_definition_unavailable", "Node input definition is unavailable")
	}
	fields := make([]string, 0, len(node.Inputs))
	for field := range node.Inputs {
		fields = append(fields, field)
	}
	slices.Sort(fields)
	resolved := make(ir.Object, len(fields))
	for _, field := range fields {
		value, err := r.Resolve(ctx, node, field, values)
		if err != nil {
			return nil, xerror.WithOperation("resolve_node_inputs", err)
		}
		resolved[field] = value
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("resolve_node_inputs", err)
	}
	return resolved, nil
}

func bindingError(ctx context.Context, operation, field, reason, message string) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation(operation, err)
	}
	return &xerror.InputBindingError{Field: field, Err: &xerror.OperationError{
		Operation: operation,
		Detail:    reason,
		Err:       xerror.CommandError{Code: constants.CommandInputUnsupported, Message: message},
	}}
}
