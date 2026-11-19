package setvariable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestPrepareExactContractAndOwnedScalar(t *testing.T) {
	for _, tc := range []struct {
		name, config, inputs string
		valid                bool
	}{
		{"number", `{"name":"score","scope":"FLOW"}`, `{"value": 10.0 }`, true},
		{"false", `{"name":"score","scope":"RUN_GROUP"}`, `{"value":false}`, true},
		{"empty", `{"name":"score","scope":"FLOW"}`, `{"value":""}`, true},
		{"zero", `{"name":"score","scope":"FLOW"}`, `{"value":0}`, true},
		{"missing scope", `{"name":"score"}`, `{"value":1}`, false},
		{"wrong scope", `{"name":"score","scope":"GLOBAL"}`, `{"value":1}`, false},
		{"invalid name", `{"name":"private bad name","scope":"FLOW"}`, `{"value":1}`, false},
		{"extra config", `{"name":"score","scope":"FLOW","extra":1}`, `{"value":1}`, false},
		{"extra input", `{"name":"score","scope":"FLOW"}`, `{"value":1,"extra":1}`, false},
		{"wrong input", `{"name":"score","scope":"FLOW"}`, `{"other":1}`, false},
		{"null", `{"name":"score","scope":"FLOW"}`, `{"value":null}`, false},
		{"array", `{"name":"score","scope":"FLOW"}`, `{"value":[]}`, false},
		{"object", `{"name":"score","scope":"FLOW"}`, `{"value":{}}`, false},
		{"nonfinite", `{"name":"score","scope":"FLOW"}`, `{"value":1e999}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config, inputs ir.Object
			if err := json.Unmarshal([]byte(tc.config), &config); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.inputs), &inputs); err != nil {
				t.Fatal(err)
			}
			got, err := Prepare(config, inputs)
			if tc.valid {
				if err != nil || len(got.Value) == 0 || got.RunGroupID != "" {
					t.Fatal(got, err)
				}
				before := string(got.Value)
				inputs["value"][0] = 'x'
				if string(got.Value) != before {
					t.Fatal("candidate aliases input")
				}
			} else {
				var command xerror.CommandError
				if !errors.As(err, &command) || command.Code != constants.CommandInputUnsupported || command.Retryable || strings.Contains(err.Error(), "private") {
					t.Fatal(got, err)
				}
			}
		})
	}
}

func TestInvokeRequiresLiveAttemptAndReturnsEmptyOutput(t *testing.T) {
	call := registry.Invocation{Config: ir.Object{"name": []byte(`"score"`), "scope": []byte(`"FLOW"`)}, Inputs: ir.Object{"value": []byte(`true`)}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	got, err := Invoke(ctx, call)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	cancel()
	if _, err := Invoke(ctx, call); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Invoke(t.Context(), call); err == nil {
		t.Fatal("unbounded attempt accepted")
	}
}

func TestPrepareScalarSizeLimit(t *testing.T) {
	config := ir.Object{"name": []byte(`"score"`), "scope": []byte(`"FLOW"`)}
	for _, size := range []int{constants.MaxVariableValueBytes, constants.MaxVariableValueBytes + 1} {
		value := []byte(`"` + strings.Repeat("a", size-2) + `"`)
		got, err := Prepare(config, ir.Object{"value": value})
		if size == constants.MaxVariableValueBytes {
			if err != nil || len(got.Value) != size {
				t.Fatal(len(got.Value), err)
			}
		} else if err == nil {
			t.Fatal("oversized scalar accepted")
		}
	}
}
