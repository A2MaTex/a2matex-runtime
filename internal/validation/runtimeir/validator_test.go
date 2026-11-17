package runtimeir_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const ruleID = "019aaa00-1111-4111-8111-111111111111"

func admission(t *testing.T, version string, disabled ...string) (*runtimeir.Validator, *atomic.Int32) {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var entries []registry.Registration
	for _, kind := range []string{"flow.start", "flow.delay", "flow.end", "flow.switch", "flow.set_variable", "flow.wait_variable", "utility.log", "android.tap", "android.swipe", "android.long_press", "android.press_back", "android.press_home", "android.launch_app", "android.input_text", "android.screenshot"} {
		if contains(disabled, kind) {
			continue
		}
		definition, _ := catalog.CompilationDefinition(kind)
		entries = append(entries, registry.Registration{Type: kind, RuntimeHandler: definition.RuntimeHandler,
			Run: func(context.Context, registry.Invocation) (map[string]any, error) {
				calls.Add(1)
				return nil, errors.New("validation invoked a handler")
			}})
	}
	handlers, err := registry.New(catalog, entries)
	if err != nil {
		t.Fatal(err)
	}
	v, err := runtimeir.New(catalog, handlers, version)
	if err != nil {
		t.Fatal(err)
	}
	return v, &calls
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func raw(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
func literal(value any) json.RawMessage {
	return raw(map[string]any{"source": "LITERAL", "value": value})
}
func source(kind string, fields map[string]any) json.RawMessage {
	fields["source"] = kind
	return raw(fields)
}
func base(t *testing.T) *ir.Document {
	t.Helper()
	data, err := os.ReadFile("testdata/valid-delay.json")
	if err != nil {
		t.Fatal(err)
	}
	var d ir.Document
	if err = json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}
func derive(t *testing.T, d *ir.Document) {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d.MinimumRuntimeVersion = "0.0.0"
	d.RequiredCapabilities = []string{}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		def, ok := catalog.CompilationDefinition(n.Type)
		if !ok {
			continue
		}
		n.RuntimeHandler = def.RuntimeHandler
		if cmp, _ := ir.CompareVersions(def.MinimumRuntimeVersion, d.MinimumRuntimeVersion); cmp > 0 {
			d.MinimumRuntimeVersion = def.MinimumRuntimeVersion
		}
		for _, c := range def.Capabilities {
			if !contains(d.RequiredCapabilities, c) {
				d.RequiredCapabilities = append(d.RequiredCapabilities, c)
			}
		}
	}
}
func target(t *testing.T, kind string, config ir.Object, inputs ir.Object) *ir.Document {
	t.Helper()
	d := base(t)
	d.Nodes[1].Type = kind
	d.Nodes[1].Config = config
	d.Nodes[1].Inputs = inputs
	derive(t, d)
	return d
}
func check(t *testing.T, v *runtimeir.Validator, data json.RawMessage, inputs ...json.RawMessage) runtimeir.Result {
	t.Helper()
	if inputs == nil {
		inputs = []json.RawMessage{raw(map[string]any{})}
	}
	r, err := v.Validate(context.Background(), data, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Validation.Status == validation.StatusInvalid && (r.Document != nil || r.RawIR != nil || r.DeviceInputs != nil) {
		t.Fatal("rejection leaked accepted data")
	}
	return r
}
func wantCode(t *testing.T, r runtimeir.Result, code validation.Code) {
	t.Helper()
	for _, d := range r.Validation.Errors {
		if d.Code == code {
			return
		}
	}
	t.Fatalf("missing %s: %+v", code, r.Validation)
}
func wantValid(t *testing.T, r runtimeir.Result) {
	t.Helper()
	if r.Validation.Status != validation.StatusValid || r.FailureCode != "" || r.Document == nil || r.DeviceInputs == nil {
		t.Fatalf("not accepted: %+v", r)
	}
}

func TestGoldenAdmission(t *testing.T) {
	v, calls := admission(t, "0.1.0")
	data, err := os.ReadFile("testdata/valid-delay.json")
	if err != nil {
		t.Fatal(err)
	}
	r := check(t, v, data)
	wantValid(t, r)
	want := validation.BuildResult(nil, nil)
	if !reflect.DeepEqual(r.Validation, want) || !bytes.Equal(r.RawIR, data) || len(r.Document.Nodes) != 3 || len(r.DeviceInputs) != 1 || r.DeviceInputs[0] == nil {
		t.Fatalf("incomplete golden result: %+v", r)
	}
	if calls.Load() != 0 {
		t.Fatal("handler invoked")
	}
	for _, tc := range []struct {
		name, version, failure string
		mutate                 func(*ir.Document)
		inputs                 []json.RawMessage
		code                   validation.Code
		path                   validation.Path
		node                   string
	}{
		{"literal", "0.1.0", constants.ValidationFailed, func(d *ir.Document) { d.Nodes[1].Inputs["durationMs"] = literal(300001) }, nil, validation.CodeNodeValueBindingInvalid, validation.Path{"runtimeIr", "nodes", 1, "inputs", "durationMs", "value"}, "wait"},
		{"handler", "0.1.0", constants.ValidationFailed, func(d *ir.Document) { d.Nodes[1].RuntimeHandler = "flow.start" }, nil, validation.CodeNodeRuntimeHandlerInvalid, validation.Path{"runtimeIr", "nodes", 1, "runtimeHandler"}, "wait"},
		{"minimum", "0.1.0", constants.ValidationFailed, func(d *ir.Document) { d.MinimumRuntimeVersion = "0.0.0" }, nil, validation.CodeRuntimeIRMinimumVersionInvalid, validation.Path{"runtimeIr", "minimumRuntimeVersion"}, ""},
		{"device", "0.1.0", constants.ValidationFailed, nil, []json.RawMessage{raw(map[string]any{}), raw(map[string]any{"extra": 1})}, validation.CodeWorkflowInputInvalid, validation.Path{"devices", 1, "inputs", "extra"}, ""},
		{"development build", "0.0.0-dev", constants.RuntimeIncompatible, nil, nil, validation.CodeRuntimeVersionUnsupported, validation.Path{"runtimeIr", "minimumRuntimeVersion"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := admission(t, tc.version)
			d := base(t)
			if tc.mutate != nil {
				tc.mutate(d)
			}
			r := check(t, v, raw(d), tc.inputs...)
			want := validation.BuildResult([]validation.Diagnostic{validation.NewDiagnostic(tc.code, tc.path, tc.node, "")}, nil)
			if !reflect.DeepEqual(r.Validation, want) || r.FailureCode != tc.failure {
				t.Fatalf("got %+v, want %+v / %s", r, want, tc.failure)
			}
		})
	}
}

func TestStrictEnvelope(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing", func(d map[string]any) { delete(d, "nodes") }},
		{"null", func(d map[string]any) { d["edges"] = nil }},
		{"unknown", func(d map[string]any) { d["unexpected"] = 1 }},
		{"container", func(d map[string]any) { d["nodes"] = map[string]any{} }},
		{"identifier", func(d map[string]any) { d["entryNodeId"] = " bad" }},
		{"policy", func(d map[string]any) { d["nodes"].([]any)[1].(map[string]any)["timeoutMs"] = 0 }},
		{"retry", func(d map[string]any) {
			d["nodes"].([]any)[1].(map[string]any)["retryPolicy"] = map[string]any{"maxAttempts": 4, "delayMs": 0}
		}},
		{"source kind", func(d map[string]any) {
			d["nodes"].([]any)[1].(map[string]any)["inputs"] = map[string]any{"durationMs": map[string]any{"kind": "LITERAL", "value": 1}}
		}},
		{"source unknown field", func(d map[string]any) {
			d["nodes"].([]any)[1].(map[string]any)["inputs"] = map[string]any{"durationMs": map[string]any{"source": "LITERAL", "value": 1, "name": "oops"}}
		}},
		{"literal null", func(d map[string]any) {
			d["nodes"].([]any)[1].(map[string]any)["inputs"] = map[string]any{"durationMs": map[string]any{"source": "LITERAL", "value": nil}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			_ = json.Unmarshal(raw(base(t)), &m)
			tc.mutate(m)
			wantCode(t, check(t, v, raw(m)), validation.CodeRuntimeIRContractInvalid)
		})
	}
	for _, data := range []json.RawMessage{[]byte("{} {}"), []byte{0xff}, []byte("null"), []byte("[]")} {
		wantCode(t, check(t, v, data), validation.CodeRuntimeIRContractInvalid)
	}
	data := bytes.Replace(raw(base(t)), []byte(`"formatVersion":1`), []byte(`"formatVersion":99,"formatVersion":1.0`), 1)
	wantValid(t, check(t, v, data))
	d := base(t)
	d.FormatVersion = 2
	d.Nodes = nil
	r := check(t, v, raw(d))
	if len(r.Validation.Errors) != 1 || r.FailureCode != constants.RuntimeIncompatible {
		t.Fatalf("version gate: %+v", r)
	}
}

func TestCompatibilityAndTopology(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	for _, tc := range []struct {
		name   string
		mutate func(*ir.Document)
		code   validation.Code
	}{
		{"duplicate node", func(d *ir.Document) { d.Nodes[1].ID = "start" }, validation.CodeNodeIDDuplicated},
		{"duplicate edge", func(d *ir.Document) { d.Edges[1].ID = "e1" }, validation.CodeEdgeIDDuplicated},
		{"missing endpoint", func(d *ir.Document) { d.Edges[1].TargetNodeID = "missing" }, validation.CodeEdgeTargetNodeNotFound},
		{"start", func(d *ir.Document) { d.EntryNodeID = "wait" }, validation.CodeEntryNodeInvalid},
		{"unreachable", func(d *ir.Document) { d.Edges[0].TargetNodeID = "end" }, validation.CodeNodeUnreachable},
		{"cycle", func(d *ir.Document) { d.Edges[1].TargetNodeID = "start" }, validation.CodeGraphCycleDetected},
		{"start incoming", func(d *ir.Document) { d.Edges[1].TargetNodeID = "start" }, validation.CodeStartNodeHasIncomingEdge},
		{"end outgoing", func(d *ir.Document) {
			d.Edges = append(d.Edges, ir.Edge{ID: "e3", SourceNodeID: "end", SourcePort: "success", TargetNodeID: "wait"})
		}, validation.CodeEndNodeHasOutgoingEdge},
		{"port", func(d *ir.Document) { d.Edges[1].SourcePort = "default" }, validation.CodeEdgeSourcePortNotFound},
		{"missing success", func(d *ir.Document) { d.Edges = d.Edges[:1] }, validation.CodeNodePortEdgeCountInvalid},
		{"unknown node", func(d *ir.Document) { d.Nodes[1].Type = "future.node" }, validation.CodeNodeTypeNotActive},
		{"capabilities", func(d *ir.Document) { d.RequiredCapabilities = []string{"android.adb"} }, validation.CodeRuntimeIRCapabilitiesInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) { d := base(t); tc.mutate(d); wantCode(t, check(t, v, raw(d)), tc.code) })
	}
	d := target(t, "utility.log", ir.Object{}, ir.Object{"message": literal("hello")})
	disabled, calls := admission(t, "0.1.0", "utility.log")
	r := check(t, disabled, raw(d))
	wantCode(t, r, validation.CodeNodeRuntimeHandlerUnavailable)
	if r.FailureCode != constants.RuntimeIncompatible || calls.Load() != 0 {
		t.Fatalf("disabled handler: %+v", r)
	}
	for _, version := range []string{"0.0.1", "bad", "01.0.0", "0.1.0-beta"} {
		v, _ := admission(t, version)
		wantCode(t, check(t, v, raw(base(t))), validation.CodeRuntimeVersionUnsupported)
	}
	for _, version := range []string{"0.1.0", "10.0.0", strings.Repeat("9", 100) + ".0.0"} {
		v, _ := admission(t, version)
		wantValid(t, check(t, v, raw(base(t))))
	}
}

func TestTargetingAndAdapterInputs(t *testing.T) {
	v, calls := admission(t, "0.1.0")
	for _, tc := range []struct {
		name, kind, mode string
		inputs           ir.Object
		code             validation.Code
	}{
		{"tap pixels", "android.tap", "ABSOLUTE_PIXELS", ir.Object{"x": literal(1), "y": literal(2)}, ""},
		{"tap percent", "android.tap", "SCREEN_PERCENT", ir.Object{"xPercent": literal(100), "yPercent": literal(0)}, ""},
		{"tap selector", "android.tap", "SELECTOR_ID", ir.Object{"selector_id": literal("android:id/button1")}, ""},
		{"mode required", "android.tap", "", ir.Object{}, validation.CodeNodeConfigInvalid},
		{"fullscreen tap", "android.tap", "FULL_SCREEN", ir.Object{}, validation.CodeNodeConfigInvalid},
		{"mixed fields", "android.tap", "ABSOLUTE_PIXELS", ir.Object{"x": literal(1), "yPercent": literal(2)}, validation.CodeNodeBindingsInvalid},
		{"nonliteral", "android.tap", "ABSOLUTE_PIXELS", ir.Object{"x": source("FLOW_VARIABLE", map[string]any{"name": "x"}), "y": literal(2)}, validation.CodeNodeBindingsInvalid},
		{"long press max", "android.long_press", "ABSOLUTE_PIXELS", ir.Object{"x": literal(1), "y": literal(2), "durationMs": literal(60000)}, ""},
		{"long press too long", "android.long_press", "ABSOLUTE_PIXELS", ir.Object{"x": literal(1), "y": literal(2), "durationMs": literal(60001)}, validation.CodeNodeExecutionInputUnsupported},
		{"full screenshot", "android.screenshot", "FULL_SCREEN", ir.Object{}, ""},
		{"crop", "android.screenshot", "SCREEN_PERCENT", ir.Object{"startXPercent": literal(20), "startYPercent": literal(20), "endXPercent": literal(40), "endYPercent": literal(30)}, ""},
		{"reversed crop", "android.screenshot", "ABSOLUTE_PIXELS", ir.Object{"startX": literal(40), "startY": literal(20), "endX": literal(20), "endY": literal(30)}, validation.CodeNodeBindingsInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := ir.Object{}
			if tc.mode != "" {
				config["targetMode"] = raw(tc.mode)
			}
			d := target(t, tc.kind, config, tc.inputs)
			r := check(t, v, raw(d))
			if tc.code == "" {
				wantValid(t, r)
			} else {
				wantCode(t, r, tc.code)
				for _, e := range r.Validation.Errors {
					if len(e.Path) == 0 || e.Path[0] != "runtimeIr" {
						t.Fatalf("unprefixed path: %+v", e)
					}
				}
			}
		})
	}
	for _, text := range []string{"plain ASCII", "bad%sescape", "Tiếng Việt"} {
		d := target(t, "android.input_text", ir.Object{}, ir.Object{"text": literal(text)})
		r := check(t, v, raw(d))
		if text == "plain ASCII" {
			wantValid(t, r)
		} else {
			wantCode(t, r, validation.CodeNodeExecutionInputUnsupported)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("adapter invoked")
	}
}

func TestDeviceInputs(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	d := base(t)
	d.InputSchema = raw(map[string]any{"type": "object", "properties": map[string]any{"duration": map[string]any{"type": "integer", "minimum": 1, "maximum": 900000}}, "required": []string{"duration"}, "additionalProperties": false})
	d.Nodes[1].Inputs["durationMs"] = source("WORKFLOW_INPUT", map[string]any{"inputName": "duration"})
	for _, tc := range []struct {
		name  string
		value json.RawMessage
		code  validation.Code
	}{
		{"valid", raw(map[string]any{"duration": 10}), ""}, {"missing", raw(map[string]any{}), validation.CodeWorkflowInputInvalid},
		{"wrong type", raw(map[string]any{"duration": "10"}), validation.CodeWorkflowInputInvalid},
		{"fraction", raw(map[string]any{"duration": 1.2}), validation.CodeWorkflowInputInvalid},
		{"destination bound", raw(map[string]any{"duration": 300001}), validation.CodeNodeValueBindingInvalid},
		{"unknown", raw(map[string]any{"duration": 10, "extra": true}), validation.CodeWorkflowInputInvalid},
		{"null", raw(nil), validation.CodeWorkflowInputInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := check(t, v, raw(d), raw(map[string]any{"duration": 10}), tc.value)
			if tc.code == "" {
				wantValid(t, r)
			} else {
				wantCode(t, r, tc.code)
				if len(r.Validation.Errors) != 1 || r.Validation.Errors[0].Path[1] != 1 {
					t.Fatalf("device error duplicated or misplaced: %+v", r)
				}
			}
		})
	}
	d = target(t, "flow.set_variable", ir.Object{"name": raw("price"), "scope": raw("FLOW")}, ir.Object{"value": source("WORKFLOW_INPUT", map[string]any{"inputName": "price"})})
	d.InputSchema = raw(map[string]any{"type": "object", "properties": map[string]any{"price": map[string]any{"type": "number", "minimum": 0.5}}, "required": []string{"price"}, "additionalProperties": false})
	wantValid(t, check(t, v, raw(d), []byte(`{"price":1.25}`)))
	d = target(t, "android.input_text", ir.Object{}, ir.Object{"text": source("WORKFLOW_INPUT", map[string]any{"inputName": "text"})})
	d.InputSchema = raw(map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "minLength": 1.0}}, "required": []string{"text"}, "additionalProperties": false})
	wantCode(t, check(t, v, raw(d), []byte(`{"text":"bad%s"}`)), validation.CodeNodeExecutionInputUnsupported)
}

func TestOwnershipConcurrencyAndCancellation(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	data := raw(base(t))
	input := []byte("{}")
	r := check(t, v, data, input)
	saved := bytes.Clone(r.RawIR)
	for i := range data {
		data[i] = ' '
	}
	input[0] = '['
	if !bytes.Equal(r.RawIR, saved) || r.Document.Nodes[0].ID != "start" || len(r.DeviceInputs[0]) != 0 {
		t.Fatal("caller mutation affected result")
	}
	r.Document.Nodes[1].Config["changed"] = raw(true)
	for i := 0; i < 32; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) { t.Parallel(); wantValid(t, check(t, v, saved)) })
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := v.Validate(ctx, saved, []json.RawMessage{raw(map[string]any{})})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, runtimeir.Result{}) {
		t.Fatalf("cancellation: %+v %v", result, err)
	}
	ctx = &countedContext{Context: context.Background(), remaining: 10}
	result, err = v.Validate(ctx, saved, []json.RawMessage{raw(map[string]any{})})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, runtimeir.Result{}) {
		t.Fatalf("mid-validation cancellation: %+v %v", result, err)
	}
}

// A deterministic cancellation during traversal avoids timing-dependent sleeps.
type countedContext struct {
	context.Context
	remaining int
}

func (c *countedContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestBudgetsAndTruncation(t *testing.T) {
	v, calls := admission(t, "0.1.0", "utility.log")
	data := raw(base(t))
	data = append(data, bytes.Repeat([]byte(" "), constants.RuntimeIRMaxContentBytes-len(data))...)
	wantValid(t, check(t, v, data))
	_, err := v.Validate(context.Background(), append(data, ' '), []json.RawMessage{[]byte("{}")})
	if !errors.Is(err, xerror.ErrRuntimeIRPayloadTooLarge) {
		t.Fatalf("IR byte limit: %v", err)
	}
	inputs := make([]json.RawMessage, 32)
	for i := range inputs {
		inputs[i] = []byte("{}")
	}
	wantValid(t, check(t, v, raw(base(t)), inputs...))
	for _, count := range []int{0, 33} {
		r, err := v.Validate(context.Background(), raw(base(t)), make([]json.RawMessage, count))
		if err != nil {
			t.Fatal(err)
		}
		wantCode(t, r, validation.CodeRuntimeIRContractInvalid)
	}
	aggregate := []byte("{}")
	aggregate = append(aggregate, bytes.Repeat([]byte(" "), constants.RunRequestMaxContentBytes-len(data)-2)...)
	wantValid(t, check(t, v, data, aggregate))
	aggregate = append(aggregate, ' ')
	_, err = v.Validate(context.Background(), data, []json.RawMessage{aggregate})
	if !errors.Is(err, xerror.ErrRuntimeIRPayloadTooLarge) {
		t.Fatalf("aggregate limit: %v", err)
	}
	d := base(t)
	d.Nodes = d.Nodes[:1]
	d.Edges = nil
	for i := 0; i < 101; i++ {
		n := base(t).Nodes[1]
		n.ID = fmt.Sprintf("log%03d", i)
		n.Type = "utility.log"
		n.Inputs = ir.Object{"message": literal("hello")}
		d.Nodes = append(d.Nodes, n)
	}
	d.Nodes = append(d.Nodes, base(t).Nodes[2])
	for i := 1; i < len(d.Nodes); i++ {
		d.Edges = append(d.Edges, ir.Edge{ID: fmt.Sprintf("e%d", i), SourceNodeID: d.Nodes[i-1].ID, SourcePort: "success", TargetNodeID: d.Nodes[i].ID})
	}
	derive(t, d)
	r := check(t, v, raw(d), []byte(`{"extra":1}`))
	if r.FailureCode != constants.ValidationFailed || !r.Validation.Truncated || len(r.Validation.Errors) != 100 {
		t.Fatalf("cap precedence: %+v", r)
	}
	for _, finding := range r.Validation.Errors {
		if finding.Code != validation.CodeNodeRuntimeHandlerUnavailable {
			t.Fatalf("incorrect canonical cap: %+v", finding)
		}
	}
	for i := 0; i < 5; i++ {
		if other := check(t, v, raw(d), []byte(`{"extra":1}`)); !reflect.DeepEqual(r.Validation, other.Validation) {
			t.Fatal("unstable diagnostics")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("handler invoked")
	}
}

func TestCompilerOutputAccepted(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	compiler := validationservice.New(catalog)
	graph := []byte(`{"schemaVersion":1,"entryNodeId":"start","nodes":[{"id":"start","type":"flow.start","editor":{"x":0,"y":0},"config":{},"bindings":{},"errorPolicy":"STOP"},{"id":"end","type":"flow.end","editor":{"x":1,"y":0},"config":{},"bindings":{},"errorPolicy":"STOP"}],"edges":[{"id":"e1","sourceNodeId":"start","sourcePort":"success","targetNodeId":"end"}]}`)
	compiled, err := compiler.Compile(graph, base(t).InputSchema)
	if err != nil || compiled.Validation.Status != validation.StatusValid {
		t.Fatalf("compile: %+v %v", compiled, err)
	}
	v, _ := admission(t, "0.1.0")
	wantValid(t, check(t, v, compiled.RuntimeIR))
}
