package nodecontract

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
)

// catalogV1 is the Backend BASIC_NODE_DEFINITIONS projection captured for this build.
//
//go:embed catalog_v1.json
var catalogV1 []byte

var catalogCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type CompilationDefinition struct {
	RuntimeHandler        string
	DefaultTimeoutMS      int64
	MinimumRuntimeVersion string
	Capabilities          []string
}

type catalogNode struct {
	Type string
	CompilationDefinition
	BindingKinds   []string
	InputSchema    ConfigSchema
	OutputSchema   ConfigSchema
	inputs         map[string]compiledField
	outputs        compiledContract
	dynamicOperand compiledField
}

func (r *Registry) installCatalog() error {
	var nodes []catalogNode
	if err := json.Unmarshal(catalogV1, &nodes); err != nil {
		return fmt.Errorf("decode installed node catalog: %w", err)
	}
	r.catalog = make(map[string]catalogNode, len(nodes))
	for _, node := range nodes {
		if _, ok := r.contracts[node.Type]; !ok || r.catalog[node.Type].Type != "" ||
			!validCatalogCode(node.RuntimeHandler) || !versionPattern.MatchString(node.MinimumRuntimeVersion) ||
			node.DefaultTimeoutMS < 1 || node.DefaultTimeoutMS > validation.NodeTimeoutMaxMS || node.Capabilities == nil {
			return fmt.Errorf("invalid installed definition for %q", node.Type)
		}
		for _, capability := range node.Capabilities {
			if !validCatalogCode(capability) {
				return fmt.Errorf("invalid installed capability for %q", node.Type)
			}
		}
		input, err := compileContract(Contract{Config: node.InputSchema})
		if err != nil {
			return fmt.Errorf("invalid installed inputs for %q: %w", node.Type, err)
		}
		node.inputs = input.properties
		output, err := compileContract(Contract{Config: node.OutputSchema})
		if err != nil {
			return fmt.Errorf("invalid installed outputs for %q: %w", node.Type, err)
		}
		node.outputs = output
		if node.Type == switchType {
			var operand FieldSchema
			if json.Unmarshal(node.InputSchema.DynamicInputs["ruleOperand"], &operand) != nil {
				return fmt.Errorf("invalid installed Switch operand definition")
			}
			compiled, err := compileField(operand)
			if err != nil {
				return fmt.Errorf("invalid installed Switch operand: %w", err)
			}
			node.dynamicOperand = compiled
		}
		r.catalog[node.Type] = node
	}
	if len(r.catalog) != len(r.contracts) {
		return fmt.Errorf("installed catalog and validator have different node inventories")
	}
	return nil
}

func (r *Registry) CompilationDefinition(nodeType string) (CompilationDefinition, bool) {
	node, ok := r.catalog[nodeType]
	definition := node.CompilationDefinition
	definition.Capabilities = slices.Clone(definition.Capabilities)
	return definition, ok
}

// ValidOutput checks a strict output object against the installed compiled fields.
func (r *Registry) ValidOutput(nodeType string, output ir.Object) bool {
	node, known := r.catalog[nodeType]
	if !known || output == nil {
		return false
	}
	for _, name := range node.outputs.required {
		if _, present := output[name]; !present {
			return false
		}
	}
	for name, value := range output {
		field, declared := node.outputs.properties[name]
		if !declared || !validValue(value, field) {
			return false
		}
	}
	return true
}

func validCatalogCode(value string) bool {
	return len(value) <= validation.NodeTypeMaxLength && catalogCodePattern.MatchString(value)
}
