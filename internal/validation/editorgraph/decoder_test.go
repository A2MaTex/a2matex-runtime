package editorgraph

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
)

const minimalGraph = `{"schemaVersion":1,"entryNodeId":"start","nodes":[{"id":"start","type":"flow.start","config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":0,"y":0}},{"id":"end","type":"flow.end","config":{},"bindings":{},"errorPolicy":"STOP","editor":{"x":1,"y":0}}],"edges":[{"id":"edge","sourceNodeId":"start","sourcePort":"success","targetNodeId":"end"}]}`

func TestDecodeContractBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		wantGraph bool
		wantCode  validation.Code
	}{
		{name: "valid", raw: minimalGraph, wantGraph: true},
		{name: "unsupported version", raw: strings.Replace(minimalGraph, `"schemaVersion":1`, `"schemaVersion":2`, 1), wantGraph: true, wantCode: validation.CodeGraphSchemaVersionUnsupported},
		{name: "unknown root field", raw: strings.Replace(minimalGraph, `"schemaVersion":1`, `"schemaVersion":1,"unknown":true`, 1), wantCode: validation.CodeEditorGraphContractInvalid},
		{name: "unknown node field", raw: strings.Replace(minimalGraph, `"id":"start"`, `"id":"start","unknown":true`, 1), wantCode: validation.CodeEditorGraphContractInvalid},
		{name: "unknown editor field", raw: strings.Replace(minimalGraph, `"x":0,"y":0`, `"x":0,"y":0,"z":0`, 1), wantCode: validation.CodeEditorGraphContractInvalid},
		{name: "null required field", raw: strings.Replace(minimalGraph, `"entryNodeId":"start"`, `"entryNodeId":null`, 1), wantCode: validation.CodeEditorGraphContractInvalid},
		{name: "invalid version", raw: strings.Replace(minimalGraph, `"schemaVersion":1`, `"schemaVersion":1.5`, 1), wantCode: validation.CodeEditorGraphContractInvalid},
		{name: "invalid trailing value", raw: minimalGraph + `{}`, wantCode: validation.CodeEditorGraphContractInvalid},
		{name: "invalid UTF-8", raw: minimalGraph[:10] + string([]byte{0xff}) + minimalGraph[10:], wantCode: validation.CodeEditorGraphContractInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			graph, diagnostics := Decode([]byte(test.raw))
			if (graph != nil) != test.wantGraph {
				t.Fatalf("graph presence = %t, want %t", graph != nil, test.wantGraph)
			}
			if test.wantCode != "" && (len(diagnostics) == 0 || diagnostics[0].Code != test.wantCode) {
				t.Fatalf("diagnostics = %+v, want code %s", diagnostics, test.wantCode)
			}
		})
	}
}

func TestDecodeStrictFieldContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		path validation.Path
	}{
		{name: "missing field", raw: strings.Replace(minimalGraph, `"entryNodeId":"start",`, "", 1), path: validation.Path{"entryNodeId"}},
		{name: "null node field", raw: strings.Replace(minimalGraph, `"id":"start"`, `"id":null`, 1), path: validation.Path{"nodes", 0, "id"}},
		{name: "unknown edge field", raw: strings.Replace(minimalGraph, `"id":"edge"`, `"id":"edge","unknown":true`, 1), path: validation.Path{"edges", 0}},
		{name: "empty identifier", raw: strings.Replace(minimalGraph, `"entryNodeId":"start"`, `"entryNodeId":""`, 1), path: validation.Path{"entryNodeId"}},
		{name: "whitespace identifier", raw: strings.Replace(minimalGraph, `"entryNodeId":"start"`, `"entryNodeId":"  "`, 1), path: validation.Path{"entryNodeId"}},
		{name: "overlength identifier", raw: strings.Replace(minimalGraph, `"entryNodeId":"start"`, `"entryNodeId":"`+strings.Repeat("a", 101)+`"`, 1), path: validation.Path{"entryNodeId"}},
		{name: "invalid node type case", raw: strings.Replace(minimalGraph, `"type":"flow.start"`, `"type":"Flow.Start"`, 1), path: validation.Path{"nodes", 0, "type"}},
		{name: "invalid node type shape", raw: strings.Replace(minimalGraph, `"type":"flow.start"`, `"type":"flow"`, 1), path: validation.Path{"nodes", 0, "type"}},
		{name: "overlength node type", raw: strings.Replace(minimalGraph, `"type":"flow.start"`, `"type":"flow.`+strings.Repeat("a", 251)+`"`, 1), path: validation.Path{"nodes", 0, "type"}},
		{name: "unknown error policy", raw: strings.Replace(minimalGraph, `"errorPolicy":"STOP"`, `"errorPolicy":"CONTINUE"`, 1), path: validation.Path{"nodes", 0, "errorPolicy"}},
		{name: "coordinate overflow", raw: strings.Replace(minimalGraph, `"x":0`, `"x":1e309`, 1), path: validation.Path{"nodes", 0, "editor", "x"}},
		{name: "version zero", raw: strings.Replace(minimalGraph, `"schemaVersion":1`, `"schemaVersion":0`, 1), path: validation.Path{"schemaVersion"}},
		{name: "version above safe integer", raw: strings.Replace(minimalGraph, `"schemaVersion":1`, `"schemaVersion":9007199254740992`, 1), path: validation.Path{"schemaVersion"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			graph, diagnostics := Decode([]byte(test.raw))
			if graph != nil || !containsPath(diagnostics, test.path) {
				t.Fatalf("graph=%+v diagnostics=%+v, want path %#v", graph, diagnostics, test.path)
			}
			for _, diagnostic := range diagnostics {
				if strings.Contains(diagnostic.Message, "CONTINUE") || strings.Contains(diagnostic.Message, "9007199254740992") {
					t.Fatalf("diagnostic leaked rejected value: %+v", diagnostic)
				}
			}
		})
	}
}

func TestDecodeNormalizesIdentifiersAndPreservesOpaqueObjects(t *testing.T) {
	t.Parallel()

	raw := strings.Replace(minimalGraph, `"entryNodeId":"start"`, "\"entryNodeId\":\" \\uFEFFstart\\u00a0\"", 1)
	raw = strings.Replace(raw, `"config":{}`, `"config":{"nested":{"value":1},"array":[true,null,"x"],"duplicate":1,"duplicate":2}`, 1)
	raw = strings.Replace(raw, `"bindings":{}`, `"bindings":{"object":{"enabled":false},"number":1.5}`, 1)
	raw = strings.Replace(raw, `"errorPolicy":"STOP"`, `"errorPolicy":"ERROR_BRANCH"`, 1)
	raw = strings.Replace(raw, `"x":0`, `"x":1.5`, 1)

	first, firstDiagnostics := Decode([]byte(raw))
	second, secondDiagnostics := Decode([]byte(raw))
	if first == nil || len(firstDiagnostics) != 0 || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstDiagnostics, secondDiagnostics) {
		t.Fatalf("decode was not stable: first=%+v/%+v second=%+v/%+v", first, firstDiagnostics, second, secondDiagnostics)
	}
	if first.EntryNodeID != "start" || first.Nodes[0].ErrorPolicy != ErrorPolicyErrorBranch || first.Nodes[0].Editor.X != 1.5 {
		t.Fatalf("normalized graph = %+v", first)
	}
	var duplicate int
	if err := json.Unmarshal(first.Nodes[0].Config["duplicate"], &duplicate); err != nil || duplicate != 2 {
		t.Fatalf("opaque duplicate value = %s, err=%v", first.Nodes[0].Config["duplicate"], err)
	}
	if _, ok := first.Nodes[0].Config["nested"]; !ok {
		t.Fatal("nested opaque config was not preserved")
	}
	if _, ok := first.Nodes[0].Bindings["object"]; !ok {
		t.Fatal("nested opaque bindings were not preserved")
	}
}

func TestDecodeUsesLastDuplicateValueAndUTF16IdentifierLimit(t *testing.T) {
	t.Parallel()

	raw := strings.Replace(minimalGraph, `"entryNodeId":"start"`, `"entryNodeId":"wrong","entryNodeId":"start"`, 1)
	graph, diagnostics := Decode([]byte(raw))
	if graph == nil || len(diagnostics) != 0 || graph.EntryNodeID != "start" {
		t.Fatalf("duplicate field result: graph=%+v diagnostics=%+v", graph, diagnostics)
	}

	overlong := strings.Repeat("😀", 51)
	raw = strings.Replace(minimalGraph, `"entryNodeId":"start"`, `"entryNodeId":"`+overlong+`"`, 1)
	graph, diagnostics = Decode([]byte(raw))
	if graph != nil || len(diagnostics) == 0 || diagnostics[0].Path[0] != "entryNodeId" {
		t.Fatalf("UTF-16 limit result: graph=%+v diagnostics=%+v", graph, diagnostics)
	}
}

func containsPath(diagnostics []validation.Diagnostic, want validation.Path) bool {
	for _, diagnostic := range diagnostics {
		if reflect.DeepEqual(diagnostic.Path, want) {
			return true
		}
	}
	return false
}
