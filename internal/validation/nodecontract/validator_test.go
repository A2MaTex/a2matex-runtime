package nodecontract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

func TestValidateNodeTypesAndConfigOwnership(t *testing.T) {
	t.Parallel()

	registry := mustRegistry(t, []Contract{{
		Type:        "known.node",
		Config:      ConfigSchema{Properties: map[string]FieldSchema{}},
		OutputPorts: []string{"success"},
	}})
	graph := &editorgraph.Graph{
		Nodes: []editorgraph.Node{
			testContractNode("unknown", "unknown.node", map[string]any{"ignored": true}),
			testContractNode("case", "Known.Node", map[string]any{}),
		},
		Edges: []editorgraph.Edge{
			{ID: "unknown_edge", SourceNodeID: "unknown", SourcePort: "invalid", TargetNodeID: "missing"},
			{ID: "case_edge", SourceNodeID: "case", SourcePort: "invalid", TargetNodeID: "missing"},
		},
	}
	diagnostics := Validate(graph, registry)
	if countValidationCode(diagnostics, validation.CodeNodeTypeNotActive) != 2 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if hasValidationCode(diagnostics, validation.CodeNodeConfigInvalid) || hasValidationCode(diagnostics, validation.CodeEdgeSourcePortNotFound) {
		t.Fatalf("unknown node types leaked into config or port validation: %+v", diagnostics)
	}
}

func TestValidateReducedConfigSchema(t *testing.T) {
	t.Parallel()

	minimum, maximum := 1.0, 10.0
	minLength, maxLength := 2, 4
	registry := mustRegistry(t, []Contract{{
		Type: "test.config",
		Config: ConfigSchema{
			Properties: map[string]FieldSchema{
				"integer": {Type: FieldTypeInteger, Minimum: &minimum, Maximum: &maximum},
				"number":  {Type: FieldTypeNumber, Minimum: &minimum, Maximum: &maximum},
				"name":    {Type: FieldTypeString, MinLength: &minLength, MaxLength: &maxLength, Pattern: `^[A-Z😀]+$`},
				"enabled": {Type: FieldTypeBoolean},
			},
			Required: []string{"integer"},
		},
		OutputPorts: []string{"success"},
	}})
	tests := []struct {
		name       string
		config     map[string]any
		wantFields []string
	}{
		{name: "valid boundaries", config: map[string]any{"integer": 1, "number": 10, "name": "😀", "enabled": true}},
		{name: "missing required", config: map[string]any{}, wantFields: []string{"integer"}},
		{name: "additional property", config: map[string]any{"integer": 1, "extra": true}, wantFields: []string{"extra"}},
		{name: "integer fractional", config: map[string]any{"integer": 1.5}, wantFields: []string{"integer"}},
		{name: "integer below minimum", config: map[string]any{"integer": 0}, wantFields: []string{"integer"}},
		{name: "number above maximum", config: map[string]any{"integer": 1, "number": 10.5}, wantFields: []string{"number"}},
		{name: "UTF-16 over maximum", config: map[string]any{"integer": 1, "name": "😀😀a"}, wantFields: []string{"name"}},
		{name: "pattern", config: map[string]any{"integer": 1, "name": "ab"}, wantFields: []string{"name"}},
		{name: "boolean type", config: map[string]any{"integer": 1, "enabled": "true"}, wantFields: []string{"enabled"}},
		{name: "invalid scalar kinds", config: map[string]any{"integer": []any{1}, "number": nil, "name": map[string]any{}, "enabled": 1}, wantFields: []string{"enabled", "integer", "name", "number"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			graph := &editorgraph.Graph{Nodes: []editorgraph.Node{testContractNode("node", "test.config", test.config)}}
			result := validation.BuildResult(Validate(graph, registry), nil)
			gotFields := make([]string, 0, len(result.Errors))
			for _, diagnostic := range result.Errors {
				if diagnostic.Code != validation.CodeNodeConfigInvalid {
					t.Fatalf("unexpected diagnostic: %+v", diagnostic)
				}
				gotFields = append(gotFields, diagnostic.Path[len(diagnostic.Path)-1].(string))
			}
			if !slices.Equal(gotFields, test.wantFields) {
				t.Fatalf("fields = %v, want %v; diagnostics=%+v", gotFields, test.wantFields, result.Errors)
			}
		})
	}
}

func TestValidateSourcePortsAndEndpointOwnership(t *testing.T) {
	t.Parallel()

	registry := mustRegistry(t, []Contract{
		{Type: "flow.start", Config: ConfigSchema{Properties: map[string]FieldSchema{}}, OutputPorts: []string{"success"}},
		{Type: "flow.end", Config: ConfigSchema{Properties: map[string]FieldSchema{}}},
	})
	tests := []struct {
		name      string
		graph     *editorgraph.Graph
		wantCount int
	}{
		{name: "valid port", graph: graphWithEdge("start", "success", "end")},
		{name: "unknown port", graph: graphWithEdge("start", "unknown", "end"), wantCount: 1},
		{name: "case mismatched port", graph: graphWithEdge("start", "Success", "end"), wantCount: 1},
		{name: "End rejects every source port", graph: graphWithEdge("end", "success", "start"), wantCount: 1},
		{name: "missing source belongs to C3", graph: graphWithEdge("missing", "unknown", "end")},
		{name: "missing target does not suppress source port", graph: graphWithEdge("start", "unknown", "missing"), wantCount: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			diagnostics := Validate(test.graph, registry)
			if got := countValidationCode(diagnostics, validation.CodeEdgeSourcePortNotFound); got != test.wantCount {
				t.Fatalf("port diagnostic count = %d, want %d; diagnostics=%+v", got, test.wantCount, diagnostics)
			}
		})
	}

	duplicate := graphWithEdge("same", "success", "target")
	duplicate.Nodes = []editorgraph.Node{
		testContractNode("same", "flow.start", map[string]any{}),
		testContractNode("same", "flow.end", map[string]any{}),
		testContractNode("target", "flow.end", map[string]any{}),
	}
	if got := countValidationCode(Validate(duplicate, registry), validation.CodeEdgeSourcePortNotFound); got != 1 {
		t.Fatalf("duplicate ID did not use last occurrence, port diagnostics=%d", got)
	}
}

func TestValidateIsDeterministicReadOnlyAndBounded(t *testing.T) {
	t.Parallel()

	registry := mustRegistry(t, []Contract{{Type: "test.node", Config: ConfigSchema{Properties: map[string]FieldSchema{}}, OutputPorts: []string{"success"}}})
	graph := &editorgraph.Graph{Nodes: make([]editorgraph.Node, validation.EditorGraphMaxNodes), Edges: make([]editorgraph.Edge, validation.EditorGraphMaxEdges)}
	for index := range graph.Nodes {
		graph.Nodes[index] = testContractNode(fmt.Sprintf("node_%03d", index), "test.node", map[string]any{})
	}
	for index := range graph.Edges {
		graph.Edges[index] = editorgraph.Edge{ID: fmt.Sprintf("edge_%04d", index), SourceNodeID: graph.Nodes[index%len(graph.Nodes)].ID, SourcePort: "success", TargetNodeID: graph.Nodes[(index+1)%len(graph.Nodes)].ID}
	}
	original := deepCopyGraph(t, graph)
	first := validation.BuildResult(Validate(graph, registry), nil)
	second := validation.BuildResult(Validate(graph, registry), nil)
	if first.Status != validation.StatusValid || !reflect.DeepEqual(first, second) {
		t.Fatalf("near-limit validation was not valid and deterministic: first=%+v second=%+v", first, second)
	}
	if !reflect.DeepEqual(graph, original) {
		t.Fatal("Validate mutated graph or opaque JSON")
	}
}

func graphWithEdge(source, port, target string) *editorgraph.Graph {
	return &editorgraph.Graph{
		Nodes: []editorgraph.Node{
			testContractNode("start", "flow.start", map[string]any{}),
			testContractNode("end", "flow.end", map[string]any{}),
		},
		Edges: []editorgraph.Edge{{ID: "edge", SourceNodeID: source, SourcePort: port, TargetNodeID: target}},
	}
}

func testContractNode(id, nodeType string, config map[string]any) editorgraph.Node {
	raw := make(editorgraph.JSONObject, len(config))
	for field, value := range config {
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		raw[field] = encoded
	}
	return editorgraph.Node{ID: id, Type: nodeType, Config: raw, Bindings: editorgraph.JSONObject{}, ErrorPolicy: editorgraph.ErrorPolicyStop}
}

func mustRegistry(t *testing.T, contracts []Contract) *Registry {
	t.Helper()
	registry, err := NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func deepCopyGraph(t *testing.T, graph *editorgraph.Graph) *editorgraph.Graph {
	t.Helper()
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	var clone editorgraph.Graph
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func hasValidationCode(diagnostics []validation.Diagnostic, code validation.Code) bool {
	return countValidationCode(diagnostics, code) > 0
}

func countValidationCode(diagnostics []validation.Diagnostic, code validation.Code) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			count++
		}
	}
	return count
}
