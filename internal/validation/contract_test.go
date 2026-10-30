package validation

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestBuildResultOrdersCapsAndDoesNotMutate(t *testing.T) {
	t.Parallel()

	errors := []Diagnostic{
		NewDiagnostic(CodeNodeUnreachable, Path{"nodes", 10}, "z", ""),
		NewDiagnostic(CodeNodeUnreachable, Path{"nodes", 2}, "a", ""),
	}
	original := append([]Diagnostic(nil), errors...)
	warnings := make([]Diagnostic, EditorGraphMaxDiagnostics)
	for index := range warnings {
		warnings[index] = NewDiagnostic(CodeGraphSchemaVersionUnsupported, Path{"schemaVersion", index}, "", "")
	}

	result := BuildResult(errors, warnings)
	if result.Status != StatusInvalid || len(result.Errors) != 2 || len(result.Warnings) != 98 || !result.Truncated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !reflect.DeepEqual(errors, original) {
		t.Fatal("BuildResult mutated caller-owned errors")
	}
	if result.Errors[0].NodeID != "a" {
		t.Fatalf("first sorted node = %q, want a", result.Errors[0].NodeID)
	}
}

func TestBuildResultStatusAndDiagnosticBoundaries(t *testing.T) {
	t.Parallel()

	warning := NewDiagnostic(CodeGraphSchemaVersionUnsupported, Path{"schemaVersion"}, "", "")
	errorDiagnostic := NewDiagnostic(CodeNodeUnreachable, Path{"nodes", 0}, "node", "")
	tests := []struct {
		name       string
		errors     []Diagnostic
		warnings   []Diagnostic
		wantStatus Status
	}{
		{name: "empty", wantStatus: StatusValid},
		{name: "warnings only", warnings: []Diagnostic{warning}, wantStatus: StatusValidWithWarnings},
		{name: "error", errors: []Diagnostic{errorDiagnostic}, wantStatus: StatusInvalid},
		{name: "error and warning", errors: []Diagnostic{errorDiagnostic}, warnings: []Diagnostic{warning}, wantStatus: StatusInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := BuildResult(test.errors, test.warnings)
			if result.Status != test.wantStatus || result.Errors == nil || result.Warnings == nil {
				t.Fatalf("result = %+v, want status %s and non-nil arrays", result, test.wantStatus)
			}
		})
	}

	exactlyLimit := make([]Diagnostic, EditorGraphMaxDiagnostics)
	for index := range exactlyLimit {
		exactlyLimit[index] = NewDiagnostic(CodeNodeUnreachable, Path{"nodes", index}, "node", "")
	}
	result := BuildResult(exactlyLimit, nil)
	if len(result.Errors) != EditorGraphMaxDiagnostics || result.Truncated {
		t.Fatalf("exact-limit result = %+v", result)
	}

	overLimit := append(append([]Diagnostic(nil), exactlyLimit...), errorDiagnostic)
	result = BuildResult(overLimit, []Diagnostic{warning})
	if len(result.Errors) != EditorGraphMaxDiagnostics || len(result.Warnings) != 0 || !result.Truncated {
		t.Fatalf("over-limit result = %+v", result)
	}
}

func TestBuildResultOrderingIsDeterministic(t *testing.T) {
	t.Parallel()

	diagnostics := []Diagnostic{
		NewDiagnostic(CodeNodeUnreachable, Path{"nodes", 10}, "node-b", ""),
		NewDiagnostic(CodeNodeUnreachable, Path{"nodes", 2}, "node-b", ""),
		NewDiagnostic(CodeNodeUnreachable, Path{"nodes", "2"}, "node-b", ""),
		NewDiagnostic(CodeNodeUnreachable, Path{"nodes", 2}, "node-a", ""),
		NewDiagnostic(CodeNodeUnreachable, Path{"nodes", 2}, "", ""),
	}
	first := BuildResult(diagnostics, nil)
	reversed := append([]Diagnostic(nil), diagnostics...)
	slices.Reverse(reversed)
	second := BuildResult(reversed, nil)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ordering changed with input order\nfirst:  %+v\nsecond: %+v", first, second)
	}
	wantPaths := []Path{{"nodes", 2}, {"nodes", 2}, {"nodes", 2}, {"nodes", 10}, {"nodes", "2"}}
	for index, want := range wantPaths {
		if !reflect.DeepEqual(first.Errors[index].Path, want) {
			t.Fatalf("path[%d] = %#v, want %#v", index, first.Errors[index].Path, want)
		}
	}
}

func TestBuildResultJSONUsesRequiredArraysAndPaths(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(BuildResult(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"status":"VALID","errors":[],"warnings":[],"truncated":false}` {
		t.Fatalf("JSON = %s", encoded)
	}
}

func TestEveryDiagnosticCodeHasMessage(t *testing.T) {
	t.Parallel()

	codes := []Code{
		CodeEditorGraphContractInvalid, CodeGraphSchemaVersionUnsupported, CodeNodeIDDuplicated,
		CodeEdgeIDDuplicated, CodeStartNodeCountInvalid, CodeEntryNodeInvalid, CodeEndNodeUnreachable,
		CodeEdgeSourceNodeNotFound, CodeEdgeTargetNodeNotFound, CodeEdgeSourcePortNotFound,
		CodeEndNodeHasOutgoingEdge, CodeNodeUnreachable, CodeGraphCycleDetected,
		CodeNodeTypeNotActive, CodeNodeConfigInvalid, CodeGraphNodeLimitExceeded,
		CodeGraphEdgeLimitExceeded,
	}
	for _, code := range codes {
		if Message(code) == "" {
			t.Fatalf("code %q has no message", code)
		}
	}
}
