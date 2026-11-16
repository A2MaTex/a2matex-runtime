package swipe

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type testDevice struct {
	display       adb.DisplaySize
	displayErr    error
	afterRead     func()
	dispatchErr   error
	command       adb.DeviceCommand
	displayCalls  []string
	dispatchCalls []string
}

func (d *testDevice) DefaultDisplaySize(_ context.Context, serial string) (adb.DisplaySize, error) {
	d.displayCalls = append(d.displayCalls, serial)
	if d.afterRead != nil {
		d.afterRead()
	}
	return d.display, d.displayErr
}

func (d *testDevice) Dispatch(_ context.Context, serial string, command adb.DeviceCommand) error {
	d.dispatchCalls = append(d.dispatchCalls, serial)
	d.command = command
	return d.dispatchErr
}

func TestSwipeModes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	device := &testDevice{}
	handler := NewSwipe(device)

	pixels := invocation(t, constants.AndroidTargetPixels,
		`{"startX":540,"startY":1535,"endX":540,"endY":384,"durationMs":300.0}`)
	out, err := handler.Invoke(ctx, pixels)
	portrait := adb.SwipeCommand{StartX: 540, StartY: 1535, EndX: 540, EndY: 384, DurationMS: 300}
	if err != nil || out == nil || len(out) != 0 || device.command != portrait || len(device.displayCalls) != 0 {
		t.Fatalf("pixel swipe: output=%v, error=%v, device=%+v", out, err, device)
	}

	percent := invocation(t, constants.AndroidTargetPercent,
		`{"startXPercent":50,"startYPercent":80,"endXPercent":50,"endYPercent":20,"durationMs":300}`)
	for _, test := range []struct {
		serial string
		size   adb.DisplaySize
		want   adb.SwipeCommand
	}{
		{"emulator-5554", adb.DisplaySize{Width: 1080, Height: 1920}, portrait},
		{"192.0.2.5:5555", adb.DisplaySize{Width: 1920, Height: 1080},
			adb.SwipeCommand{StartX: 960, StartY: 863, EndX: 960, EndY: 216, DurationMS: 300}},
	} {
		percent.ADBSerial, device.display = test.serial, test.size
		out, err := handler.Invoke(ctx, percent)
		if err != nil || out == nil || len(out) != 0 || device.command != test.want {
			t.Fatalf("percent swipe: output=%v, error=%v, command=%+v, want %+v", out, err, device.command, test.want)
		}
	}
	if !slices.Equal(device.displayCalls, []string{"emulator-5554", "192.0.2.5:5555"}) ||
		!slices.Equal(device.dispatchCalls, []string{"emulator-5554", "emulator-5554", "192.0.2.5:5555"}) {
		t.Fatalf("display calls=%v, dispatch calls=%v", device.displayCalls, device.dispatchCalls)
	}
}

func TestSwipeInputValidation(t *testing.T) {
	const pixels = `{"startX":0,"startY":0,"endX":100000,"endY":100000,"durationMs":1}`
	const percent = `{"startXPercent":0,"startYPercent":100,"endXPercent":100,"endYPercent":0,"durationMs":60000}`
	for _, test := range []struct {
		name, mode, raw string
		edit            func(*registry.Invocation)
		wantValid       bool
	}{
		{"minimum duration and pixel bounds", constants.AndroidTargetPixels, pixels, nil, true},
		{"maximum duration and percent bounds", constants.AndroidTargetPercent, percent, nil, true},
		{"selector mode", constants.AndroidTargetSelector, pixels, nil, false},
		{"missing mode", "", pixels, nil, false},
		{"unknown mode", "OTHER", pixels, nil, false},
		{"mixed fields", constants.AndroidTargetPixels, percent, nil, false},
		{"extra input", constants.AndroidTargetPixels, pixels, func(c *registry.Invocation) { c.Inputs["extra"] = json.RawMessage("1") }, false},
		{"extra config", constants.AndroidTargetPixels, pixels, func(c *registry.Invocation) { c.Config["extra"] = json.RawMessage("1") }, false},
		{"missing endpoint", constants.AndroidTargetPixels, pixels, func(c *registry.Invocation) { delete(c.Inputs, "endY") }, false},
		{"null coordinate", constants.AndroidTargetPercent, percent, func(c *registry.Invocation) { c.Inputs["startXPercent"] = json.RawMessage("null") }, false},
		{"numeric string", constants.AndroidTargetPixels, pixels, func(c *registry.Invocation) { c.Inputs["startX"] = json.RawMessage(`"1"`) }, false},
		{"fractional duration", constants.AndroidTargetPixels, pixels, func(c *registry.Invocation) { c.Inputs["durationMs"] = json.RawMessage("1.5") }, false},
		{"zero duration", constants.AndroidTargetPixels, pixels, func(c *registry.Invocation) { c.Inputs["durationMs"] = json.RawMessage("0") }, false},
		{"duration above limit", constants.AndroidTargetPercent, percent, func(c *registry.Invocation) { c.Inputs["durationMs"] = json.RawMessage("60001") }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := invocation(t, test.mode, test.raw)
			if test.edit != nil {
				test.edit(&call)
			}
			device := &testDevice{display: adb.DisplaySize{Width: 1080, Height: 1920}}
			ctx, cancel := context.WithTimeout(t.Context(), 65*time.Second)
			defer cancel()
			out, err := NewSwipe(device).Invoke(ctx, call)
			if test.wantValid {
				if err != nil || out == nil || len(device.dispatchCalls) != 1 {
					t.Fatalf("valid input failed: output=%v, error=%v, device=%+v", out, err, device)
				}
			} else {
				var failure xerror.CommandError
				if out != nil || !errors.As(err, &failure) || failure.Code != constants.CommandInputUnsupported ||
					failure.Retryable || len(device.displayCalls) != 0 || len(device.dispatchCalls) != 0 {
					t.Fatalf("invalid input reached device: output=%v, error=%v, device=%+v", out, err, device)
				}
			}
		})
	}
}

func TestSwipeFailureStopsWithoutFallback(t *testing.T) {
	call := invocation(t, constants.AndroidTargetPercent,
		`{"startXPercent":0,"startYPercent":100,"endXPercent":100,"endYPercent":0,"durationMs":300}`)
	deviceErr := xerror.CommandError{Code: constants.CommandFailed, Message: "test command failed"}
	for _, test := range []struct {
		name       string
		device     testDevice
		cancelRead bool
		want       error
		wantSwipes int
	}{
		{"display query failed", testDevice{displayErr: deviceErr}, false, deviceErr, 0},
		{"unusable display", testDevice{}, false, xerror.CommandError{Code: constants.CommandFailed, Message: "cannot determine the current default display size"}, 0},
		{"converted point above limit", testDevice{display: adb.DisplaySize{Width: 100002, Height: 1920}}, false,
			xerror.CommandError{Code: constants.CommandFailed, Message: "resolved Android Swipe points are out of range"}, 0},
		{"cancelled after reading", testDevice{display: adb.DisplaySize{Width: 1080, Height: 1920}}, true, context.Canceled, 0},
		{"dispatch failed without retry", testDevice{display: adb.DisplaySize{Width: 1080, Height: 1920}, dispatchErr: deviceErr}, false, deviceErr, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if test.cancelRead {
				test.device.afterRead = cancel
			}
			out, err := NewSwipe(&test.device).Invoke(ctx, call)
			if out != nil || !errors.Is(err, test.want) || len(test.device.displayCalls) != 1 ||
				len(test.device.dispatchCalls) != test.wantSwipes {
				t.Fatalf("output=%v, error=%v, device=%+v", out, err, test.device)
			}
		})
	}
}

func invocation(t *testing.T, mode, inputs string) registry.Invocation {
	t.Helper()
	modeJSON, err := json.Marshal(mode)
	if err != nil {
		t.Fatal(err)
	}
	call := registry.Invocation{
		ADBSerial: "emulator-5554",
		Config:    map[string]json.RawMessage{"targetMode": modeJSON},
	}
	if err := json.Unmarshal([]byte(inputs), &call.Inputs); err != nil {
		t.Fatal(err)
	}
	return call
}
