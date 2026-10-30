package nodecontract

import (
	"math"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

func TestDefaultRegistryContainsApprovedContracts(t *testing.T) {
	t.Parallel()

	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	wantPorts := map[string][]string{
		"flow.start":         {"success"},
		"flow.end":           {},
		"flow.delay":         {"success", "error"},
		"utility.log":        {"success", "error"},
		"android.launch_app": {"success", "error"},
		"android.tap":        {"success", "error"},
		"android.swipe":      {"success", "error"},
		"android.input_text": {"success", "error"},
		"android.press_back": {"success", "error"},
		"android.press_home": {"success", "error"},
		"android.screenshot": {"success", "error"},
	}
	if len(registry.contracts) != len(wantPorts) {
		t.Fatalf("contract count = %d, want %d", len(registry.contracts), len(wantPorts))
	}
	for nodeType, ports := range wantPorts {
		contract, ok := registry.lookup(nodeType)
		if !ok {
			t.Fatalf("missing contract %q", nodeType)
		}
		if len(contract.properties) != 0 || len(contract.required) != 0 {
			t.Fatalf("contract %q does not have an empty config schema", nodeType)
		}
		if len(contract.outputPorts) != len(ports) {
			t.Fatalf("contract %q port count = %d, want %d", nodeType, len(contract.outputPorts), len(ports))
		}
		for _, port := range ports {
			if _, ok := contract.outputPorts[port]; !ok {
				t.Fatalf("contract %q missing port %q", nodeType, port)
			}
		}

		empty := &editorgraph.Graph{Nodes: []editorgraph.Node{testContractNode("node", nodeType, map[string]any{})}}
		if diagnostics := Validate(empty, registry); len(diagnostics) != 0 {
			t.Fatalf("contract %q rejected empty config: %+v", nodeType, diagnostics)
		}
		configured := &editorgraph.Graph{Nodes: []editorgraph.Node{testContractNode("node", nodeType, map[string]any{"unexpected": true})}}
		if diagnostics := Validate(configured, registry); countValidationCode(diagnostics, validation.CodeNodeConfigInvalid) != 1 {
			t.Fatalf("contract %q accepted unexpected config: %+v", nodeType, diagnostics)
		}
	}
}

func TestRegistryRejectsInvalidSystemContracts(t *testing.T) {
	t.Parallel()

	negative := -1
	nan := math.NaN()
	minimum := 2.0
	maximum := 1.0
	minLength := 2
	maxLength := 1
	tests := []struct {
		name      string
		contracts []Contract
	}{
		{name: "empty registry"},
		{name: "empty type", contracts: []Contract{{}}},
		{name: "duplicate type", contracts: []Contract{{Type: "flow.start"}, {Type: "flow.start"}}},
		{name: "empty port", contracts: []Contract{{Type: "flow.start", OutputPorts: []string{""}}}},
		{name: "duplicate port", contracts: []Contract{{Type: "flow.start", OutputPorts: []string{"success", "success"}}}},
		{name: "missing required property", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Required: []string{"missing"}}}}},
		{name: "duplicate required property", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Properties: map[string]FieldSchema{"value": {Type: FieldTypeBoolean}}, Required: []string{"value", "value"}}}}},
		{name: "unsupported type", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Properties: map[string]FieldSchema{"value": {Type: "object"}}}}}},
		{name: "nonfinite bound", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Properties: map[string]FieldSchema{"value": {Type: FieldTypeNumber, Minimum: &nan}}}}}},
		{name: "minimum exceeds maximum", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Properties: map[string]FieldSchema{"value": {Type: FieldTypeNumber, Minimum: &minimum, Maximum: &maximum}}}}}},
		{name: "negative length", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Properties: map[string]FieldSchema{"value": {Type: FieldTypeString, MinLength: &negative}}}}}},
		{name: "minimum length exceeds maximum", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Properties: map[string]FieldSchema{"value": {Type: FieldTypeString, MinLength: &minLength, MaxLength: &maxLength}}}}}},
		{name: "invalid pattern", contracts: []Contract{{Type: "test.config", Config: ConfigSchema{Properties: map[string]FieldSchema{"value": {Type: FieldTypeString, Pattern: "["}}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewRegistry(test.contracts); err == nil {
				t.Fatal("NewRegistry returned no error")
			}
		})
	}
}

func TestRegistryCopiesCallerCollections(t *testing.T) {
	t.Parallel()

	minimum := 1.0
	ports := []string{"success"}
	required := []string{"enabled"}
	properties := map[string]FieldSchema{
		"enabled": {Type: FieldTypeBoolean},
		"count":   {Type: FieldTypeNumber, Minimum: &minimum},
	}
	registry, err := NewRegistry([]Contract{{Type: "test.node", OutputPorts: ports, Config: ConfigSchema{Properties: properties, Required: required}}})
	if err != nil {
		t.Fatal(err)
	}
	ports[0] = "changed"
	required[0] = "changed"
	minimum = 99
	delete(properties, "enabled")
	contract, _ := registry.lookup("test.node")
	if _, ok := contract.outputPorts["success"]; !ok || contract.required[0] != "enabled" {
		t.Fatal("registry changed after caller mutation")
	}
	if _, ok := contract.properties["enabled"]; !ok {
		t.Fatal("registry property changed after caller mutation")
	}
	if got := *contract.properties["count"].Minimum; got != 1 {
		t.Fatalf("registry numeric bound = %v, want 1", got)
	}
}
