package nodecontract

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"unicode/utf16"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const (
	setVariableType       = "flow.set_variable"
	waitVariableType      = "flow.wait_variable"
	bindingNamePattern    = constants.VariableNamePattern
	bindingLiteral        = constants.InputSourceLiteral
	bindingWorkflowInput  = constants.InputSourceWorkflowInput
	bindingNodeOutput     = constants.InputSourceNodeOutput
	bindingFlowVariable   = constants.InputSourceFlowVariable
	bindingGlobalVariable = constants.InputSourceGlobalVariable
)

var bindingName = regexp.MustCompile(bindingNamePattern)

var fieldName = regexp.MustCompile(constants.WorkflowFieldNamePattern)

type WorkflowInputs struct {
	Properties map[string]FieldType
	Required   map[string]bool
	fields     map[string]compiledField
}

func validateBindings(graph *graphView, registry *Registry, nodes map[string]nodeView, workflowInputs *WorkflowInputs, emit func(validation.Diagnostic)) error {
	outgoing := make(map[string][]ir.Edge)
	for _, edge := range graph.Edges {
		_, source := nodes[edge.SourceNodeID]
		_, target := nodes[edge.TargetNodeID]
		if source && target {
			outgoing[edge.SourceNodeID] = append(outgoing[edge.SourceNodeID], edge)
		}
	}
	bypasses := make(map[string]map[string]bool)
	for index, node := range graph.Nodes {
		if err := graph.ctx.Err(); err != nil {
			return err
		}
		definition, known := registry.catalog[node.Type]
		if !known || targetingInputs[node.Type] != nil {
			continue
		}
		set := node.Type == setVariableType
		isSwitch := node.Type == switchType
		var rules map[string]string
		fields := map[string]bool{"value": true}
		if isSwitch {
			rules = switchRules(node)
			if rules == nil {
				continue
			}
			for id, operator := range rules {
				fields[id] = needsOperand(operator)
			}
		}
		types := make(map[string]FieldType)
		reported := make(map[string]bool)
		report := func(field string, suffix ...any) {
			if reported[field] {
				return
			}
			reported[field] = true
			path := node.inputPath(index, field)
			if node.form.source == "source" {
				path = append(path, suffix...)
			}
			emit(validation.NewDiagnostic(validation.CodeNodeValueBindingInvalid, path, node.ID, ""))
		}
		for _, field := range definition.InputSchema.Required {
			if node.Inputs[field] == nil {
				report(field)
			}
		}
		if set && node.Inputs["value"] == nil {
			report("value")
		}
		if isSwitch {
			for field, required := range fields {
				if required && node.Inputs[field] == nil {
					report(field)
				}
			}
		}
		for field, raw := range node.Inputs {
			if node.Type == waitVariableType {
				report(field)
				continue
			}
			var binding ir.Object
			_ = json.Unmarshal(raw, &binding)
			kind := bindingString(binding[node.form.source])
			input, knownInput := registry.inputField(node.Type, node.Config, field)
			if (set && field != "value") || (isSwitch && !fields[field]) ||
				(!set && !isSwitch && !knownInput) || !slices.Contains(registry.inputKinds(node.Type, input), kind) || !validBindingShape(kind, binding, node.form.source) {
				report(field)
				continue
			}
			if kind == bindingLiteral {
				if knownInput && !validValue(binding["value"], input) {
					report(field, "value")
				}
				if isSwitch {
					var literal any
					_ = json.Unmarshal(binding["value"], &literal)
					switch literal.(type) {
					case string:
						types[field] = FieldTypeString
					case bool:
						types[field] = FieldTypeBoolean
					case float64:
						types[field] = FieldTypeNumber
					}
				}
				continue
			}
			if kind == bindingWorkflowInput {
				if workflowInputs == nil {
					continue
				}
				name := bindingString(binding["inputName"])
				sourceType := workflowInputs.Properties[name]
				required := slices.Contains(definition.InputSchema.Required, field) || (node.form.source == "source" && isSwitch && fields[field])
				if sourceType == "" || (knownInput && input.Type != "" && sourceType != input.Type) ||
					(required && !workflowInputs.Required[name]) {
					report(field)
				} else if isSwitch {
					types[field] = switchFieldType(sourceType)
				}
				continue
			}
			if kind != bindingNodeOutput {
				continue
			}
			producer := bindingString(binding["nodeId"])
			if node.form.source == "kind" {
				producer = editorgraph.TrimIdentifier(producer)
			}
			outputSchema := registry.catalog[nodes[producer].Type].OutputSchema
			outputField := bindingString(binding["field"])
			output := outputSchema.Properties[outputField].Type
			if !slices.Contains(outputSchema.Required, outputField) || output == "" || (knownInput && input.Type != "" && input.Type != output) || producer == node.ID {
				report(field)
				continue
			}
			if graph.dominance && bypasses[producer] == nil {
				bypasses[producer] = reachableWithoutSuccess(graph, producer, outgoing)
			}
			if graph.dominance && bypasses[producer][node.ID] {
				report(field)
			} else if isSwitch {
				types[field] = switchFieldType(output)
			}
		}
		left := types["value"]
		for id, operator := range rules {
			right := types[id]
			expected := operatorType(operator)
			if expected == "" {
				if left != "" && right != "" && left != right {
					report(id)
				}
				continue
			}
			if left != "" && left != expected {
				report("value")
			}
			if needsOperand(operator) && right != "" && right != expected {
				report(id)
			}
		}
	}
	return graph.ctx.Err()
}

func reachableWithoutSuccess(graph *graphView, producer string, outgoing map[string][]ir.Edge) map[string]bool {
	seen := map[string]bool{graph.EntryNodeID: true}
	queue := []string{graph.EntryNodeID}
	for index := 0; index < len(queue); index++ {
		if graph.ctx.Err() != nil {
			return seen
		}
		current := queue[index]
		for _, edge := range outgoing[current] {
			if seen[edge.TargetNodeID] || (current == producer && edge.SourcePort == "success") {
				continue
			}
			seen[edge.TargetNodeID] = true
			queue = append(queue, edge.TargetNodeID)
		}
	}
	return seen
}

func validBindingShape(kind string, binding ir.Object, tag string) bool {
	for key := range binding {
		if key != tag && key != "value" && key != "inputName" && key != "nodeId" && key != "field" && key != "name" {
			return false
		}
	}
	switch kind {
	case bindingNodeOutput:
		id := bindingString(binding["nodeId"])
		validID := ir.ValidIdentifier(id)
		if tag == "kind" {
			id = editorgraph.TrimIdentifier(id)
			validID = id != "" && len(utf16.Encode([]rune(id))) <= validation.EditorGraphIdentifierMaxUTF16
		}
		return len(binding) == 3 && validID &&
			fieldName.MatchString(bindingString(binding["field"]))
	case bindingFlowVariable, bindingGlobalVariable:
		return len(binding) == 2 && bindingName.MatchString(bindingString(binding["name"]))
	case bindingWorkflowInput:
		return len(binding) == 2 && fieldName.MatchString(bindingString(binding["inputName"]))
	case bindingLiteral:
		if len(binding) != 2 {
			return false
		}
		return validScalar(binding["value"])
	}
	return false
}

func bindingString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func switchFieldType(kind FieldType) FieldType {
	if kind == FieldTypeInteger {
		return FieldTypeNumber
	}
	return kind
}

func validScalar(raw json.RawMessage) bool {
	_, valid := ScalarType(raw)
	return valid
}

// ScalarType identifies finite scalar values without coercing strings or nulls.
func ScalarType(raw json.RawMessage) (FieldType, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	switch value := value.(type) {
	case string:
		return FieldTypeString, true
	case bool:
		return FieldTypeBoolean, true
	case float64:
		return FieldTypeNumber, !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	return "", false
}
