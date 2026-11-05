package nodecontract

import (
	"encoding/json"
	"regexp"
	"unicode/utf16"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

const (
	setVariableType    = "flow.set_variable"
	waitVariableType   = "flow.wait_variable"
	bindingNamePattern = "^[A-Za-z_][A-Za-z0-9_]{0,99}$"
)

var bindingName = regexp.MustCompile(bindingNamePattern)

// Field types mirrored from the installed Backend Node Catalog. Target
// inputs are validated separately and allow only literals.
var bindingInputs = map[string]map[string]FieldType{
	"flow.delay":         {"durationMs": FieldTypeInteger},
	"utility.log":        {"message": FieldTypeString},
	"android.launch_app": {"packageName": FieldTypeString},
	"android.input_text": {"text": FieldTypeString},
}

var requiredOutputs = map[string]map[string]FieldType{
	"flow.delay":         {"durationMs": FieldTypeInteger},
	"android.screenshot": {"artifactId": FieldTypeString},
}

func validateBindings(graph *editorgraph.Graph, registry *Registry, nodes map[string]editorgraph.Node) []validation.Diagnostic {
	var diagnostics []validation.Diagnostic
	outgoing := make(map[string][]editorgraph.Edge)
	for _, edge := range graph.Edges {
		_, source := nodes[edge.SourceNodeID]
		_, target := nodes[edge.TargetNodeID]
		if source && target {
			outgoing[edge.SourceNodeID] = append(outgoing[edge.SourceNodeID], edge)
		}
	}
	bypasses := make(map[string]map[string]bool)
	for index, node := range graph.Nodes {
		if _, known := registry.lookup(node.Type); !known || targetingInputs[node.Type] != nil {
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
				fields[id] = operator != "IS_EMPTY"
			}
		}
		types := make(map[string]FieldType)
		reported := make(map[string]bool)
		report := func(field string) {
			if reported[field] {
				return
			}
			reported[field] = true
			diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeNodeValueBindingInvalid,
				validation.Path{"nodes", index, "bindings", field}, node.ID, ""))
		}
		if set && node.Bindings["value"] == nil {
			report("value")
		}
		if isSwitch {
			for field, required := range fields {
				if required && node.Bindings[field] == nil {
					report(field)
				}
			}
		}
		for field, raw := range node.Bindings {
			if node.Type == waitVariableType {
				report(field)
				continue
			}
			var binding editorgraph.JSONObject
			_ = json.Unmarshal(raw, &binding)
			kind := bindingString(binding["kind"])
			if !set && !isSwitch && kind != "nodeOutput" && kind != "flowVariable" && kind != "globalVariable" {
				continue
			}
			input := bindingInputs[node.Type][field]
			if (set && field != "value") || (isSwitch && !fields[field]) ||
				(!set && !isSwitch && input == "") || !validBindingShape(kind, binding) {
				report(field)
				continue
			}
			if isSwitch && kind == "literal" {
				var literal any
				_ = json.Unmarshal(binding["value"], &literal)
				switch literal.(type) {
				case string:
					types[field] = FieldTypeString
				case bool:
					types[field] = FieldTypeBoolean
				case float64:
					types[field] = FieldTypeInteger
				}
			}
			if kind != "nodeOutput" {
				continue
			}
			producer := bindingString(binding["nodeId"])
			output := requiredOutputs[nodes[producer].Type][bindingString(binding["field"])]
			if output == "" || (!set && !isSwitch && input != output) || producer == node.ID {
				report(field)
				continue
			}
			if bypasses[producer] == nil {
				bypasses[producer] = reachableWithoutSuccess(graph.EntryNodeID, producer, outgoing)
			}
			if bypasses[producer][node.ID] {
				report(field)
			} else if isSwitch {
				types[field] = output
			}
		}
		left := types["value"]
		for id, operator := range rules {
			right := types[id]
			switch operator {
			case "IS_EMPTY":
				if left != "" && left != FieldTypeString {
					report("value")
				}
			case "EQ", "NE":
				if left != "" && right != "" && left != right {
					report(id)
				}
			default:
				if left != "" && left != FieldTypeInteger {
					report("value")
				}
				if right != "" && right != FieldTypeInteger {
					report(id)
				}
			}
		}
	}
	return diagnostics
}

func reachableWithoutSuccess(entry, producer string, outgoing map[string][]editorgraph.Edge) map[string]bool {
	seen := map[string]bool{entry: true}
	queue := []string{entry}
	for index := 0; index < len(queue); index++ {
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

func validBindingShape(kind string, binding editorgraph.JSONObject) bool {
	switch kind {
	case "nodeOutput":
		id := bindingString(binding["nodeId"])
		return len(binding) == 3 && id != "" && len(utf16.Encode([]rune(id))) <= validation.EditorGraphIdentifierMaxUTF16 &&
			bindingName.MatchString(bindingString(binding["field"]))
	case "flowVariable", "globalVariable":
		return len(binding) == 2 && bindingName.MatchString(bindingString(binding["name"]))
	case "workflowInput":
		return len(binding) == 2 && bindingName.MatchString(bindingString(binding["inputName"]))
	case "literal":
		if len(binding) != 2 {
			return false
		}
		var value any
		if json.Unmarshal(binding["value"], &value) != nil {
			return false
		}
		switch value := value.(type) {
		case string, bool:
			return true
		case float64:
			return value >= -maxSafeInteger && value <= maxSafeInteger && value == float64(int64(value))
		}
	}
	return false
}

func bindingString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
