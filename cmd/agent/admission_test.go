package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
)

func TestAdmissionUsesActualEnabledRegistryWithoutDispatch(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	// A nil dispatcher is deliberately unusable. Validation must never invoke it.
	handlers, err := newHandlerRegistry(catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := runtimeir.New(catalog, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../internal/validation/runtimeir/testdata/valid-delay.json")
	if err != nil {
		t.Fatal(err)
	}
	var document ir.Document
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"flow.delay", "android.tap", "android.screenshot"} {
		t.Run(kind, func(t *testing.T) {
			n := &document.Nodes[1]
			n.Type = kind
			n.RuntimeHandler = kind
			n.Config = ir.Object{}
			n.Inputs = ir.Object{}
			if kind == "flow.delay" {
				n.Inputs["durationMs"] = json.RawMessage(`{"source":"LITERAL","value":10}`)
			} else if kind == "android.tap" {
				n.Config["targetMode"] = json.RawMessage(`"ABSOLUTE_PIXELS"`)
				n.Inputs["x"] = json.RawMessage(`{"source":"LITERAL","value":1}`)
				n.Inputs["y"] = json.RawMessage(`{"source":"LITERAL","value":1}`)
			} else {
				n.Config["targetMode"] = json.RawMessage(`"FULL_SCREEN"`)
			}
			def, _ := catalog.CompilationDefinition(kind)
			document.RequiredCapabilities = def.Capabilities
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			result, err := v.Validate(context.Background(), data, []json.RawMessage{[]byte("{}")})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "android.screenshot" {
				if len(result.Validation.Errors) != 1 || result.Validation.Errors[0].Code != validation.CodeNodeRuntimeHandlerUnavailable {
					t.Fatalf("disabled Screenshot accepted: %+v", result)
				}
			} else if result.Validation.Status != validation.StatusValid {
				t.Fatalf("enabled pair rejected: %+v", result)
			}
		})
	}
}
