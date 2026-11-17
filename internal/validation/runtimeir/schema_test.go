package runtimeir_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestWorkflowSchemaVocabularyAndValues(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	for _, tc := range []struct {
		name          string
		schema, value json.RawMessage
		code          validation.Code
	}{
		{"number", raw(map[string]any{"type": "number", "minimum": 0.5, "maximum": 2.5}), raw(1.25), ""},
		{"number bound", raw(map[string]any{"type": "number", "minimum": 0.5, "maximum": 2.5}), raw(3), validation.CodeWorkflowInputInvalid},
		{"integer", raw(map[string]any{"type": "integer"}), json.RawMessage("1.0"), ""},
		{"unsafe integer", raw(map[string]any{"type": "integer"}), json.RawMessage("9007199254740992"), validation.CodeWorkflowInputInvalid},
		{"boolean", raw(map[string]any{"type": "boolean"}), raw(false), ""},
		{"null", raw(map[string]any{"type": "number"}), raw(nil), validation.CodeWorkflowInputInvalid},
		{"object", raw(map[string]any{"type": "number"}), raw(map[string]int{"value": 1}), validation.CodeWorkflowInputInvalid},
		{"nonfinite", raw(map[string]any{"type": "number"}), json.RawMessage("1e400"), validation.CodeWorkflowInputInvalid},
		{"fractional integer bound", raw(map[string]any{"type": "integer", "minimum": 0.5}), raw(1), validation.CodeWorkflowInputSchemaInvalid},
		{"unsupported schema", raw(map[string]any{"type": "array"}), raw(1), validation.CodeWorkflowInputSchemaInvalid},
		{"unknown constraint", raw(map[string]any{"type": "number", "pattern": "x"}), raw(1), validation.CodeWorkflowInputSchemaInvalid},
		{"UTF16 string bound", raw(map[string]any{"type": "string", "maxLength": 1}), raw("😀"), validation.CodeWorkflowInputInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := base(t)
			d.InputSchema = raw(map[string]any{"type": "object", "properties": map[string]any{"value": tc.schema}, "required": []string{"value"}, "additionalProperties": false})
			// Assemble the map directly to retain an otherwise syntactically valid overflowing number.
			input := append(append([]byte(`{"value":`), tc.value...), '}')
			r := check(t, v, raw(d), input)
			if tc.code == "" {
				wantValid(t, r)
			} else {
				wantCode(t, r, tc.code)
			}
		})
	}
	for _, count := range []int{100, 101} {
		d := base(t)
		fields := map[string]any{}
		for i := 0; i < count; i++ {
			fields[fmt.Sprintf("field%d", i)] = map[string]any{"type": "number"}
		}
		d.InputSchema = raw(map[string]any{"type": "object", "properties": fields, "required": []string{}, "additionalProperties": false})
		r := check(t, v, raw(d))
		if count == 100 {
			wantValid(t, r)
		} else {
			wantCode(t, r, validation.CodeWorkflowInputSchemaInvalid)
		}
	}
}

func TestRequiredAndExactWorkflowInputTypes(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	for _, tc := range []struct {
		kind     string
		required []string
	}{
		{"number", []string{"duration"}}, {"integer", []string{}},
	} {
		d := base(t)
		d.InputSchema = raw(map[string]any{"type": "object", "properties": map[string]any{"duration": map[string]any{"type": tc.kind}}, "required": tc.required, "additionalProperties": false})
		d.Nodes[1].Inputs = ir.Object{"durationMs": source("WORKFLOW_INPUT", map[string]any{"inputName": "duration"})}
		wantCode(t, check(t, v, raw(d), json.RawMessage(`{"duration":10}`)), validation.CodeNodeValueBindingInvalid)
	}
}

func TestMissingDependenciesAndDeadline(t *testing.T) {
	if _, err := runtimeir.New(nil, nil, "0.1.0"); !errors.Is(err, xerror.ErrRuntimeIRDependencies) {
		t.Fatalf("missing dependencies: %v", err)
	}
	v, _ := admission(t, "0.1.0")
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	r, err := v.Validate(ctx, raw(base(t)), []json.RawMessage{json.RawMessage("{}")})
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(r, runtimeir.Result{}) {
		t.Fatalf("deadline: %+v %v", r, err)
	}
}
