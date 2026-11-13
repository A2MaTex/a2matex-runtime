package service

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
)

const (
	maxInputFields       = 100
	maxInputStringLength = 10_000
	maxSafeInteger       = 9_007_199_254_740_991
)

var inputName = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,99}$`)

func validateInputSchema(raw json.RawMessage) (*nodecontract.WorkflowInputs, []validation.Diagnostic) {
	var diagnostics []validation.Diagnostic
	report := func(path ...any) {
		diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeWorkflowInputSchemaInvalid,
			append(validation.Path{"inputSchema"}, path...), "", ""))
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		report()
		return nil, diagnostics
	}
	for key := range schema {
		switch key {
		case "type", "properties", "required", "additionalProperties":
			continue
		}
		report()
		break
	}
	var schemaType string
	if json.Unmarshal(schema["type"], &schemaType) != nil || schemaType != "object" {
		report("type")
	}
	if !bytes.Equal(bytes.TrimSpace(schema["additionalProperties"]), []byte("false")) {
		report("additionalProperties")
	}

	inputs := &nodecontract.WorkflowInputs{Properties: map[string]nodecontract.FieldType{}, Required: map[string]bool{}}
	var properties map[string]json.RawMessage
	if json.Unmarshal(schema["properties"], &properties) != nil || properties == nil {
		report("properties")
	} else {
		if len(properties) > maxInputFields {
			report("properties")
		}
		for name, rawField := range properties {
			if !inputName.MatchString(name) {
				report("properties", name)
				continue
			}
			var field map[string]json.RawMessage
			if json.Unmarshal(rawField, &field) != nil || field == nil {
				report("properties", name)
				continue
			}
			var kind nodecontract.FieldType
			_ = json.Unmarshal(field["type"], &kind)
			switch kind {
			case nodecontract.FieldTypeString:
				validateInputBounds(field, name, "minLength", "maxLength", 0, maxInputStringLength, report)
			case nodecontract.FieldTypeInteger:
				validateInputBounds(field, name, "minimum", "maximum", -maxSafeInteger, maxSafeInteger, report)
			case nodecontract.FieldTypeBoolean:
				if len(field) != 1 {
					report("properties", name)
				}
			default:
				report("properties", name, "type")
			}
			inputs.Properties[name] = kind
		}
	}
	var required []json.RawMessage
	if json.Unmarshal(schema["required"], &required) != nil || required == nil {
		report("required")
	} else {
		if len(required) > maxInputFields {
			report("required")
		}
		for index, rawName := range required {
			var name string
			if json.Unmarshal(rawName, &name) != nil || !inputName.MatchString(name) || properties[name] == nil || inputs.Required[name] {
				report("required", index)
				continue
			}
			inputs.Required[name] = true
		}
	}
	return inputs, diagnostics
}

func validateInputBounds(field map[string]json.RawMessage, name, lower, upper string, min, max float64, report func(...any)) {
	for key := range field {
		if key != "type" && key != lower && key != upper {
			report("properties", name)
			break
		}
	}
	low, high := min, max
	for _, key := range []string{lower, upper} {
		raw, exists := field[key]
		if !exists {
			continue
		}
		value, err := strconv.ParseFloat(string(raw), 64)
		if err != nil || math.Trunc(value) != value || value < min || value > max {
			report("properties", name, key)
			continue
		}
		if key == lower {
			low = value
		} else {
			high = value
		}
	}
	if low > high {
		report("properties", name)
	}
}
