package nodecontract

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf16"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func validateView(graph *graphView, registry *Registry, inputs *WorkflowInputs, report func(validation.Diagnostic)) error {
	for index, node := range graph.Nodes {
		if err := graph.ctx.Err(); err != nil {
			return err
		}
		contract, ok := registry.lookup(node.Type)
		if !ok {
			report(validation.NewDiagnostic(validation.CodeNodeTypeNotActive, node.path(index, "type"), node.ID, ""))
			continue
		}
		if node.ErrorPolicy == constants.ErrorPolicyErrorBranch {
			if _, exists := contract.outputPorts["error"]; !exists {
				report(validation.NewDiagnostic(validation.CodeNodeErrorPolicyInvalid, node.path(index, "errorPolicy"), node.ID, ""))
			}
		}
		if node.Type != switchType {
			for _, finding := range validateNode(node, index, contract) {
				report(finding)
			}
		}
	}
	nodes := make(map[string]nodeView, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	for index, edge := range graph.Edges {
		if err := graph.ctx.Err(); err != nil {
			return err
		}
		node, exists := nodes[edge.SourceNodeID]
		if !exists || node.Type == switchType {
			continue
		}
		contract, known := registry.lookup(node.Type)
		if !known {
			continue
		}
		if _, exists := contract.outputPorts[edge.SourcePort]; !exists {
			report(validation.NewDiagnostic(validation.CodeEdgeSourcePortNotFound, graph.edgePath(index, "sourcePort"), node.ID, edge.ID))
		}
	}
	for _, finding := range validateOrdinaryPortCounts(graph, registry) {
		report(finding)
	}
	if err := validateSwitches(graph, registry, report); err != nil {
		return err
	}
	return validateBindings(graph, registry, nodes, inputs, report)
}

func validateOrdinaryPortCounts(graph *graphView, registry *Registry) []validation.Diagnostic {
	type portCounts struct {
		success int
		failure int
	}
	outgoing := make(map[string]portCounts, len(graph.Nodes))
	for _, edge := range graph.Edges {
		counts := outgoing[edge.SourceNodeID]
		switch edge.SourcePort {
		case "success":
			counts.success++
		case "error":
			counts.failure++
		default:
			continue
		}
		outgoing[edge.SourceNodeID] = counts
	}

	var diagnostics []validation.Diagnostic
	for index, node := range graph.Nodes {
		if _, known := registry.lookup(node.Type); !known || node.Type == "flow.end" || node.Type == switchType {
			continue
		}
		counts := outgoing[node.ID]
		if counts.success != 1 {
			diagnostics = append(diagnostics, portCountDiagnostic(node, index, "success"))
		}

		expectedErrors := 0
		if node.ErrorPolicy == constants.ErrorPolicyErrorBranch {
			expectedErrors = 1
		}
		if counts.failure != expectedErrors {
			diagnostics = append(diagnostics, portCountDiagnostic(node, index, "error"))
		}
	}
	return diagnostics
}

func portCountDiagnostic(node nodeView, index int, port string) validation.Diagnostic {
	return validation.NewDiagnostic(validation.CodeNodePortEdgeCountInvalid, node.portPath(index, port), node.ID, "")
}

func validValue(raw json.RawMessage, schema compiledField) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	if schema.Type == "" && len(schema.BindingKinds) > 0 {
		return validScalar(raw)
	}
	switch schema.Type {
	case FieldTypeString:
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
		length := len(utf16.Encode([]rune(value)))
		return (schema.MinLength == nil || length >= *schema.MinLength) &&
			(schema.MaxLength == nil || length <= *schema.MaxLength) &&
			(schema.pattern == nil || schema.pattern.MatchString(value))
	case FieldTypeInteger, FieldTypeNumber:
		value, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
		if schema.Type == FieldTypeInteger && (math.Trunc(value) != value || value < -constants.MaxSafeInteger || value > constants.MaxSafeInteger) {
			return false
		}
		return (schema.Minimum == nil || value >= *schema.Minimum) &&
			(schema.Maximum == nil || value <= *schema.Maximum)
	case FieldTypeBoolean:
		var value bool
		trimmed := bytes.TrimSpace(raw)
		return json.Unmarshal(trimmed, &value) == nil && (string(trimmed) == "true" || string(trimmed) == "false")
	default:
		return false
	}
}
