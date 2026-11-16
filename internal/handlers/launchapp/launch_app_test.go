package launchapp

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

type dispatchStub struct {
	calls   int
	context context.Context
	serial  string
	command adb.DeviceCommand
	err     error
}

func (d *dispatchStub) Dispatch(ctx context.Context, serial string, command adb.DeviceCommand) error {
	d.calls++
	d.context, d.serial, d.command = ctx, serial, command
	return d.err
}

func TestLaunchAppRegisteredHandler(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &dispatchStub{}
	reg, err := registry.New(catalog, []registry.Registration{
		{Type: "android.launch_app", RuntimeHandler: "android.launch_app", Run: NewLaunchApp(dispatcher).Invoke},
	})
	if err != nil {
		t.Fatal(err)
	}
	invoke, ok := reg.Lookup("android.launch_app", "android.launch_app")
	if !ok {
		t.Fatal("registered Launch App handler is missing")
	}
	for _, pkg := range []string{"A.b", "com.Example.App_5", "a." + strings.Repeat("b", 253)} {
		*dispatcher = dispatchStub{}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		call := registry.Invocation{ADBSerial: "192.0.2.5:5555", Inputs: map[string]json.RawMessage{
			"packageName": json.RawMessage(`"` + pkg + `"`),
		}}
		output, err := invoke(ctx, call)
		cancel()
		if err != nil || output == nil || len(output) != 0 {
			t.Fatalf("output = %#v, error = %v, want empty output", output, err)
		}
		if dispatcher.calls != 1 || dispatcher.context != ctx || dispatcher.serial != call.ADBSerial ||
			dispatcher.command != (adb.LaunchAppCommand{PackageName: pkg}) {
			t.Fatalf("unexpected dispatch: %+v", dispatcher)
		}
	}
}

func TestLaunchAppRejectsInvalidInputBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, inputs string
		config       map[string]json.RawMessage
	}{
		{name: "missing package", inputs: `{}`},
		{name: "null package", inputs: `{"packageName":null}`},
		{name: "numeric package", inputs: `{"packageName":42}`},
		{name: "extra field", inputs: `{"packageName":"com.example.app","activity":".Main"}`},
		{name: "unexpected config", inputs: `{"packageName":"com.example.app"}`, config: map[string]json.RawMessage{"user": json.RawMessage(`0`)}},
		{name: "single segment", inputs: `{"packageName":"example"}`},
		{name: "leading whitespace", inputs: `{"packageName":" com.example"}`},
		{name: "component", inputs: `{"packageName":"com.example/.Main"}`},
		{name: "URI", inputs: `{"packageName":"intent://com.example"}`},
		{name: "shell syntax", inputs: `{"packageName":"com.example;x"}`},
		{name: "above maximum", inputs: `{"packageName":"a.` + strings.Repeat("b", 254) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &dispatchStub{}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			call := registry.Invocation{ADBSerial: "emulator-5554", Config: tc.config}
			if err := json.Unmarshal([]byte(tc.inputs), &call.Inputs); err != nil {
				t.Fatal(err)
			}
			output, err := NewLaunchApp(dispatcher).Invoke(ctx, call)
			var commandErr xerror.CommandError
			var operation *xerror.OperationError
			if output != nil || !errors.As(err, &commandErr) || commandErr.Code != constants.CommandInputUnsupported || commandErr.Retryable ||
				!errors.As(err, &operation) || operation.Operation != "validate_inputs" || dispatcher.calls != 0 {
				t.Fatalf("output = %v, error = %v, calls = %d", output, err, dispatcher.calls)
			}
		})
	}
}

func TestLaunchAppAttemptAndDispatchErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	call := registry.Invocation{ADBSerial: "emulator-5554", Inputs: map[string]json.RawMessage{"packageName": json.RawMessage(`"com.example.app"`)}}
	dispatchFailure := &xerror.OperationError{Operation: "parse_launch_result", Err: xerror.CommandError{Code: constants.CommandTimedOut}}
	dispatcher := &dispatchStub{err: dispatchFailure}
	handler := NewLaunchApp(dispatcher)
	cancelled, stop := context.WithCancel(ctx)
	stop()
	expired, expire := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer expire()
	for _, blocked := range []context.Context{cancelled, expired} {
		output, err := handler.Invoke(blocked, call)
		if output != nil || !errors.Is(err, blocked.Err()) || dispatcher.calls != 0 {
			t.Fatalf("output = %v, error = %v, calls = %d", output, err, dispatcher.calls)
		}
	}
	output, err := handler.Invoke(ctx, call)
	if output != nil || !errors.Is(err, dispatchFailure) || dispatcher.calls != 1 {
		t.Fatalf("output = %v, error = %v, calls = %d, want one failed launch", output, err, dispatcher.calls)
	}
}
