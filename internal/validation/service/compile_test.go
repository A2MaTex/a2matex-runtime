package service_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
)

func TestBackendCompilationParity(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("../testdata/compilation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name        string
		EditorGraph json.RawMessage
		InputSchema json.RawMessage
		Validation  validation.Result
		RuntimeIR   json.RawMessage
	}
	if err := json.Unmarshal(fixture, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("invalid compilation fixture: %v", err)
	}
	compiler := defaultService(t)
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			got, err := compiler.Compile(tc.EditorGraph, tc.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			actualValidation, err := json.Marshal(got.Validation)
			if err != nil {
				t.Fatal(err)
			}
			expectedValidation, err := json.Marshal(tc.Validation)
			if err != nil {
				t.Fatal(err)
			}
			if string(actualValidation) != string(expectedValidation) {
				t.Fatalf("validation = %s, want %s", actualValidation, expectedValidation)
			}
			var actual, expected any
			raw := got.RuntimeIR
			if raw == nil {
				raw = json.RawMessage("null")
			}
			if err := json.Unmarshal(raw, &actual); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(tc.RuntimeIR, &expected); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("IR = %s, want %s", raw, tc.RuntimeIR)
			}
		})
	}
}
