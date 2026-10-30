package service_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/topology"
)

const backendReferenceCommit = "f7e6ef8b89c8d1b5a70e1d921fdae022b8aa75e8"

func TestBackendReferenceFixtureHashes(t *testing.T) {
	t.Parallel()

	fixtures := map[string]string{
		"valid-minimal.json": "5b563688cb9268d2354862da75f7d834e5115f1ecbd9152ef91b7e838af7be04",
		"invalid-cycle.json": "8ff462b07d228a1ed18174eb658bd155ac2746609d4a625002ec19a3b57ad61d",
	}
	for name, want := range fixtures {
		contents, err := os.ReadFile("../testdata/backend-reference/f7e6ef8/" + name)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		if got := hex.EncodeToString(digest[:]); got != want {
			t.Fatalf("fixture %s hash = %s, want %s", name, got, want)
		}
	}
}

func TestBackendReferenceContractAndServiceParity(t *testing.T) {
	t.Parallel()

	service := defaultService(t)
	base := fixtureRawGraph(t, "valid-minimal.json")
	tests := []struct {
		name string
		raw  func(t *testing.T) []byte
		want validation.Result
	}{
		{name: "valid.minimal", raw: func(*testing.T) []byte { return base }, want: valid()},
		{name: "version.unsupported", raw: mutateObject(base, func(root map[string]any) { root["schemaVersion"] = 2 }), want: invalid(d(validation.CodeGraphSchemaVersionUnsupported, validation.Path{"schemaVersion"}, "", ""))},
		{name: "contract.missing_version", raw: mutateObject(base, func(root map[string]any) { delete(root, "schemaVersion") }), want: invalid(d(validation.CodeEditorGraphContractInvalid, validation.Path{"schemaVersion"}, "", ""))},
		{name: "contract.non_integer_version", raw: mutateObject(base, func(root map[string]any) { root["schemaVersion"] = 1.5 }), want: invalid(d(validation.CodeEditorGraphContractInvalid, validation.Path{"schemaVersion"}, "", ""))},
		{name: "contract.unknown_field", raw: mutateObject(base, func(root map[string]any) { root["unknown"] = true }), want: invalid(d(validation.CodeEditorGraphContractInvalid, validation.Path{}, "", ""))},
		{
			name: "contract.invalid_json_safe_fields",
			raw: mutateObject(base, func(root map[string]any) {
				root["entryNodeId"] = "  "
				node := root["nodes"].([]any)[0].(map[string]any)
				node["type"] = "Start"
				node["config"] = []any{}
			}),
			want: invalid(
				d(validation.CodeEditorGraphContractInvalid, validation.Path{"entryNodeId"}, "", ""),
				d(validation.CodeEditorGraphContractInvalid, validation.Path{"nodes", 0, "config"}, "", ""),
				d(validation.CodeEditorGraphContractInvalid, validation.Path{"nodes", 0, "type"}, "", ""),
			),
		},
		{
			name: "contract.malformed_service_input",
			raw:  func(*testing.T) []byte { return []byte(`{"nodes":[],"unexpected":true}`) },
			want: invalid(
				d(validation.CodeEditorGraphContractInvalid, validation.Path{}, "", ""),
				d(validation.CodeEditorGraphContractInvalid, validation.Path{"edges"}, "", ""),
				d(validation.CodeEditorGraphContractInvalid, validation.Path{"entryNodeId"}, "", ""),
				d(validation.CodeEditorGraphContractInvalid, validation.Path{"schemaVersion"}, "", ""),
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := service.Validate(test.raw(t))
			if err != nil {
				t.Fatal(err)
			}
			assertFunctionalResult(t, got, test.want)
		})
	}

	t.Run("limit.content_bytes", func(t *testing.T) {
		t.Parallel()
		_, err := service.Validate(bytes.Repeat([]byte("x"), validation.EditorGraphMaxContentBytes+1))
		if !errors.Is(err, validationservice.ErrPayloadTooLarge) {
			t.Fatalf("error = %v, want ErrPayloadTooLarge", err)
		}
	})
}

func TestBackendReferenceTopologyParity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		graph graphFactory
		want  func(graph *editorgraph.Graph) validation.Result
	}{
		{
			name: "identity.duplicate_node_and_edge",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				graph.Nodes = append(graph.Nodes, graph.Nodes[1])
				graph.Edges = append(graph.Edges, graph.Edges[0])
			}),
			want: fixed(invalid(
				d(validation.CodeEdgeIDDuplicated, validation.Path{"edges", 1, "id"}, "", "edge_01"),
				d(validation.CodeNodeIDDuplicated, validation.Path{"nodes", 2, "id"}, "end_01", ""),
			)),
		},
		{
			name:  "start.none",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) { graph.Nodes[0].Type = "flow.delay" }),
			want: fixed(invalid(
				d(validation.CodeEndNodeUnreachable, validation.Path{"nodes"}, "", ""),
				d(validation.CodeEntryNodeInvalid, validation.Path{"entryNodeId"}, "start_01", ""),
				d(validation.CodeNodeUnreachable, validation.Path{"nodes", 1}, "end_01", ""),
				d(validation.CodeNodeUnreachable, validation.Path{"nodes", 0}, "start_01", ""),
				d(validation.CodeStartNodeCountInvalid, validation.Path{"nodes"}, "", ""),
			)),
		},
		{
			name:  "start.wrong_entry",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) { graph.EntryNodeID = "end_01" }),
			want:  fixed(invalid(d(validation.CodeEntryNodeInvalid, validation.Path{"entryNodeId"}, "end_01", ""))),
		},
		{
			name: "start.multiple",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				node := graph.Nodes[0]
				node.ID = "start_02"
				graph.Nodes = append(graph.Nodes, node)
			}),
			want: fixed(invalid(
				d(validation.CodeEndNodeUnreachable, validation.Path{"nodes"}, "", ""),
				d(validation.CodeEntryNodeInvalid, validation.Path{"entryNodeId"}, "start_01", ""),
				d(validation.CodeNodeUnreachable, validation.Path{"nodes", 1}, "end_01", ""),
				d(validation.CodeNodeUnreachable, validation.Path{"nodes", 0}, "start_01", ""),
				d(validation.CodeNodeUnreachable, validation.Path{"nodes", 2}, "start_02", ""),
				d(validation.CodeStartNodeCountInvalid, validation.Path{"nodes"}, "", ""),
			)),
		},
		{
			name: "edge.missing_endpoints",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				graph.Edges = append(graph.Edges,
					editorgraph.Edge{ID: "missing_source", SourceNodeID: "unknown_source", SourcePort: "success", TargetNodeID: "end_01"},
					editorgraph.Edge{ID: "missing_target", SourceNodeID: "start_01", SourcePort: "success", TargetNodeID: "unknown_target"},
				)
			}),
			want: fixed(invalid(
				d(validation.CodeEdgeSourceNodeNotFound, validation.Path{"edges", 1, "sourceNodeId"}, "unknown_source", "missing_source"),
				d(validation.CodeEdgeTargetNodeNotFound, validation.Path{"edges", 2, "targetNodeId"}, "unknown_target", "missing_target"),
			)),
		},
		{
			name: "end.outgoing_edge",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				graph.Edges = append(graph.Edges, editorgraph.Edge{ID: "edge_from_end", SourceNodeID: "end_01", SourcePort: "success", TargetNodeID: "start_01"})
			}),
			want: fixed(invalid(
				d(validation.CodeEndNodeHasOutgoingEdge, validation.Path{"edges", 1, "sourceNodeId"}, "end_01", "edge_from_end"),
				d(validation.CodeGraphCycleDetected, validation.Path{"edges", 1}, "start_01", "edge_from_end"),
			)),
		},
		{
			name:  "reachability.disconnected_end",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) { graph.Edges = []editorgraph.Edge{} }),
			want: fixed(invalid(
				d(validation.CodeEndNodeUnreachable, validation.Path{"nodes"}, "", ""),
				d(validation.CodeNodeUnreachable, validation.Path{"nodes", 1}, "end_01", ""),
			)),
		},
		{
			name:  "cycle.self",
			graph: fixtureGraph,
			want:  fixed(invalid(d(validation.CodeGraphCycleDetected, validation.Path{"edges", 1}, "delay_01", "edge_02"))),
		},
		{
			name: "cycle.multiple_nodes",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				delay := editorgraph.Node{ID: "delay_01", Type: "flow.delay", Config: object(), Bindings: object(), ErrorPolicy: editorgraph.ErrorPolicyStop, Editor: editorgraph.Position{X: 220, Y: 180}}
				graph.Nodes = append(graph.Nodes[:1], append([]editorgraph.Node{delay}, graph.Nodes[1:]...)...)
				graph.Edges = []editorgraph.Edge{
					{ID: "edge_01", SourceNodeID: "start_01", SourcePort: "success", TargetNodeID: "delay_01"},
					{ID: "edge_02", SourceNodeID: "delay_01", SourcePort: "success", TargetNodeID: "start_01"},
					{ID: "edge_03", SourceNodeID: "delay_01", SourcePort: "success", TargetNodeID: "end_01"},
				}
			}),
			want: fixed(invalid(d(validation.CodeGraphCycleDetected, validation.Path{"edges", 1}, "start_01", "edge_02"))),
		},
		{
			name: "limit.nodes",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				graph.Nodes = make([]editorgraph.Node, validation.EditorGraphMaxNodes+1)
				for index := range graph.Nodes {
					graph.Nodes[index] = editorgraph.Node{ID: "node_" + jsonNumber(index), Type: "flow.start", Config: object(), Bindings: object(), ErrorPolicy: editorgraph.ErrorPolicyStop}
				}
			}),
			want: fixed(invalid(d(validation.CodeGraphNodeLimitExceeded, validation.Path{"nodes"}, "", ""))),
		},
		{
			name: "limit.edges",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				baseEdge := graph.Edges[0]
				graph.Edges = make([]editorgraph.Edge, validation.EditorGraphMaxEdges+1)
				for index := range graph.Edges {
					graph.Edges[index] = baseEdge
					graph.Edges[index].ID = "edge_" + jsonNumber(index)
				}
			}),
			want: fixed(invalid(d(validation.CodeGraphEdgeLimitExceeded, validation.Path{"edges"}, "", ""))),
		},
		{
			name: "limit.diagnostics",
			graph: mutateGraph("valid-minimal.json", func(graph *editorgraph.Graph) {
				for index := 0; index < validation.EditorGraphMaxDiagnostics+1; index++ {
					graph.Nodes = append(graph.Nodes, editorgraph.Node{ID: "unreachable_" + jsonNumber(index), Type: "flow.delay", Config: object(), Bindings: object(), ErrorPolicy: editorgraph.ErrorPolicyStop})
				}
			}),
			want: diagnosticLimitResult,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixtureName := "valid-minimal.json"
			if test.name == "cycle.self" {
				fixtureName = "invalid-cycle.json"
			}
			graph := test.graph(t, fixtureName)
			topologyResult := topology.Validate(graph)
			got := validation.BuildResult(topologyResult.Diagnostics, nil)
			assertFunctionalResult(t, got, test.want(graph))
		})
	}
}

func TestBackendReferenceNodeContractParity(t *testing.T) {
	t.Parallel()

	t.Run("catalog.node_and_port", func(t *testing.T) {
		t.Parallel()
		graph := fixtureGraph(t, "valid-minimal.json")
		graph.Edges[0].SourcePort = "unknown"
		registry := registry(t, []nodecontract.Contract{emptyContract("flow.start", "success")})
		got := validation.BuildResult(nodecontract.Validate(graph, registry), nil)
		want := invalid(
			d(validation.CodeEdgeSourcePortNotFound, validation.Path{"edges", 0, "sourcePort"}, "start_01", "edge_01"),
			d(validation.CodeNodeTypeNotActive, validation.Path{"nodes", 1, "type"}, "end_01", ""),
		)
		assertFunctionalResult(t, got, want)
	})

	minimum, maximum := 1.0, 10.0
	minLength, maxLength := 2, 4
	configContract := nodecontract.Contract{
		Type: "test.config",
		Config: nodecontract.ConfigSchema{
			Properties: map[string]nodecontract.FieldSchema{
				"requiredValue": {Type: nodecontract.FieldTypeInteger, Minimum: &minimum, Maximum: &maximum},
				"name":          {Type: nodecontract.FieldTypeString, MinLength: &minLength, MaxLength: &maxLength, Pattern: `^[A-Z]+$`},
				"enabled":       {Type: nodecontract.FieldTypeBoolean},
			},
			Required: []string{"requiredValue"},
		},
		OutputPorts: []string{"success"},
	}
	tests := []struct {
		name  string
		value map[string]any
		field string
	}{
		{name: "config.required_missing", value: map[string]any{}, field: "requiredValue"},
		{name: "config.integer_type", value: map[string]any{"requiredValue": 1.5}, field: "requiredValue"},
		{name: "config.integer_maximum", value: map[string]any{"requiredValue": 11}, field: "requiredValue"},
		{name: "config.string_constraints", value: map[string]any{"requiredValue": 5, "name": "wrong"}, field: "name"},
		{name: "config.boolean_type", value: map[string]any{"requiredValue": 5, "name": "ABC", "enabled": "yes"}, field: "enabled"},
		{name: "config.additional_property", value: map[string]any{"requiredValue": 5, "name": "ABC", "extra": true}, field: "extra"},
		{name: "config.empty_property_name", value: map[string]any{"requiredValue": 5, "": true}, field: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			graph := fixtureGraph(t, "valid-minimal.json")
			configured := editorgraph.Node{ID: "configured_01", Type: "test.config", Config: rawObject(t, test.value), Bindings: object(), ErrorPolicy: editorgraph.ErrorPolicyStop}
			graph.Nodes = append(graph.Nodes[:1], append([]editorgraph.Node{configured}, graph.Nodes[1:]...)...)
			registry := registry(t, []nodecontract.Contract{emptyContract("flow.start", "success"), emptyContract("flow.end"), configContract})
			got := validation.BuildResult(nodecontract.Validate(graph, registry), nil)
			want := invalid(d(validation.CodeNodeConfigInvalid, validation.Path{"nodes", 1, "config", test.field}, "configured_01", ""))
			assertFunctionalResult(t, got, want)
		})
	}
}

type graphFactory func(t *testing.T, fixtureName string) *editorgraph.Graph

func mutateGraph(fixtureName string, mutate func(*editorgraph.Graph)) graphFactory {
	return func(t *testing.T, _ string) *editorgraph.Graph {
		graph := fixtureGraph(t, fixtureName)
		mutate(graph)
		return graph
	}
}

func fixtureGraph(t *testing.T, fixtureName string) *editorgraph.Graph {
	t.Helper()
	raw := fixtureRawGraph(t, fixtureName)
	graph, diagnostics := editorgraph.Decode(raw)
	if graph == nil || len(diagnostics) != 0 {
		t.Fatalf("reference fixture did not decode: %+v", diagnostics)
	}
	return graph
}

func fixtureRawGraph(t *testing.T, fixtureName string) []byte {
	t.Helper()
	contents, err := os.ReadFile("../testdata/backend-reference/f7e6ef8/" + fixtureName)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		EditorGraph json.RawMessage `json:"editorGraph"`
	}
	if err := json.Unmarshal(contents, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.EditorGraph
}

func mutateObject(base []byte, mutate func(map[string]any)) func(*testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		var object map[string]any
		if err := json.Unmarshal(base, &object); err != nil {
			t.Fatal(err)
		}
		mutate(object)
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
}

func defaultService(t *testing.T) *validationservice.Service {
	t.Helper()
	registry, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return validationservice.New(registry)
}

func registry(t *testing.T, contracts []nodecontract.Contract) *nodecontract.Registry {
	t.Helper()
	registry, err := nodecontract.NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func emptyContract(nodeType string, ports ...string) nodecontract.Contract {
	return nodecontract.Contract{Type: nodeType, Config: nodecontract.ConfigSchema{Properties: map[string]nodecontract.FieldSchema{}}, OutputPorts: ports}
}

func rawObject(t *testing.T, value map[string]any) editorgraph.JSONObject {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object editorgraph.JSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func object() editorgraph.JSONObject { return editorgraph.JSONObject{} }

func jsonNumber(value int) string {
	return strconv.Itoa(value)
}

func d(code validation.Code, path validation.Path, nodeID, edgeID string) validation.Diagnostic {
	return validation.NewDiagnostic(code, path, nodeID, edgeID)
}

func invalid(diagnostics ...validation.Diagnostic) validation.Result {
	return validation.Result{
		Status:   validation.StatusInvalid,
		Errors:   diagnostics,
		Warnings: []validation.Diagnostic{},
	}
}

func valid() validation.Result {
	return validation.Result{
		Status:   validation.StatusValid,
		Errors:   []validation.Diagnostic{},
		Warnings: []validation.Diagnostic{},
	}
}

func fixed(result validation.Result) func(*editorgraph.Graph) validation.Result {
	return func(*editorgraph.Graph) validation.Result { return result }
}

func diagnosticLimitResult(*editorgraph.Graph) validation.Result {
	diagnostics := make([]validation.Diagnostic, 0, validation.EditorGraphMaxDiagnostics)
	for _, index := range lexicalIntegers(validation.EditorGraphMaxDiagnostics)[:validation.EditorGraphMaxDiagnostics] {
		diagnostics = append(diagnostics, d(
			validation.CodeNodeUnreachable,
			validation.Path{"nodes", index + 2},
			"unreachable_"+jsonNumber(index),
			"",
		))
	}
	return validation.Result{Status: validation.StatusInvalid, Errors: diagnostics, Warnings: []validation.Diagnostic{}, Truncated: true}
}

func lexicalIntegers(maximum int) []int {
	values := []int{0}
	var appendBranch func(int)
	appendBranch = func(prefix int) {
		if prefix > maximum {
			return
		}
		values = append(values, prefix)
		for digit := 0; digit <= 9; digit++ {
			child := prefix*10 + digit
			if child > maximum {
				return
			}
			appendBranch(child)
		}
	}
	for first := 1; first <= 9; first++ {
		appendBranch(first)
	}
	return values
}

func assertFunctionalResult(t *testing.T, got, want validation.Result) {
	t.Helper()
	for index := range got.Errors {
		got.Errors[index].Message = ""
	}
	for index := range got.Warnings {
		got.Warnings[index].Message = ""
	}
	for index := range want.Errors {
		want.Errors[index].Message = ""
	}
	for index := range want.Warnings {
		want.Warnings[index].Message = ""
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Backend reference %s mismatch\ngot:  %#v\nwant: %#v", backendReferenceCommit, got, want)
	}
}
