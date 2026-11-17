package nodecontract

import (
	"encoding/json"
	"slices"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
)

// InputContract is an owned view of one static or instance-derived node input.
type InputContract struct {
	Schema   FieldSchema
	Required bool
	Sources  []string
}

func (r *Registry) inputField(nodeType string, config ir.Object, name string) (compiledField, bool) {
	definition, known := r.catalog[nodeType]
	if !known {
		return compiledField{}, false
	}
	if modes := targetingInputs[nodeType]; modes != nil {
		field, ok := modes[bindingString(config[targetModeField])][name]
		return field, ok
	}
	if field, ok := definition.inputs[name]; ok {
		return field, true
	}
	if nodeType == switchType {
		rules := switchRules(nodeView{Config: config})
		operator, ok := rules[name]
		if ok && needsOperand(operator) {
			return definition.dynamicOperand, true
		}
	}
	return compiledField{}, false
}

func (r *Registry) inputKinds(nodeType string, field compiledField) []string {
	if len(field.BindingKinds) == 0 {
		return slices.Clone(r.catalog[nodeType].BindingKinds)
	}
	kinds := make([]string, 0, len(field.BindingKinds))
	for kind := range field.BindingKinds {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds
}

func (r *Registry) Input(nodeType string, config ir.Object, name string) (InputContract, bool) {
	field, known := r.inputField(nodeType, config, name)
	if !known {
		return InputContract{}, false
	}
	required := slices.Contains(r.catalog[nodeType].InputSchema.Required, name)
	if targetingInputs[nodeType] != nil || (nodeType == switchType && name != "value") {
		required = true
	}
	return InputContract{Schema: cloneFieldSchema(field.FieldSchema), Required: required,
		Sources: r.inputKinds(nodeType, field)}, true
}

func (r *Registry) ValidInputValue(nodeType string, config ir.Object, name string, value json.RawMessage) bool {
	field, known := r.inputField(nodeType, config, name)
	return known && validValue(value, field)
}
