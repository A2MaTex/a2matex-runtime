package bindings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func resolver(t *testing.T) *bindings.Resolver {
	t.Helper()
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	r, err := bindings.New(definitions)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func inputNode(kind, field string, source json.RawMessage) ir.Node {
	return ir.Node{ID: "node", Type: kind, Config: ir.Object{}, Inputs: ir.Object{field: source}}
}

func literal(value string) json.RawMessage {
	return json.RawMessage(`{"source":"LITERAL","value":` + value + `}`)
}

func failure(t *testing.T, err error, field, reason, operation string) {
	t.Helper()
	var binding *xerror.InputBindingError
	var context *xerror.OperationError
	var command xerror.CommandError
	if !errors.As(err, &binding) || binding.Field != field ||
		!errors.As(err, &context) || context.Detail != reason || context.Operation != operation ||
		!errors.As(err, &command) || command.Code != constants.CommandInputUnsupported || command.Retryable {
		t.Fatalf("unexpected binding failure: %v (%+v, %+v, %+v)", err, binding, context, command)
	}
	messages := map[string]string{
		"input_definition_unavailable": "Node input definition is unavailable",
		"binding_missing":              "Node input binding is unavailable",
		"binding_invalid":              "Input binding does not match its contract",
		"source_unsupported":           "Input binding source is not supported",
		"workflow_input_missing":       "Workflow input value is unavailable",
		"input_value_invalid":          "Resolved input does not satisfy its field contract",
		"node_output_unavailable":      "Previous node output is unavailable",
		"node_output_field_missing":    "Previous node output field is unavailable",
		"variable_missing":             "Variable value is unavailable",
	}
	if command.Message != messages[reason] || err.Error() != operation+": "+messages[reason] {
		t.Fatalf("unsafe or incorrect message: %v", err)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	r, err := bindings.New(nil)
	if r != nil || !errors.Is(err, xerror.ErrInputBindingDependencies) {
		t.Fatalf("missing definitions: %v, %v", r, err)
	}
}

func TestResolveScalars(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for _, tc := range []struct{ name, kind, field, value string }{
		{"string", "android.input_text", "text", `"hello"`},
		{"escaped string", "android.input_text", "text", `"hello\nworld"`},
		{"empty string", "android.input_text", "text", `""`},
		{"integer", "flow.delay", "durationMs", `10`},
		{"integral number", "flow.delay", "durationMs", `10.0`},
		{"exponent", "flow.delay", "durationMs", `1e2`},
		{"lower bound", "flow.delay", "durationMs", `0`},
		{"upper bound", "flow.delay", "durationMs", `300000`},
		{"boolean false", "flow.set_variable", "value", `false`},
		{"boolean true", "flow.set_variable", "value", `true`},
		{"decimal", "flow.set_variable", "value", `1.25`},
		{"finite large number", "flow.set_variable", "value", `1e300`},
		{"adapter checks deferred", "android.input_text", "text", `"Tiếng Việt %s"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			node := inputNode(tc.kind, tc.field, literal(tc.value))
			for _, workflow := range []bool{false, true} {
				values := ir.Object(nil)
				if workflow {
					node.Inputs[tc.field] = json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"value"}`)
					values = ir.Object{"value": json.RawMessage(" \t" + tc.value + "\n ")}
				}
				got, err := r.Resolve(context.Background(), node, tc.field, bindings.Values{WorkflowInputs: values})
				want := tc.value
				if workflow {
					want = " \t" + want + "\n "
				}
				if err != nil || string(got) != want {
					t.Fatalf("workflow=%v: %s (%v), want %s", workflow, got, err, want)
				}
			}
		})
	}
	node := inputNode("flow.delay", "durationMs", json.RawMessage(`{"source":"LITERAL","value":1,"value":10.0}`))
	got, err := r.Resolve(context.Background(), node, "durationMs", bindings.Values{})
	if err != nil || string(got) != "10.0" {
		t.Fatalf("duplicate property behavior: %s, %v", got, err)
	}
}

func TestResolveFailures(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for _, tc := range []struct {
		name, kind, field, source, reason string
		inputs                            ir.Object
	}{
		{"unknown node", "future.node", "text", string(literal(`"hello"`)), "input_definition_unavailable", nil},
		{"unknown field", "android.input_text", "private-field-secret", string(literal(`"hello"`)), "input_definition_unavailable", nil},
		{"case sensitive field", "android.input_text", "Text", string(literal(`"hello"`)), "input_definition_unavailable", nil},
		{"empty source", "android.input_text", "text", "", "binding_invalid", nil},
		{"null source", "android.input_text", "text", "null", "binding_invalid", nil},
		{"array source", "android.input_text", "text", "[]", "binding_invalid", nil},
		{"trailing JSON", "android.input_text", "text", `{} {}`, "binding_invalid", nil},
		{"invalid UTF8 source", "android.input_text", "text", string([]byte{0xff}), "binding_invalid", nil},
		{"editor graph tag", "android.input_text", "text", `{"kind":"LITERAL","value":"hello"}`, "binding_invalid", nil},
		{"null tag", "android.input_text", "text", `{"source":null,"value":"hello"}`, "binding_invalid", nil},
		{"wrong tag type", "android.input_text", "text", `{"source":1,"value":"hello"}`, "binding_invalid", nil},
		{"unknown source kind", "android.input_text", "text", `{"source":"FUTURE","value":"hello"}`, "source_unsupported", nil},
		{"missing literal value", "android.input_text", "text", `{"source":"LITERAL","extra":"secret"}`, "binding_invalid", nil},
		{"extra source key", "android.input_text", "text", `{"source":"LITERAL","value":"hello","extra":1}`, "binding_invalid", nil},
		{"null literal", "android.input_text", "text", string(literal(`null`)), "input_value_invalid", nil},
		{"object literal", "android.input_text", "text", string(literal(`{"value":"secret"}`)), "input_value_invalid", nil},
		{"array literal", "android.input_text", "text", string(literal(`[]`)), "input_value_invalid", nil},
		{"string integer", "flow.delay", "durationMs", string(literal(`"10"`)), "input_value_invalid", nil},
		{"fractional integer", "flow.delay", "durationMs", string(literal(`1.5`)), "input_value_invalid", nil},
		{"unsafe integer", "flow.delay", "durationMs", string(literal(`9007199254740992`)), "input_value_invalid", nil},
		{"number overflow", "flow.set_variable", "value", string(literal(`1e400`)), "input_value_invalid", nil},
		{"below bound", "flow.delay", "durationMs", string(literal(`-1`)), "input_value_invalid", nil},
		{"above bound", "flow.delay", "durationMs", string(literal(`300001`)), "input_value_invalid", nil},
		{"invalid package pattern", "android.launch_app", "packageName", string(literal(`"bad;command"`)), "input_value_invalid", nil},
		{"missing input name", "android.input_text", "text", `{"source":"WORKFLOW_INPUT"}`, "binding_invalid", nil},
		{"invalid input name", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":" bad "}`, "binding_invalid", nil},
		{"null input name", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":null}`, "binding_invalid", nil},
		{"name is case sensitive", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":"message"}`, "workflow_input_missing", ir.Object{"Message": json.RawMessage(`"hello"`)}},
		{"missing workflow input", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":"privateInputSecret"}`, "workflow_input_missing", nil},
		{"nil workflow value", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":"value"}`, "input_value_invalid", ir.Object{"value": nil}},
		{"empty workflow value", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":"value"}`, "input_value_invalid", ir.Object{"value": json.RawMessage{}}},
		{"null workflow value", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":"value"}`, "input_value_invalid", ir.Object{"value": json.RawMessage(`null`)}},
		{"invalid UTF8 value", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":"value"}`, "input_value_invalid", ir.Object{"value": {0xff}}},
		{"wrong workflow value type", "android.input_text", "text", `{"source":"WORKFLOW_INPUT","inputName":"value"}`, "input_value_invalid", ir.Object{"value": json.RawMessage(`123`)}},
		{"node output unavailable", "android.input_text", "text", `{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId"}`, "node_output_unavailable", nil},
		{"flow variable ownership override", "android.input_text", "text", `{"source":"FLOW_VARIABLE","name":"value","scope":"FLOW"}`, "binding_invalid", nil},
		{"global variable invalid name", "android.input_text", "text", `{"source":"GLOBAL_VARIABLE","name":" bad "}`, "binding_invalid", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			node := inputNode(tc.kind, tc.field, json.RawMessage(tc.source))
			got, err := r.Resolve(context.Background(), node, tc.field, bindings.Values{WorkflowInputs: tc.inputs})
			if got != nil {
				t.Fatalf("failure returned a value: %s", got)
			}
			field := tc.field
			if tc.reason == "input_definition_unavailable" {
				field = ""
			}
			failure(t, err, field, tc.reason, "resolve_input_binding")
		})
	}
	node := inputNode("android.input_text", "text", nil)
	delete(node.Inputs, "text")
	got, err := r.Resolve(context.Background(), node, "text", bindings.Values{})
	if got != nil {
		t.Fatal("missing binding returned a value")
	}
	failure(t, err, "text", "binding_missing", "resolve_input_binding")
}

func TestResolveSelectedModeAndField(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	node := inputNode("android.tap", "x", literal("10"))
	node.Config["targetMode"] = json.RawMessage(`"ABSOLUTE_PIXELS"`)
	got, err := r.Resolve(context.Background(), node, "x", bindings.Values{})
	if err != nil || string(got) != "10" {
		t.Fatalf("pixel literal: %s %v", got, err)
	}
	node.Inputs["x"] = json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"x"}`)
	_, err = r.Resolve(context.Background(), node, "x", bindings.Values{WorkflowInputs: ir.Object{"x": json.RawMessage("10")}})
	failure(t, err, "x", "source_unsupported", "resolve_input_binding")
	_, err = r.Resolve(context.Background(), node, "xPercent", bindings.Values{})
	failure(t, err, "", "input_definition_unavailable", "resolve_input_binding")

	// UUID keys use the destination contract, not the workflow-field-name pattern.
	const id = "019aaa00-1111-4111-8111-111111111111"
	node = inputNode("flow.switch", id, literal("2.5"))
	node.Config["rules"] = json.RawMessage(`[{"id":"` + id + `","operator":"EQ"}]`)
	node.Inputs["value"] = json.RawMessage(`{"source":"FLOW_VARIABLE","name":"notResolvedHere"}`)
	got, err = r.Resolve(context.Background(), node, id, bindings.Values{})
	if err != nil || string(got) != "2.5" {
		t.Fatalf("requested field was not isolated: %s %v", got, err)
	}
}

func TestResolveStringBoundary(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for _, size := range []int{10000, 10001} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			value, err := json.Marshal(strings.Repeat("a", size))
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Resolve(context.Background(), inputNode("android.input_text", "text", literal(string(value))), "text", bindings.Values{})
			if size == 10000 {
				if err != nil || string(got) != string(value) {
					t.Fatalf("boundary rejected: %v", err)
				}
			} else {
				if got != nil {
					t.Fatal("oversized value returned")
				}
				failure(t, err, "text", "input_value_invalid", "resolve_input_binding")
			}
		})
	}
}

func TestResolveInputs(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	node := inputNode("android.input_text", "text", json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"message"}`))
	for _, message := range []string{`"hello"`, `"other device"`, `""`} {
		t.Run(message, func(t *testing.T) {
			t.Parallel()
			got, err := r.ResolveInputs(context.Background(), node, bindings.Values{WorkflowInputs: ir.Object{"message": json.RawMessage(message), "unreferenced": nil}})
			if err != nil || len(got) != 1 || string(got["text"]) != message {
				t.Fatalf("handler inputs: %v, %v", got, err)
			}
		})
	}
	for _, kind := range []string{"flow.start", "flow.end"} {
		t.Run(kind, func(t *testing.T) {
			got, err := r.ResolveInputs(context.Background(), ir.Node{Type: kind}, bindings.Values{})
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("empty inputs: %v, %v", got, err)
			}
			got["mutated"] = json.RawMessage("true")
			next, err := r.ResolveInputs(context.Background(), ir.Node{Type: kind}, bindings.Values{})
			if err != nil || len(next) != 0 {
				t.Fatal("empty results share state")
			}
		})
	}
	got, err := r.ResolveInputs(context.Background(), ir.Node{Type: "private.unknown"}, bindings.Values{})
	if got != nil {
		t.Fatal("unknown node returned inputs")
	}
	failure(t, err, "", "input_definition_unavailable", "resolve_node_inputs")
}

func TestResolveInputsFailureOrder(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for _, order := range [][]string{{"x", "y"}, {"y", "x"}} {
		node := ir.Node{Type: "android.tap", Config: ir.Object{"targetMode": json.RawMessage(`"ABSOLUTE_PIXELS"`)}, Inputs: ir.Object{}}
		for _, field := range order {
			node.Inputs[field] = literal(`"invalid"`)
		}
		got, err := r.ResolveInputs(context.Background(), node, bindings.Values{})
		if got != nil {
			t.Fatal("failed bulk resolution returned inputs")
		}
		failure(t, err, "x", "input_value_invalid", "resolve_input_binding")
		node.Inputs["x"] = literal("1")
		got, err = r.ResolveInputs(context.Background(), node, bindings.Values{})
		if got != nil {
			t.Fatal("failed bulk resolution leaked a partial map")
		}
		failure(t, err, "y", "input_value_invalid", "resolve_input_binding")
	}
}

func TestResolvedValuesAreOwned(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	const id = "019aaa00-1111-4111-8111-111111111111"
	source := json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"message"}`)
	node := ir.Node{Type: "flow.switch", Config: ir.Object{"rules": json.RawMessage(`[{"id":"` + id + `","operator":"EQ"}]`)}, Inputs: ir.Object{"value": source, id: source}}
	value := json.RawMessage(`"hello"`)
	inputs := ir.Object{"message": value}
	got, err := r.ResolveInputs(context.Background(), node, bindings.Values{WorkflowInputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	got["value"][1] = 'H'
	if string(value) != `"hello"` || string(got[id]) != `"hello"` {
		t.Fatal("result aliases source or another result")
	}
	value[1] = 'X'
	source[0] = '['
	inputs["message"] = json.RawMessage(`"changed"`)
	node.Inputs["value"] = literal("1")
	if string(got["value"]) != `"Hello"` || string(got[id]) != `"hello"` {
		t.Fatal("caller mutation changed returned inputs")
	}
	got["added"] = json.RawMessage("true")
	if _, present := node.Inputs["added"]; present {
		t.Fatal("result map aliases the node")
	}
	bytes := literal(`"literal"`)
	literalNode := inputNode("android.input_text", "text", bytes)
	resolved, err := r.Resolve(context.Background(), literalNode, "text", bindings.Values{})
	if err != nil {
		t.Fatal(err)
	}
	bytes[0] = '['
	if string(resolved) != `"literal"` {
		t.Fatal("literal result aliases source bytes")
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	node := ir.Node{Type: "android.tap", Config: ir.Object{"targetMode": json.RawMessage(`"ABSOLUTE_PIXELS"`)}, Inputs: ir.Object{"x": literal("1"), "y": literal("2")}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := r.Resolve(ctx, node, "x", bindings.Values{})
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolution: %s %v", got, err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	all, err := r.ResolveInputs(deadline, node, bindings.Values{})
	if all != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired resolution: %v %v", all, err)
	}
	for _, checks := range []int32{3, 4, 6} {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &cancelDuringChecks{Context: parent, cancel: cancel, at: checks}
		all, err := r.ResolveInputs(ctx, node, bindings.Values{})
		cancel()
		if all != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation after check %d: %v %v", checks, all, err)
		}
		var command xerror.CommandError
		if errors.As(err, &command) {
			t.Fatal("cancellation became a node input error")
		}
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = &cancelDuringChecks{Context: parent, cancel: cancel, at: 2}
	got, err = r.Resolve(ctx, node, "unknown", bindings.Values{})
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not take precedence over failure: %s %v", got, err)
	}
}

func TestResolveNodeOutput(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for _, tc := range []struct{ name, value string }{
		{"string", `"capture-a"`},
		{"integer", `10`},
		{"numeric representation", `10.0`},
		{"exponent", `1e2`},
		{"false is present", `false`},
		{"zero is present", `0`},
		{"empty string is present", `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Generic lookup fixtures exercise presence without adding producer schemas.
			node := inputNode("flow.set_variable", "value", json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"producer","field":"result"}`))
			values := bindings.Values{CommittedOutputs: map[string]ir.Object{"producer": {"result": json.RawMessage(tc.value)}}}
			got, err := r.Resolve(context.Background(), node, "value", values)
			if err != nil || string(got) != tc.value {
				t.Fatalf("got %s (%v), want %s", got, err, tc.value)
			}
		})
	}
}

func TestResolveNodeOutputFailures(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	const source = `{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId"}`
	for _, tc := range []struct {
		name   string
		view   map[string]ir.Object
		reason string
	}{
		{"missing view", nil, "node_output_unavailable"},
		{"empty view", map[string]ir.Object{}, "node_output_unavailable"},
		{"different producer", map[string]ir.Object{"other": {"artifactId": json.RawMessage(`"private"`)}}, "node_output_unavailable"},
		{"producer case", map[string]ir.Object{"Capture": {"artifactId": json.RawMessage(`"private"`)}}, "node_output_unavailable"},
		{"nil producer", map[string]ir.Object{"capture": nil}, "node_output_unavailable"},
		{"empty producer", map[string]ir.Object{"capture": {}}, "node_output_field_missing"},
		{"field case", map[string]ir.Object{"capture": {"artifactID": json.RawMessage(`"private"`)}}, "node_output_field_missing"},
		{"nil value", map[string]ir.Object{"capture": {"artifactId": nil}}, "input_value_invalid"},
		{"empty bytes", map[string]ir.Object{"capture": {"artifactId": json.RawMessage{}}}, "input_value_invalid"},
		{"null", map[string]ir.Object{"capture": {"artifactId": json.RawMessage(`null`)}}, "input_value_invalid"},
		{"invalid UTF8", map[string]ir.Object{"capture": {"artifactId": {0xff}}}, "input_value_invalid"},
		{"object", map[string]ir.Object{"capture": {"artifactId": json.RawMessage(`{"secret":"private"}`)}}, "input_value_invalid"},
		{"array", map[string]ir.Object{"capture": {"artifactId": json.RawMessage(`[]`)}}, "input_value_invalid"},
		{"wrong type", map[string]ir.Object{"capture": {"artifactId": json.RawMessage(`123`)}}, "input_value_invalid"},
		{"malformed scalar", map[string]ir.Object{"capture": {"artifactId": json.RawMessage(`"private" "extra"`)}}, "input_value_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			node := inputNode("android.input_text", "text", json.RawMessage(source))
			values := bindings.Values{
				WorkflowInputs:   ir.Object{"artifactId": json.RawMessage(`"must-not-be-used"`)},
				CommittedOutputs: tc.view,
			}
			got, err := r.Resolve(context.Background(), node, "text", values)
			if got != nil {
				t.Fatal("failed output lookup returned a value")
			}
			failure(t, err, "text", tc.reason, "resolve_input_binding")
		})
	}
	for _, source := range []string{
		`{"source":"NODE_OUTPUT","field":"artifactId"}`,
		`{"source":"NODE_OUTPUT","nodeId":"capture"}`,
		`{"source":"NODE_OUTPUT","nodeId":null,"field":"artifactId"}`,
		`{"source":"NODE_OUTPUT","nodeId":1,"field":"artifactId"}`,
		`{"source":"NODE_OUTPUT","nodeId":" capture ","field":"artifactId"}`,
		`{"source":"NODE_OUTPUT","nodeId":"capture","field":null}`,
		`{"source":"NODE_OUTPUT","nodeId":"capture","field":1}`,
		`{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifact.id"}`,
		`{"source":"NODE_OUTPUT","nodeId":"capture","field":" artifactId "}`,
		`{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId","value":"private"}`,
		`{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId"} {}`,
	} {
		t.Run(source, func(t *testing.T) {
			node := inputNode("android.input_text", "text", json.RawMessage(source))
			got, err := r.Resolve(context.Background(), node, "text", bindings.Values{})
			if got != nil {
				t.Fatal("invalid source returned a value")
			}
			failure(t, err, "text", "binding_invalid", "resolve_input_binding")
		})
	}
	node := inputNode("android.tap", "x", json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId"}`))
	node.Config["targetMode"] = json.RawMessage(`"ABSOLUTE_PIXELS"`)
	got, err := r.Resolve(context.Background(), node, "x", bindings.Values{})
	if got != nil {
		t.Fatal("forbidden output binding returned a value")
	}
	failure(t, err, "x", "source_unsupported", "resolve_input_binding")
}

func TestResolveG3CheckedOutputView(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	guard, err := outputs.New(definitions)
	if err != nil {
		t.Fatal(err)
	}
	for _, duration := range []string{"10.0", "300001"} {
		t.Run(duration, func(t *testing.T) {
			snapshot, err := guard.Validate(context.Background(), "flow.delay", map[string]any{"durationMs": json.Number(duration)})
			if err != nil {
				t.Fatal(err)
			}
			var decoded ir.Object
			if err := json.Unmarshal(snapshot, &decoded); err != nil {
				t.Fatal(err)
			}
			// Commit eligibility is a G9 prerequisite; this test supplies the view directly.
			values := bindings.Values{CommittedOutputs: map[string]ir.Object{"wait": decoded}}
			node := inputNode("flow.delay", "durationMs", json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"wait","field":"durationMs"}`))
			got, err := r.Resolve(context.Background(), node, "durationMs", values)
			if duration == "300001" {
				if got != nil {
					t.Fatal("consumer received an out-of-bound value")
				}
				failure(t, err, "durationMs", "input_value_invalid", "resolve_input_binding")
			} else if err != nil || string(got) != duration {
				t.Fatalf("G3 snapshot lookup: %s %v", got, err)
			}
		})
	}
}

func TestResolveMixedSourcesAndOutputOwnership(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	const first = "019aaa00-1111-4111-8111-111111111111"
	const second = "019aaa00-2222-4222-8222-222222222222"
	node := ir.Node{Type: "flow.switch", Config: ir.Object{"rules": json.RawMessage(`[{"id":"` + first + `","operator":"EQ"},{"id":"` + second + `","operator":"EQ"}]`)}, Inputs: ir.Object{
		"value": literal(`"base"`),
		first:   json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"message"}`),
		second:  json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId"}`),
	}}
	value := json.RawMessage(`"hello"`)
	values := bindings.Values{WorkflowInputs: ir.Object{"message": json.RawMessage(`"workflow"`)}, CommittedOutputs: map[string]ir.Object{"capture": {"artifactId": value}}}
	got, err := r.ResolveInputs(context.Background(), node, values)
	if err != nil || len(got) != 3 || string(got["value"]) != `"base"` || string(got[first]) != `"workflow"` || string(got[second]) != `"hello"` {
		t.Fatalf("mixed sources: %v %v", got, err)
	}
	node.Inputs[first] = node.Inputs[second]
	got, err = r.ResolveInputs(context.Background(), node, values)
	if err != nil {
		t.Fatal(err)
	}
	got[first][1] = 'H'
	if string(value) != `"hello"` || string(got[second]) != `"hello"` {
		t.Fatal("resolved outputs share mutable bytes")
	}
	value[1] = 'X'
	delete(values.CommittedOutputs, "capture")
	if string(got[first]) != `"Hello"` || string(got[second]) != `"hello"` {
		t.Fatal("view mutation affected resolved outputs")
	}
	got, err = r.ResolveInputs(context.Background(), node, values)
	if got != nil {
		t.Fatal("partial inputs escaped after missing producer")
	}
	failure(t, err, first, "node_output_unavailable", "resolve_input_binding")

	values.CommittedOutputs["capture"] = ir.Object{"artifactId": json.RawMessage(`"new"`)}
	node.Inputs[first] = literal(`"first field succeeds"`)
	delete(values.CommittedOutputs["capture"], "artifactId")
	got, err = r.ResolveInputs(context.Background(), node, values)
	if got != nil {
		t.Fatal("earlier success escaped in a partial bulk map")
	}
	failure(t, err, second, "node_output_field_missing", "resolve_input_binding")
}

func TestNodeOutputExecutionIsolation(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	node := inputNode("android.input_text", "text", json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId"}`))
	for _, value := range []string{`"device-a"`, `"device-b"`} {
		got, err := r.Resolve(context.Background(), node, "text", bindings.Values{CommittedOutputs: map[string]ir.Object{"capture": {"artifactId": json.RawMessage(value)}}})
		if err != nil || string(got) != value {
			t.Fatalf("execution isolation: %s %v", got, err)
		}
	}
	got, err := r.Resolve(context.Background(), node, "text", bindings.Values{})
	if got != nil {
		t.Fatal("fresh execution reused an old view")
	}
	failure(t, err, "text", "node_output_unavailable", "resolve_input_binding")
	for i := 0; i < 32; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			value := json.RawMessage(fmt.Sprintf(`"device%d"`, i))
			got, err := r.Resolve(context.Background(), node, "text", bindings.Values{CommittedOutputs: map[string]ir.Object{"capture": {"artifactId": value}}})
			if err != nil || string(got) != string(value) {
				t.Fatalf("concurrent isolation: %s %v", got, err)
			}
		})
	}
}

func TestNodeOutputCancellation(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	node := inputNode("android.input_text", "text", json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"capture","field":"artifactId"}`))
	values := bindings.Values{CommittedOutputs: map[string]ir.Object{"capture": {"artifactId": json.RawMessage(`"hello"`)}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := r.Resolve(ctx, node, "text", values)
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancellation: %s %v", got, err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	got, err = r.Resolve(deadline, node, "text", values)
	if got != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %s %v", got, err)
	}
	for _, view := range []bindings.Values{values, {}} {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &cancelDuringChecks{Context: parent, cancel: cancel, at: 2}
		got, err := r.Resolve(ctx, node, "text", view)
		cancel()
		var command xerror.CommandError
		if got != nil || !errors.Is(err, context.Canceled) || errors.As(err, &command) {
			t.Fatalf("mid-resolution cancellation: %s %v", got, err)
		}
	}
}

// Trigger real cancellation at a deterministic checkpoint rather than sleeping.
type cancelDuringChecks struct {
	context.Context
	cancel context.CancelFunc
	at     int32
	checks atomic.Int32
}

func (c *cancelDuringChecks) Err() error {
	if c.checks.Add(1) == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func ExampleResolver_ResolveInputs() {
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		fmt.Println(err)
		return
	}
	r, err := bindings.New(definitions)
	if err != nil {
		fmt.Println(err)
		return
	}
	node := inputNode("android.input_text", "text", json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"message"}`))
	inputs, err := r.ResolveInputs(context.Background(), node, bindings.Values{WorkflowInputs: ir.Object{"message": json.RawMessage(`"hello"`)}})
	if err != nil {
		fmt.Println(err)
		return
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(encoded))
	// Output: {"text":"hello"}
}
