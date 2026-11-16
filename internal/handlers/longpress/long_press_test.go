package longpress

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const testSerial = "192.0.2.5:5555"

type controlledDevice struct {
	calls       []string
	serials     []string
	selectorID  string
	size        adb.DisplaySize
	point       adb.TapCommand
	command     adb.DeviceCommand
	readErr     error
	dispatchErr error
	onRead      func()
}

func (d *controlledDevice) record(operation, serial string) {
	d.calls = append(d.calls, operation)
	d.serials = append(d.serials, serial)
}

func (d *controlledDevice) DefaultDisplaySize(_ context.Context, serial string) (adb.DisplaySize, error) {
	d.record("display", serial)
	if d.onRead != nil {
		d.onRead()
	}
	return d.size, d.readErr
}

func (d *controlledDevice) SelectorPoint(_ context.Context, serial, resourceID string) (adb.TapCommand, error) {
	d.record("selector", serial)
	d.selectorID = resourceID
	if d.onRead != nil {
		d.onRead()
	}
	return d.point, d.readErr
}

func (d *controlledDevice) Dispatch(_ context.Context, serial string, command adb.DeviceCommand) error {
	d.record("dispatch", serial)
	d.command = command
	return d.dispatchErr
}

func TestLongPressRegistryAndTargetModes(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode, inputs string
		size               adb.DisplaySize
		calls              []string
		want               adb.LongPressCommand
	}{
		{"pixels, lower duration bound", "ABSOLUTE_PIXELS", `{"x":0,"y":100000,"durationMs":1}`, adb.DisplaySize{},
			[]string{"dispatch"}, adb.LongPressCommand{Y: 100000, DurationMS: 1}},
		{"portrait percentages", "SCREEN_PERCENT", `{"xPercent":50,"yPercent":80,"durationMs":800.0}`, adb.DisplaySize{Width: 1080, Height: 1920},
			[]string{"display", "dispatch"}, adb.LongPressCommand{X: 540, Y: 1535, DurationMS: 800}},
		{"landscape percentage boundaries", "SCREEN_PERCENT", `{"xPercent":100,"yPercent":0,"durationMs":800}`, adb.DisplaySize{Width: 1920, Height: 1080},
			[]string{"display", "dispatch"}, adb.LongPressCommand{X: 1919, DurationMS: 800}},
		{"selector, upper duration bound", "SELECTOR_ID", `{"selector_id":"com.example.demo:id/Hold_Target","durationMs":60000}`, adb.DisplaySize{},
			[]string{"selector", "dispatch"}, adb.LongPressCommand{X: 60, Y: 70, DurationMS: 60000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := &controlledDevice{size: tc.size, point: adb.TapCommand{X: 60, Y: 70}}
			r, err := registry.New(catalog, []registry.Registration{{
				Type: "android.long_press", RuntimeHandler: "android.long_press", Run: NewLongPress(device).Invoke,
			}})
			if err != nil {
				t.Fatal(err)
			}
			invoke, ok := r.Lookup("android.long_press", "android.long_press")
			if !ok {
				t.Fatal("expected registered Long Press handler")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 65*time.Second)
			defer cancel()
			call := invocation(t, `{"targetMode":"`+tc.mode+`"}`, tc.inputs)
			output, err := invoke(ctx, call)
			if err != nil || output == nil || len(output) != 0 {
				t.Fatalf("output = %#v, error = %v, want non-nil empty output", output, err)
			}
			if !slices.Equal(device.calls, tc.calls) || device.command != tc.want {
				t.Fatalf("calls = %v, command = %+v, want %v and %+v", device.calls, device.command, tc.calls, tc.want)
			}
			for _, serial := range device.serials {
				if serial != call.ADBSerial {
					t.Fatalf("serial = %q, want %q", serial, call.ADBSerial)
				}
			}
			if tc.mode == constants.AndroidTargetSelector && device.selectorID != "com.example.demo:id/Hold_Target" {
				t.Fatalf("selector was changed: %q", device.selectorID)
			}
		})
	}
}

func TestLongPressRejectsInvalidInputBeforeDeviceIO(t *testing.T) {
	const pixels = `{"targetMode":"ABSOLUTE_PIXELS"}`
	const percent = `{"targetMode":"SCREEN_PERCENT"}`
	const selector = `{"targetMode":"SELECTOR_ID"}`
	for _, tc := range []struct{ name, config, inputs string }{
		{"missing mode", `{}`, `{"x":0,"y":0,"durationMs":800}`},
		{"unknown mode", `{"targetMode":"AUTO"}`, `{"x":0,"y":0,"durationMs":800}`},
		{"null mode", `{"targetMode":null}`, `{"x":0,"y":0,"durationMs":800}`},
		{"extra config", `{"targetMode":"ABSOLUTE_PIXELS","other":true}`, `{"x":0,"y":0,"durationMs":800}`},
		{"zero duration", pixels, `{"x":0,"y":0,"durationMs":0}`},
		{"excessive percent duration", percent, `{"xPercent":50,"yPercent":80,"durationMs":60001}`},
		{"excessive selector duration", selector, `{"selector_id":"com.example.demo:id/hold","durationMs":60001}`},
		{"missing duration", selector, `{"selector_id":"com.example.demo:id/hold"}`},
		{"fractional duration", pixels, `{"x":0,"y":0,"durationMs":800.5}`},
		{"string duration", pixels, `{"x":0,"y":0,"durationMs":"800"}`},
		{"null coordinate", pixels, `{"x":null,"y":0,"durationMs":800}`},
		{"coordinate over limit", pixels, `{"x":100001,"y":0,"durationMs":800}`},
		{"missing coordinate", percent, `{"xPercent":50,"durationMs":800}`},
		{"mixed targets", percent, `{"xPercent":50,"yPercent":80,"x":20,"durationMs":800}`},
		{"invalid selector", selector, `{"selector_id":"hold","durationMs":800}`},
		{"extra selector input", selector, `{"selector_id":"com.example.demo:id/hold","x":1,"durationMs":800}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := &controlledDevice{}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			output, err := NewLongPress(device).Invoke(ctx, invocation(t, tc.config, tc.inputs))
			var commandErr xerror.CommandError
			var operation *xerror.OperationError
			if output != nil || !errors.As(err, &commandErr) || commandErr.Code != constants.CommandInputUnsupported || commandErr.Retryable ||
				!errors.As(err, &operation) || operation.Operation != "validate_inputs" || len(device.calls) != 0 {
				t.Fatalf("output = %v, error = %v, device calls = %v", output, err, device.calls)
			}
		})
	}
}

func TestLongPressFailureStopsWithoutFallback(t *testing.T) {
	const selector = `{"targetMode":"SELECTOR_ID"}`
	const input = `{"selector_id":"com.example.demo:id/hold","durationMs":800}`
	selectorFailure := &xerror.OperationError{Operation: "read_hierarchy", Err: errors.New("controlled hierarchy failure")}
	for _, tc := range []struct {
		name           string
		device         controlledDevice
		cancelDuringIO bool
		want           error
		calls          []string
	}{
		{"selector fails", controlledDevice{readErr: selectorFailure}, false, selectorFailure, []string{"selector"}},
		{"cancelled after selector read", controlledDevice{}, true, context.Canceled, []string{"selector"}},
		{"dispatch is cancelled", controlledDevice{dispatchErr: context.Canceled}, false, context.Canceled, []string{"selector", "dispatch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if tc.cancelDuringIO {
				tc.device.onRead = cancel
			}
			output, err := NewLongPress(&tc.device).Invoke(ctx, invocation(t, selector, input))
			if output != nil || !errors.Is(err, tc.want) || !slices.Equal(tc.device.calls, tc.calls) {
				t.Fatalf("output = %v, error = %v, calls = %v, want %v and %v", output, err, tc.device.calls, tc.want, tc.calls)
			}
		})
	}
}

func invocation(t *testing.T, config, inputs string) registry.Invocation {
	t.Helper()
	call := registry.Invocation{ADBSerial: testSerial}
	if err := json.Unmarshal([]byte(config), &call.Config); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(inputs), &call.Inputs); err != nil {
		t.Fatal(err)
	}
	return call
}
