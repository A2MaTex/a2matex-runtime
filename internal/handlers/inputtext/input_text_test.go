package inputtext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type dispatchFunc func(context.Context, string, adb.DeviceCommand) error

func (f dispatchFunc) Dispatch(ctx context.Context, serial string, command adb.DeviceCommand) error {
	return f(ctx, serial, command)
}

func TestInputTextRegisteredHandler(t *testing.T) {
	const serial = "emulator-5554"
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var dispatched []adb.DeviceCommand
	handler := NewInputText(dispatchFunc(func(gotCtx context.Context, gotSerial string, command adb.DeviceCommand) error {
		if gotCtx != ctx || gotSerial != serial {
			t.Fatal("dispatch did not preserve its context and serial")
		}
		dispatched = append(dispatched, command)
		return nil
	}))
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(catalog, []registry.Registration{
		{Type: "android.input_text", RuntimeHandler: "android.input_text", Run: handler.Invoke},
	})
	if err != nil {
		t.Fatal(err)
	}
	invoke, ok := reg.Lookup("android.input_text", "android.input_text")
	if !ok {
		t.Fatal("registered Input Text handler is missing")
	}

	const text = "  A2MaTeX 'demo' & $5  "
	longText := strings.Repeat("x", constants.MaxAndroidInputTextLength)
	for _, value := range []string{"", text, longText} {
		binding, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		output, err := invoke(ctx, registry.Invocation{ADBSerial: serial, Inputs: map[string]json.RawMessage{"text": binding}})
		if err != nil || output == nil || len(output) != 0 {
			t.Fatalf("output = %#v, error = %v, want empty output", output, err)
		}
	}
	if len(dispatched) != 2 || dispatched[0] != (adb.InputTextCommand{Text: text}) || dispatched[1] != (adb.InputTextCommand{Text: longText}) {
		t.Fatal("expected exactly one typed dispatch for each nonempty binding")
	}
}

func TestInputTextRejectsUnsupportedInputBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	handler := NewInputText(dispatchFunc(func(context.Context, string, adb.DeviceCommand) error {
		t.Fatal("invalid input reached ADB")
		return nil
	}))
	for _, tc := range []struct {
		name, inputs, operation, reason string
		config                          map[string]json.RawMessage
	}{
		{name: "missing", inputs: `{}`, operation: "validate_inputs", reason: "unexpected_input_fields"},
		{name: "null", inputs: `{"text":null}`, operation: "validate_inputs", reason: "invalid_text_type"},
		{name: "number", inputs: `{"text":3}`, operation: "validate_inputs", reason: "invalid_text_type"},
		{name: "extra field", inputs: `{"text":"hello","clear":true}`, operation: "validate_inputs", reason: "unexpected_input_fields"},
		{name: "extra config", inputs: `{"text":"hello"}`, operation: "validate_inputs", reason: "unexpected_config_fields", config: map[string]json.RawMessage{"clear": json.RawMessage(`true`)}},
		{name: "literal percent sequence", inputs: `{"text":"prefix-%s"}`, operation: "validate_input_text", reason: "unsupported_percent_sequence"},
		{name: "Unicode", inputs: `{"text":"café"}`, operation: "validate_input_text", reason: "unsupported_character"},
		{name: "line break", inputs: `{"text":"one\ntwo"}`, operation: "validate_input_text", reason: "unsupported_character"},
		{name: "NUL", inputs: `{"text":"\u0000"}`, operation: "validate_input_text", reason: "unsupported_character"},
		{name: "oversized", inputs: `{"text":"` + strings.Repeat("x", constants.MaxAndroidInputTextLength+1) + `"}`, operation: "validate_input_text", reason: "text_too_long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := registry.Invocation{ADBSerial: "emulator-5554", Config: tc.config}
			if err := json.Unmarshal([]byte(tc.inputs), &call.Inputs); err != nil {
				t.Fatal(err)
			}
			output, err := handler.Invoke(ctx, call)
			var commandErr xerror.CommandError
			var operation *xerror.OperationError
			if output != nil || !errors.As(err, &commandErr) || commandErr.Code != constants.CommandInputUnsupported || commandErr.Retryable ||
				!errors.As(err, &operation) || operation.Operation != tc.operation || operation.Detail != tc.reason {
				t.Fatalf("output = %v, error = %v", output, err)
			}
		})
	}
}

func TestInputTextPreservesAttemptAndDispatchErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	call := registry.Invocation{ADBSerial: "emulator-5554", Inputs: map[string]json.RawMessage{"text": json.RawMessage(`"hello"`)}}
	dispatchErr := &xerror.OperationError{Operation: "dispatch_command", Err: xerror.CommandError{Code: constants.CommandTimedOut}}
	calls := 0
	handler := NewInputText(dispatchFunc(func(context.Context, string, adb.DeviceCommand) error {
		calls++
		return dispatchErr
	}))
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if output, err := handler.Invoke(cancelled, call); output != nil || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("output = %v, error = %v, calls = %d", output, err, calls)
	}
	if output, err := handler.Invoke(ctx, call); output != nil || !errors.Is(err, dispatchErr) || calls != 1 {
		t.Fatalf("output = %v, error = %v, calls = %d", output, err, calls)
	}
}
