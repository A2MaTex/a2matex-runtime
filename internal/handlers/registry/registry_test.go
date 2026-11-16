package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/tap"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func testCatalog(t *testing.T) *nodecontract.Registry {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func emptyHandler(context.Context, registry.Invocation) (map[string]any, error) {
	return map[string]any{}, nil
}

func TestRejectsInvalidRegistrations(t *testing.T) {
	catalog := testCatalog(t)
	tap := registry.Registration{Type: "android.tap", RuntimeHandler: "android.tap", Run: emptyHandler}
	for _, test := range []struct {
		name    string
		entries []registry.Registration
	}{
		{"missing type", []registry.Registration{{RuntimeHandler: "android.tap", Run: emptyHandler}}},
		{"missing key", []registry.Registration{{Type: "android.tap", Run: emptyHandler}}},
		{"unknown type", []registry.Registration{{Type: "android.unknown", RuntimeHandler: "android.unknown", Run: emptyHandler}}},
		{"incorrect key", []registry.Registration{{Type: "android.tap", RuntimeHandler: "android.swipe", Run: emptyHandler}}},
		{"nil callable", []registry.Registration{{Type: "android.tap", RuntimeHandler: "android.tap"}}},
		{"duplicate registration", []registry.Registration{tap, tap}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := registry.New(catalog, test.entries); got != nil || err == nil {
				t.Fatalf("registry=%v, error=%v, want nil registry and error", got, err)
			}
		})
	}
}

func TestSupportedNodesSnapshot(t *testing.T) {
	catalog := testCatalog(t)
	empty, err := registry.New(catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	if nodes := empty.SupportedNodes(); nodes == nil || len(nodes) != 0 {
		t.Fatalf("empty registry returned %#v", nodes)
	}
	entries := []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: emptyHandler},
		{Type: "android.tap", RuntimeHandler: "android.tap", Run: emptyHandler},
	}
	handlers, err := registry.New(catalog, entries)
	if err != nil {
		t.Fatal(err)
	}
	entries[1] = registry.Registration{}
	want := []registry.SupportedNode{
		{Type: "android.tap", RuntimeHandler: "android.tap"}, {Type: "flow.start", RuntimeHandler: "flow.start"},
	}
	for _, expected := range want {
		t.Run(expected.Type, func(t *testing.T) {
			t.Parallel()
			first := handlers.SupportedNodes()
			if !reflect.DeepEqual(first, want) {
				t.Fatalf("supported nodes=%v, want %v", first, want)
			}
			first[0] = registry.SupportedNode{}
			if _, ok := handlers.Lookup(expected.Type, expected.RuntimeHandler); !ok {
				t.Fatalf("could not look up %s", expected.Type)
			}
			if next := handlers.SupportedNodes(); !reflect.DeepEqual(next, want) {
				t.Fatalf("caller changed supported nodes to %v", next)
			}
		})
	}
}

func TestLookupAndInvokeTap(t *testing.T) {
	device := &controlledTap{}
	tapHandler := tap.NewTap(device)
	handlers, err := registry.New(testCatalog(t), []registry.Registration{
		{Type: "android.tap", RuntimeHandler: "android.tap", Run: tapHandler.Invoke},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"android.swipe", "android.tap"}, {"android.tap", "android.unknown"}, {"Android.tap", "android.tap"}} {
		if found, ok := handlers.Lookup(pair[0], pair[1]); found != nil || ok {
			t.Fatalf("found handler for %v", pair)
		}
	}
	run, ok := handlers.Lookup("android.tap", "android.tap")
	if !ok {
		t.Fatal("Tap is not registered")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	call := registry.Invocation{
		ADBSerial: "emulator-5554",
		Config:    map[string]json.RawMessage{"targetMode": json.RawMessage(`"ABSOLUTE_PIXELS"`)},
		Inputs:    map[string]json.RawMessage{"x": json.RawMessage("120"), "y": json.RawMessage("300")},
	}
	out, err := run(ctx, call)
	if err != nil || out == nil || len(out) != 0 || device.count != 1 ||
		device.serial != call.ADBSerial || device.command != (adb.TapCommand{X: 120, Y: 300}) {
		t.Fatalf("output=%v, error=%v, device=%+v", out, err, device)
	}
	device.err = xerror.CommandError{Code: constants.CommandStartFailed, Message: "test action could not start", Retryable: true}
	if out, err := run(ctx, call); out != nil || !errors.Is(err, device.err) {
		t.Fatalf("handler changed the device error: output=%v, error=%v", out, err)
	}

	for _, rawMode := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("123")} {
		call.Config["targetMode"] = rawMode
		out, err := run(ctx, call)
		var failure xerror.CommandError
		if out != nil || !errors.As(err, &failure) || failure.Code != constants.CommandInputUnsupported || device.count != 2 {
			t.Fatalf("invalid mode reached device: output=%v, error=%v, calls=%d", out, err, device.count)
		}
	}
}

type controlledTap struct {
	count   int
	serial  string
	command adb.DeviceCommand
	err     error
}

func (d *controlledTap) Dispatch(_ context.Context, serial string, command adb.DeviceCommand) error {
	d.count++
	d.serial, d.command = serial, command
	return d.err
}

func (*controlledTap) DefaultDisplaySize(context.Context, string) (adb.DisplaySize, error) {
	return adb.DisplaySize{}, errors.New("unexpected display query")
}

func (*controlledTap) SelectorPoint(context.Context, string, string) (adb.TapCommand, error) {
	return adb.TapCommand{}, errors.New("unexpected selector query")
}
