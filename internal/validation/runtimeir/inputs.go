package runtimeir

import (
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func (c *checks) validateInputs(document *ir.Document, schema *nodecontract.WorkflowInputs, rawInputs []json.RawMessage, definitions *nodecontract.Registry) []InputValues {
	if schema == nil {
		return nil
	}
	inputs := make([]InputValues, len(rawInputs))
	invalid := make([]map[string]bool, len(rawInputs))
	for index, raw := range rawInputs {
		if c.ctx.Err() != nil {
			return nil
		}
		path := validation.Path{"devices", index, "inputs"}
		var values InputValues
		if !utf8.Valid(raw) || json.Unmarshal(raw, &values) != nil || values == nil {
			c.add(validation.NewDiagnostic(validation.CodeWorkflowInputInvalid, path, "", ""))
			continue
		}
		inputs[index] = values
		invalid[index] = make(map[string]bool)
		for name := range schema.Required {
			if _, present := values[name]; !present {
				invalid[index][name] = true
				c.add(validation.NewDiagnostic(validation.CodeWorkflowInputInvalid, appendPath(path, name), "", ""))
			}
		}
		for name, value := range values {
			if c.ctx.Err() != nil {
				return nil
			}
			if !schema.ValueValid(name, value) {
				invalid[index][name] = true
				c.add(validation.NewDiagnostic(validation.CodeWorkflowInputInvalid, appendPath(path, name), "", ""))
			}
		}
	}
	for nodeIndex, node := range document.Nodes {
		if c.ctx.Err() != nil {
			return nil
		}
		for name, raw := range node.Inputs {
			if c.ctx.Err() != nil {
				return nil
			}
			var source ir.Object
			_ = json.Unmarshal(raw, &source)
			tag := sourceString(source["source"])
			if tag == constants.InputSourceLiteral {
				if _, known := definitions.Input(node.Type, node.Config, name); known && definitions.ValidInputValue(node.Type, node.Config, name, source["value"]) {
					c.adapterInput(node, name, source["value"], validation.Path{"runtimeIr", "nodes", nodeIndex, "inputs", name, "value"})
				}
				continue
			}
			if tag != constants.InputSourceWorkflowInput {
				continue
			}
			inputName := sourceString(source["inputName"])
			contract, known := definitions.Input(node.Type, node.Config, name)
			fieldType, declared := schema.Properties[inputName]
			if !known || !declared || !slices.Contains(contract.Sources, tag) {
				continue
			}
			if contract.Schema.Type != "" && contract.Schema.Type != fieldType {
				continue
			}
			if contract.Required && !schema.Required[inputName] {
				// The shared contract checker diagnoses static requiredness first.
				continue
			}
			for deviceIndex, values := range inputs {
				if c.ctx.Err() != nil {
					return nil
				}
				if values == nil || invalid[deviceIndex][inputName] {
					continue
				}
				value, present := values[inputName]
				path := validation.Path{"devices", deviceIndex, "inputs", inputName}
				if !present || !definitions.ValidInputValue(node.Type, node.Config, name, value) {
					c.add(validation.NewDiagnostic(validation.CodeNodeValueBindingInvalid, path, node.ID, ""))
					continue
				}
				c.adapterInput(node, name, value, path)
			}
		}
	}
	return inputs
}

func (c *checks) adapterInput(node ir.Node, field string, raw json.RawMessage, path validation.Path) {
	unsupported := false
	switch node.Type {
	case "android.input_text":
		if field == "text" {
			unsupported = (adb.InputTextCommand{Text: sourceString(raw)}).Validate() != nil
		}
	case "android.long_press":
		if field == "durationMs" {
			var duration float64
			_ = json.Unmarshal(raw, &duration)
			unsupported = duration > constants.MaxAndroidLongPressDurationMS
		}
	}
	if unsupported {
		c.add(validation.NewDiagnostic(validation.CodeNodeExecutionInputUnsupported, path, node.ID, ""))
	}
}

func sourceString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
