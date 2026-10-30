package nodecontract

import (
	"errors"
	"fmt"
	"math"
	"regexp"
)

type FieldType string

const (
	FieldTypeString  FieldType = "string"
	FieldTypeInteger FieldType = "integer"
	FieldTypeNumber  FieldType = "number"
	FieldTypeBoolean FieldType = "boolean"
)

type FieldSchema struct {
	Type      FieldType
	Minimum   *float64
	Maximum   *float64
	MinLength *int
	MaxLength *int
	Pattern   string
}

type ConfigSchema struct {
	Properties map[string]FieldSchema
	Required   []string
}

type Contract struct {
	Type        string
	Config      ConfigSchema
	OutputPorts []string
}

type compiledField struct {
	FieldSchema
	pattern *regexp.Regexp
}

type compiledContract struct {
	properties  map[string]compiledField
	required    []string
	outputPorts map[string]struct{}
}

type Registry struct {
	contracts map[string]compiledContract
}

func NewRegistry(contracts []Contract) (*Registry, error) {
	if len(contracts) == 0 {
		return nil, errors.New("node-contract registry requires at least one definition")
	}
	registry := &Registry{contracts: make(map[string]compiledContract, len(contracts))}
	for _, contract := range contracts {
		if contract.Type == "" {
			return nil, errors.New("node-contract type is empty")
		}
		if _, exists := registry.contracts[contract.Type]; exists {
			return nil, fmt.Errorf("duplicate node-contract type %q", contract.Type)
		}
		compiled, err := compileContract(contract)
		if err != nil {
			return nil, fmt.Errorf("invalid node-contract %q: %w", contract.Type, err)
		}
		registry.contracts[contract.Type] = compiled
	}
	return registry, nil
}

func NewDefaultRegistry() (*Registry, error) {
	empty := func(nodeType string, ports ...string) Contract {
		return Contract{Type: nodeType, Config: ConfigSchema{Properties: map[string]FieldSchema{}}, OutputPorts: ports}
	}
	return NewRegistry([]Contract{
		empty("flow.start", "success"),
		empty("flow.end"),
		empty("flow.delay", "success", "error"),
		empty("utility.log", "success", "error"),
		empty("android.launch_app", "success", "error"),
		empty("android.tap", "success", "error"),
		empty("android.swipe", "success", "error"),
		empty("android.input_text", "success", "error"),
		empty("android.press_back", "success", "error"),
		empty("android.press_home", "success", "error"),
		empty("android.screenshot", "success", "error"),
	})
}

func compileContract(contract Contract) (compiledContract, error) {
	compiled := compiledContract{
		properties:  make(map[string]compiledField, len(contract.Config.Properties)),
		required:    append([]string(nil), contract.Config.Required...),
		outputPorts: make(map[string]struct{}, len(contract.OutputPorts)),
	}
	for name, schema := range contract.Config.Properties {
		field, err := compileField(schema)
		if err != nil {
			return compiledContract{}, fmt.Errorf("property %q: %w", name, err)
		}
		compiled.properties[name] = field
	}
	required := make(map[string]struct{}, len(compiled.required))
	for _, name := range compiled.required {
		if _, exists := compiled.properties[name]; !exists {
			return compiledContract{}, fmt.Errorf("required property %q is not defined", name)
		}
		if _, exists := required[name]; exists {
			return compiledContract{}, fmt.Errorf("required property %q is duplicated", name)
		}
		required[name] = struct{}{}
	}
	for _, port := range contract.OutputPorts {
		if port == "" {
			return compiledContract{}, errors.New("output port is empty")
		}
		if _, exists := compiled.outputPorts[port]; exists {
			return compiledContract{}, fmt.Errorf("output port %q is duplicated", port)
		}
		compiled.outputPorts[port] = struct{}{}
	}
	return compiled, nil
}

func compileField(schema FieldSchema) (compiledField, error) {
	switch schema.Type {
	case FieldTypeString, FieldTypeInteger, FieldTypeNumber, FieldTypeBoolean:
	default:
		return compiledField{}, fmt.Errorf("unsupported field type %q", schema.Type)
	}
	if invalidNumber(schema.Minimum) || invalidNumber(schema.Maximum) {
		return compiledField{}, errors.New("numeric bound is not finite")
	}
	if schema.Minimum != nil && schema.Maximum != nil && *schema.Minimum > *schema.Maximum {
		return compiledField{}, errors.New("minimum exceeds maximum")
	}
	if schema.MinLength != nil && *schema.MinLength < 0 || schema.MaxLength != nil && *schema.MaxLength < 0 {
		return compiledField{}, errors.New("string length is negative")
	}
	if schema.MinLength != nil && schema.MaxLength != nil && *schema.MinLength > *schema.MaxLength {
		return compiledField{}, errors.New("minimum string length exceeds maximum")
	}
	compiled := compiledField{FieldSchema: cloneFieldSchema(schema)}
	if schema.Pattern != "" {
		pattern, err := regexp.Compile(schema.Pattern)
		if err != nil {
			return compiledField{}, errors.New("string pattern is invalid")
		}
		compiled.pattern = pattern
	}
	return compiled, nil
}

func cloneFieldSchema(schema FieldSchema) FieldSchema {
	clone := schema
	if schema.Minimum != nil {
		value := *schema.Minimum
		clone.Minimum = &value
	}
	if schema.Maximum != nil {
		value := *schema.Maximum
		clone.Maximum = &value
	}
	if schema.MinLength != nil {
		value := *schema.MinLength
		clone.MinLength = &value
	}
	if schema.MaxLength != nil {
		value := *schema.MaxLength
		clone.MaxLength = &value
	}
	return clone
}

func invalidNumber(value *float64) bool {
	return value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0))
}

func (r *Registry) lookup(nodeType string) (compiledContract, bool) {
	contract, ok := r.contracts[nodeType]
	return contract, ok
}
