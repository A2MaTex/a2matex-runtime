package outputs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func validator(t *testing.T) *outputs.Validator {
	t.Helper()
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	v, err := outputs.New(definitions)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func assertFailure(t *testing.T, data json.RawMessage, err error, code, reason string) {
	t.Helper()
	var command xerror.CommandError
	var operation *xerror.OperationError
	if data != nil || !errors.As(err, &command) || command.Code != code || command.Retryable ||
		!errors.As(err, &operation) || operation.Operation != "validate_node_output" || operation.Detail != reason {
		t.Fatalf("unexpected output failure: %s, %v (%+v, %+v)", data, err, command, operation)
	}
	messages := map[string]string{
		"output_definition_unavailable": "Node output definition is unavailable",
		"output_missing":                "Node output must be a JSON object",
		"output_not_serializable":       "Node output cannot be encoded as JSON",
		"output_too_large":              "Node output exceeds the execution event limit",
		"output_contract_invalid":       "Node output does not satisfy its installed contract",
	}
	if command.Message != messages[reason] || err.Error() != "validate_node_output: "+messages[reason] {
		t.Fatalf("unsafe or incorrect error message: %v", err)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	v, err := outputs.New(nil)
	if v != nil || !errors.Is(err, xerror.ErrOutputValidationDependencies) {
		t.Fatalf("missing definitions: %v, %v", v, err)
	}
	var operation *xerror.OperationError
	if !errors.As(err, &operation) || operation.Operation != "configure_output_validation" {
		t.Fatalf("missing constructor context: %v", err)
	}
}

func TestValidateAcceptedOutputs(t *testing.T) {
	t.Parallel()
	v := validator(t)
	for _, tc := range []struct {
		name, kind string
		output     map[string]any
		want       string
	}{
		{"zero duration", "flow.delay", map[string]any{"durationMs": 0}, `{"durationMs":0}`},
		{"positive duration", "flow.delay", map[string]any{"durationMs": 10}, `{"durationMs":10}`},
		{"integral float", "flow.delay", map[string]any{"durationMs": float64(10)}, `{"durationMs":10}`},
		{"JSON number", "flow.delay", map[string]any{"durationMs": json.Number("10.0")}, `{"durationMs":10.0}`},
		{"no input upper bound", "flow.delay", map[string]any{"durationMs": 300001}, `{"durationMs":300001}`},
		{"safe integer maximum", "flow.delay", map[string]any{"durationMs": uint64(9007199254740991)}, `{"durationMs":9007199254740991}`},
		{"screenshot ID", "android.screenshot", map[string]any{"artifactId": "019aaa00-1111-4111-8111-111111111111"}, `{"artifactId":"019aaa00-1111-4111-8111-111111111111"}`},
		{"no extra UUID validation", "android.screenshot", map[string]any{"artifactId": "capture"}, `{"artifactId":"capture"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := v.Validate(context.Background(), tc.kind, tc.output)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s (%v), want %s", got, err, tc.want)
			}
		})
	}
	for _, kind := range []string{"flow.start", "flow.end", "flow.switch", "flow.set_variable", "flow.wait_variable", "utility.log", "android.tap", "android.swipe", "android.long_press", "android.input_text", "android.launch_app", "android.press_back", "android.press_home"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			got, err := v.Validate(context.Background(), kind, map[string]any{})
			if err != nil || string(got) != "{}" {
				t.Fatalf("empty output rejected: %s %v", got, err)
			}
		})
	}
}

func TestValidateRejectedOutputs(t *testing.T) {
	t.Parallel()
	v := validator(t)
	for _, tc := range []struct {
		name, kind string
		output     map[string]any
		reason     string
	}{
		{"nil empty-node output", "flow.start", nil, "output_missing"},
		{"nil data-node output", "flow.delay", nil, "output_missing"},
		{"extra stdout", "flow.start", map[string]any{"stdout": "private-output"}, "output_contract_invalid"},
		{"extra status", "android.tap", map[string]any{"status": "OK"}, "output_contract_invalid"},
		{"missing duration", "flow.delay", map[string]any{}, "output_contract_invalid"},
		{"negative duration", "flow.delay", map[string]any{"durationMs": -1}, "output_contract_invalid"},
		{"fractional duration", "flow.delay", map[string]any{"durationMs": 1.5}, "output_contract_invalid"},
		{"unsafe integer", "flow.delay", map[string]any{"durationMs": uint64(9007199254740992)}, "output_contract_invalid"},
		{"string duration", "flow.delay", map[string]any{"durationMs": "10"}, "output_contract_invalid"},
		{"boolean duration", "flow.delay", map[string]any{"durationMs": false}, "output_contract_invalid"},
		{"null duration", "flow.delay", map[string]any{"durationMs": nil}, "output_contract_invalid"},
		{"number overflow", "flow.delay", map[string]any{"durationMs": json.Number("1e400")}, "output_contract_invalid"},
		{"missing ID", "android.screenshot", map[string]any{}, "output_contract_invalid"},
		{"empty ID", "android.screenshot", map[string]any{"artifactId": ""}, "output_contract_invalid"},
		{"wrong ID type", "android.screenshot", map[string]any{"artifactId": 1}, "output_contract_invalid"},
		{"null ID", "android.screenshot", map[string]any{"artifactId": nil}, "output_contract_invalid"},
		{"nested ID", "android.screenshot", map[string]any{"artifactId": map[string]any{"secret": "private"}}, "output_contract_invalid"},
		{"array ID", "android.screenshot", map[string]any{"artifactId": []string{"private"}}, "output_contract_invalid"},
		{"extra artifact path", "android.screenshot", map[string]any{"artifactId": "capture", "path": "/private/file"}, "output_contract_invalid"},
		{"NaN", "flow.delay", map[string]any{"durationMs": math.NaN()}, "output_not_serializable"},
		{"infinity", "flow.delay", map[string]any{"durationMs": math.Inf(1)}, "output_not_serializable"},
		{"channel", "flow.delay", map[string]any{"durationMs": make(chan int)}, "output_not_serializable"},
		{"function", "flow.delay", map[string]any{"durationMs": func() {}}, "output_not_serializable"},
		{"bad raw JSON", "android.screenshot", map[string]any{"artifactId": json.RawMessage(`{"secret":"private"`)}, "output_not_serializable"},
		{"failing marshaler", "android.screenshot", map[string]any{"artifactId": marshalFunc(func() ([]byte, error) { return nil, errors.New("private-value /private/path") })}, "output_not_serializable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := v.Validate(context.Background(), tc.kind, tc.output)
			assertFailure(t, got, err, constants.NodeOutputInvalid, tc.reason)
		})
	}
	cycle := map[string]any{}
	cycle["loop"] = cycle
	got, err := v.Validate(context.Background(), "android.screenshot", map[string]any{"artifactId": cycle})
	assertFailure(t, got, err, constants.NodeOutputInvalid, "output_not_serializable")
	got, err = v.Validate(context.Background(), "private.unknown", map[string]any{})
	assertFailure(t, got, err, constants.InternalError, "output_definition_unavailable")
}

func TestValidateEventCeiling(t *testing.T) {
	t.Parallel()
	v := validator(t)
	const emptyObject = `{"artifactId":""}`
	for _, size := range []int{constants.MaxEventBytes - 1, constants.MaxEventBytes, constants.MaxEventBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			output := map[string]any{"artifactId": strings.Repeat("a", size-len(emptyObject))}
			got, err := v.Validate(context.Background(), "android.screenshot", output)
			if size > constants.MaxEventBytes {
				assertFailure(t, got, err, constants.NodeOutputInvalid, "output_too_large")
			} else if err != nil || len(got) != size {
				t.Fatalf("standalone ceiling: %d bytes, %v", len(got), err)
			}
		})
	}
}

func TestValidateOwnedSnapshot(t *testing.T) {
	t.Parallel()
	v := validator(t)
	value := json.RawMessage(`"capture"`)
	output := map[string]any{"artifactId": value}
	first, err := v.Validate(context.Background(), "android.screenshot", output)
	if err != nil {
		t.Fatal(err)
	}
	second, err := v.Validate(context.Background(), "android.screenshot", output)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = '['
	if string(value) != `"capture"` || string(second) != `{"artifactId":"capture"}` {
		t.Fatal("result aliases source or another result")
	}
	value[1] = 'X'
	output["artifactId"] = "changed"
	if string(second) != `{"artifactId":"capture"}` {
		t.Fatal("source mutation changed accepted snapshot")
	}

	var calls atomic.Int32
	output = map[string]any{"artifactId": marshalFunc(func() ([]byte, error) {
		return []byte(fmt.Sprintf(`"capture%d"`, calls.Add(1))), nil
	})}
	snapshot, err := v.Validate(context.Background(), "android.screenshot", output)
	if err != nil || calls.Load() != 1 || string(snapshot) != `{"artifactId":"capture1"}` {
		t.Fatalf("snapshot was not serialized once: %s %v (%d calls)", snapshot, err, calls.Load())
	}
}

func TestValidateConcurrentCalls(t *testing.T) {
	t.Parallel()
	v := validator(t)
	for i := 0; i < 32; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			got, err := v.Validate(context.Background(), "android.screenshot", map[string]any{"artifactId": fmt.Sprintf("capture%d", i)})
			want := fmt.Sprintf(`{"artifactId":"capture%d"}`, i)
			if err != nil || string(got) != want {
				t.Fatalf("shared state: %s, %v", got, err)
			}
		})
	}
}

func TestValidateCancellation(t *testing.T) {
	t.Parallel()
	v := validator(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := v.Validate(ctx, "flow.start", map[string]any{})
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancellation: %s %v", got, err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	got, err = v.Validate(deadline, "flow.start", map[string]any{})
	if got != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %s %v", got, err)
	}

	for _, check := range []int32{2, 3} {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &cancelDuringChecks{Context: parent, cancel: cancel, at: check}
		got, err := v.Validate(ctx, "flow.start", map[string]any{})
		cancel()
		if got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation during check %d: %s %v", check, got, err)
		}
	}
	for _, failEncoding := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		value := marshalFunc(func() ([]byte, error) {
			cancel()
			if failEncoding {
				return nil, errors.New("private")
			}
			return []byte(`"capture"`), nil
		})
		got, err := v.Validate(ctx, "android.screenshot", map[string]any{"artifactId": value})
		cancel()
		var command xerror.CommandError
		if got != nil || !errors.Is(err, context.Canceled) || errors.As(err, &command) {
			t.Fatalf("cancellation during encoding: %s %v", got, err)
		}
	}
	for _, kind := range []string{"future.node", "flow.start"} {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &cancelDuringChecks{Context: parent, cancel: cancel, at: 2}
		got, err := v.Validate(ctx, kind, nil)
		cancel()
		if got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation did not override local error: %s %v", got, err)
		}
	}
}

type marshalFunc func() ([]byte, error)

func (f marshalFunc) MarshalJSON() ([]byte, error) { return f() }

// Trigger real cancellation at a deterministic stage without timing-dependent sleeps.
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

func TestCurrentFlowHandlerOutputs(t *testing.T) {
	t.Parallel()
	v := validator(t)
	for _, tc := range []struct {
		kind string
		run  registry.Handler
		call registry.Invocation
		want string
	}{
		{"flow.start", start.Invoke, registry.Invocation{}, `{}`},
		{"flow.end", end.Invoke, registry.Invocation{}, `{}`},
		{"flow.delay", delay.Invoke, registry.Invocation{Inputs: map[string]json.RawMessage{"durationMs": json.RawMessage(`0`)}}, `{"durationMs":0}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			output, err := tc.run(ctx, tc.call)
			if err != nil {
				t.Fatal(err)
			}
			got, err := v.Validate(ctx, tc.kind, output)
			if err != nil || string(got) != tc.want {
				t.Fatalf("real handler output: %s %v", got, err)
			}
		})
	}
}
