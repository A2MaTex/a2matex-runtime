package nodecontract

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf16"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

func Validate(graph *editorgraph.Graph, registry *Registry) []validation.Diagnostic {
	var diagnostics []validation.Diagnostic
	for index, node := range graph.Nodes {
		contract, ok := registry.lookup(node.Type)
		if !ok {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeNodeTypeNotActive,
				validation.Path{"nodes", index, "type"},
				node.ID,
				"",
			))
			continue
		}
		for _, field := range contract.required {
			if _, exists := node.Config[field]; !exists {
				diagnostics = append(diagnostics, configDiagnostic(node.ID, index, field))
			}
		}
		for field, raw := range node.Config {
			schema, exists := contract.properties[field]
			if !exists || !validValue(raw, schema) {
				diagnostics = append(diagnostics, configDiagnostic(node.ID, index, field))
			}
		}
	}

	nodeByID := make(map[string]editorgraph.Node, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodeByID[node.ID] = node
	}
	for index, edge := range graph.Edges {
		node, exists := nodeByID[edge.SourceNodeID]
		if !exists {
			continue
		}
		contract, exists := registry.lookup(node.Type)
		if !exists {
			continue
		}
		if _, exists := contract.outputPorts[edge.SourcePort]; !exists {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeEdgeSourcePortNotFound,
				validation.Path{"edges", index, "sourcePort"},
				node.ID,
				edge.ID,
			))
		}
	}
	return diagnostics
}

func configDiagnostic(nodeID string, nodeIndex int, field string) validation.Diagnostic {
	return validation.NewDiagnostic(
		validation.CodeNodeConfigInvalid,
		validation.Path{"nodes", nodeIndex, "config", field},
		nodeID,
		"",
	)
}

func validValue(raw json.RawMessage, schema compiledField) bool {
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
		value, err := strconv.ParseFloat(string(raw), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
		if schema.Type == FieldTypeInteger && math.Trunc(value) != value {
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
