package nodecontract

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const (
	maxInputFields       = constants.RuntimeIRMaxFields
	maxInputStringLength = constants.MaxWorkflowStringBound
)

var inputName = regexp.MustCompile(constants.WorkflowFieldNamePattern)

func DecodeWorkflowInputs(ctx context.Context, raw json.RawMessage, root validation.Path, emit func(validation.Diagnostic)) (*WorkflowInputs, error) {
	findings := 0
	report := func(path ...any) {
		findings++
		location := append(append(validation.Path{}, root...), path...)
		emit(validation.NewDiagnostic(validation.CodeWorkflowInputSchemaInvalid, location, "", ""))
	}
	var schema map[string]json.RawMessage
	if !utf8.Valid(raw) || json.Unmarshal(raw, &schema) != nil || schema == nil {
		report()
		return nil, ctx.Err()
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

	inputs := &WorkflowInputs{Properties: map[string]FieldType{}, Required: map[string]bool{}, fields: map[string]compiledField{}}
	var properties map[string]json.RawMessage
	if json.Unmarshal(schema["properties"], &properties) != nil || properties == nil {
		report("properties")
	} else {
		if len(properties) > maxInputFields {
			report("properties")
			return nil, ctx.Err()
		}
		for name, rawField := range properties {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !inputName.MatchString(name) {
				report("properties", name)
				continue
			}
			var field map[string]json.RawMessage
			if json.Unmarshal(rawField, &field) != nil || field == nil {
				report("properties", name)
				continue
			}
			var kind FieldType
			_ = json.Unmarshal(field["type"], &kind)
			switch kind {
			case FieldTypeString:
				validateInputBounds(field, name, "minLength", "maxLength", 0, maxInputStringLength, report)
			case FieldTypeInteger:
				validateInputBounds(field, name, "minimum", "maximum", -maxSafeInteger, maxSafeInteger, report)
			case FieldTypeNumber:
				validateInputBounds(field, name, "minimum", "maximum", -math.MaxFloat64, math.MaxFloat64, report)
			case FieldTypeBoolean:
				if len(field) != 1 {
					report("properties", name)
				}
			default:
				report("properties", name, "type")
			}
			inputs.Properties[name] = kind
			if findings == 0 {
				shape := FieldSchema{Type: kind}
				for _, key := range []string{"minimum", "maximum", "minLength", "maxLength"} {
					if raw := field[key]; raw != nil {
						value, _ := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
						switch key {
						case "minimum":
							shape.Minimum = &value
						case "maximum":
							shape.Maximum = &value
						case "minLength":
							integer := int(value)
							shape.MinLength = &integer
						case "maxLength":
							integer := int(value)
							shape.MaxLength = &integer
						}
					}
				}
				compiled, err := compileField(shape)
				if err != nil {
					report("properties", name)
				} else {
					inputs.fields[name] = compiled
				}
			}
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
	if findings > 0 {
		return nil, ctx.Err()
	}
	return inputs, ctx.Err()
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
		value, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
		integerBound := lower == "minLength" || (min == -constants.MaxSafeInteger && max == constants.MaxSafeInteger)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || (integerBound && math.Trunc(value) != value) || value < min || value > max {
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

// ValueValid checks a scalar against the decoded workflow field without coercion.
func (s *WorkflowInputs) ValueValid(name string, raw json.RawMessage) bool {
	field, exists := s.fields[name]
	return exists && validValue(raw, field)
}
