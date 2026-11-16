package tap

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type tapDevice struct {
	calls []string
	point adb.TapCommand
	size  adb.DisplaySize
	err   error
}

func (d *tapDevice) DefaultDisplaySize(_ context.Context, serial string) (adb.DisplaySize, error) {
	d.calls = append(d.calls, "display "+serial)
	return d.size, d.err
}

func (d *tapDevice) SelectorPoint(_ context.Context, serial, id string) (adb.TapCommand, error) {
	d.calls = append(d.calls, "selector "+serial+" "+id)
	return adb.TapCommand{X: 42, Y: 23}, d.err
}

func (d *tapDevice) Dispatch(_ context.Context, serial string, command adb.DeviceCommand) error {
	d.calls = append(d.calls, "tap "+serial)
	d.point = command.(adb.TapCommand)
	return d.err
}

func TestTapModes(t *testing.T) {
	const serial = "emulator-5554"
	for _, test := range []struct {
		name, mode, inputs string
		want               adb.TapCommand
		calls              []string
	}{
		{"absolute", constants.AndroidTargetPixels, `{"x":0,"y":100000}`, adb.TapCommand{X: 0, Y: 100_000}, []string{"tap " + serial}},
		{"percentage rounding", constants.AndroidTargetPercent, `{"xPercent":50.0,"yPercent":25}`, adb.TapCommand{X: 540, Y: 480}, []string{"display " + serial, "tap " + serial}},
		{"percentage endpoints", constants.AndroidTargetPercent, `{"xPercent":0,"yPercent":100}`, adb.TapCommand{X: 0, Y: 1919}, []string{"display " + serial, "tap " + serial}},
		{"selector", constants.AndroidTargetSelector, `{"selector_id":"example:id/view"}`, adb.TapCommand{X: 42, Y: 23}, []string{"selector " + serial + " example:id/view", "tap " + serial}},
	} {
		t.Run(test.name, func(t *testing.T) {
			device := tapDevice{size: adb.DisplaySize{Width: 1080, Height: 1920}}
			out, err := runTap(t, &device, serial, test.mode, test.inputs)
			if err != nil || out == nil || len(out) != 0 || device.point != test.want || !slices.Equal(device.calls, test.calls) {
				t.Fatalf("output=%#v, error=%v, point=%+v, calls=%v", out, err, device.point, device.calls)
			}
		})
	}
}

func TestTapRejectsInvalidTargetsWithoutDeviceCalls(t *testing.T) {
	for _, test := range []struct{ name, mode, inputs string }{
		{"missing mode", "", `{"x":1,"y":2}`},
		{"unknown mode", "OTHER", `{"x":1,"y":2}`},
		{"mixed fields", constants.AndroidTargetPixels, `{"x":1,"y":2,"selector_id":"example:id/view"}`},
		{"missing field", constants.AndroidTargetPercent, `{"xPercent":50}`},
		{"fraction", constants.AndroidTargetPixels, `{"x":1.1,"y":2}`},
		{"numeric string", constants.AndroidTargetPixels, `{"x":"1","y":2}`},
		{"null", constants.AndroidTargetPixels, `{"x":null,"y":2}`},
		{"out of range", constants.AndroidTargetPercent, `{"xPercent":101,"yPercent":0}`},
		{"selector whitespace", constants.AndroidTargetSelector, `{"selector_id":" example:id/view"}`},
		{"selector path", constants.AndroidTargetSelector, `{"selector_id":"example:id/view/other"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var device tapDevice
			out, err := runTap(t, &device, "emulator-5554", test.mode, test.inputs)
			var failure xerror.CommandError
			if out != nil || !errors.As(err, &failure) || failure.Code != constants.CommandInputUnsupported ||
				failure.Retryable || len(device.calls) != 0 {
				t.Fatalf("output=%#v, error=%#v, calls=%v", out, err, device.calls)
			}
		})
	}
}

func TestTapPreservesFailuresWithoutFallback(t *testing.T) {
	for _, test := range []struct{ name, mode, inputs, call string }{
		{"display read", constants.AndroidTargetPercent, `{"xPercent":10,"yPercent":20}`, "display emulator-5554"},
		{"selector read", constants.AndroidTargetSelector, `{"selector_id":"example:id/view"}`, "selector emulator-5554 example:id/view"},
		{"dispatch", constants.AndroidTargetPixels, `{"x":1,"y":2}`, "tap emulator-5554"},
	} {
		t.Run(test.name, func(t *testing.T) {
			device := tapDevice{err: context.DeadlineExceeded}
			out, err := runTap(t, &device, "emulator-5554", test.mode, test.inputs)
			if out != nil || !errors.Is(err, device.err) || !slices.Equal(device.calls, []string{test.call}) {
				t.Fatalf("output=%#v, error=%v, calls=%v", out, err, device.calls)
			}
		})
	}
}

func runTap(t *testing.T, device TapDevice, serial, mode, raw string) (map[string]any, error) {
	t.Helper()
	var inputs map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	return NewTap(device).Run(ctx, serial, mode, inputs)
}
