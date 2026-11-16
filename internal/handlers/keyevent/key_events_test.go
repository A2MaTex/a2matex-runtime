package keyevent

import (
	"context"
	"encoding/json"
	"errors"
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
	ctx     context.Context
	serial  string
	command adb.DeviceCommand
	err     error
}

func (d *dispatchStub) Dispatch(ctx context.Context, serial string, command adb.DeviceCommand) error {
	d.calls++
	d.ctx, d.serial, d.command = ctx, serial, command
	return d.err
}

func TestKeyEventsRegisteredCommands(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &dispatchStub{}
	handlers := NewKeyEvents(dispatcher)
	r, err := registry.New(catalog, []registry.Registration{
		{Type: "android.press_back", RuntimeHandler: "android.press_back", Run: handlers.PressBack},
		{Type: "android.press_home", RuntimeHandler: "android.press_home", Run: handlers.PressHome},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("android.press_back", "android.press_home"); ok {
		t.Fatal("a mismatched type and handler key resolved")
	}
	for _, tc := range []struct {
		node    string
		config  map[string]json.RawMessage
		inputs  map[string]json.RawMessage
		command adb.DeviceCommand
	}{
		{node: "android.press_back", command: adb.PressBackCommand{}},
		{node: "android.press_home", config: map[string]json.RawMessage{}, inputs: map[string]json.RawMessage{}, command: adb.PressHomeCommand{}},
	} {
		t.Run(tc.node, func(t *testing.T) {
			*dispatcher = dispatchStub{}
			invoke, ok := r.Lookup(tc.node, tc.node)
			if !ok {
				t.Fatal("registered key handler is missing")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			call := registry.Invocation{ADBSerial: "192.0.2.5:5555", Config: tc.config, Inputs: tc.inputs}
			output, err := invoke(ctx, call)
			if err != nil || output == nil || len(output) != 0 {
				t.Fatalf("output = %#v, error = %v, want empty output", output, err)
			}
			if dispatcher.calls != 1 || dispatcher.command != tc.command || dispatcher.ctx != ctx || dispatcher.serial != call.ADBSerial {
				t.Fatalf("unexpected dispatch: %+v, want one call for %q with %T", dispatcher, call.ADBSerial, tc.command)
			}
		})
	}
}

func TestKeyEventsRejectBeforeDispatch(t *testing.T) {
	dispatcher := &dispatchStub{}
	handlers := NewKeyEvents(dispatcher)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	call := registry.Invocation{ADBSerial: "emulator-5554"}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		call registry.Invocation
		code string
	}{
		{"extra config", ctx, registry.Invocation{ADBSerial: call.ADBSerial, Config: map[string]json.RawMessage{"keyCode": json.RawMessage(`null`)}}, constants.CommandInputUnsupported},
		{"extra input", ctx, registry.Invocation{ADBSerial: call.ADBSerial, Inputs: map[string]json.RawMessage{"repeat": json.RawMessage(`2`)}}, constants.CommandInputUnsupported},
		{"invalid serial", ctx, registry.Invocation{ADBSerial: "emulator 5554"}, constants.InternalError},
		{"missing deadline", context.WithoutCancel(t.Context()), call, constants.InternalError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := handlers.PressBack(tc.ctx, tc.call)
			var commandErr xerror.CommandError
			if output != nil || !errors.As(err, &commandErr) || commandErr.Code != tc.code || commandErr.Retryable || dispatcher.calls != 0 {
				t.Fatalf("output = %v, error = %v, calls = %d", output, err, dispatcher.calls)
			}
		})
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	expired, expire := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer expire()
	for _, ctx := range []context.Context{cancelled, expired} {
		output, err := handlers.PressHome(ctx, call)
		if output != nil || !errors.Is(err, ctx.Err()) || dispatcher.calls != 0 {
			t.Fatalf("output = %v, error = %v, calls = %d", output, err, dispatcher.calls)
		}
	}
}

func TestKeyEventsPreserveDispatchFailureWithoutRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, failure := range []error{
		&xerror.OperationError{Operation: "resolve_adb", Err: xerror.CommandError{Code: constants.ADBUnavailable, Retryable: true}},
		xerror.CommandError{Code: constants.CommandStartFailed, Retryable: true},
		xerror.CommandError{Code: constants.CommandFailed},
		context.Canceled,
		context.DeadlineExceeded,
	} {
		dispatcher := &dispatchStub{err: failure}
		output, err := NewKeyEvents(dispatcher).PressHome(ctx, registry.Invocation{ADBSerial: "emulator-5554"})
		if output != nil || !errors.Is(err, failure) || dispatcher.calls != 1 {
			t.Fatalf("output = %v, error = %v, calls = %d, want %v and one dispatch", output, err, dispatcher.calls, failure)
		}
	}
}
