package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestBuiltInHandlers(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	resolver := adb.NewResolver(config.ADBConfig{ExecutablePath: filepath.Join(t.TempDir(), "unavailable-adb.exe")})
	handlers, err := newHandlerRegistry(catalog, adb.NewDispatcher(resolver))
	if err != nil {
		t.Fatal(err)
	}
	wantSupported := []registry.SupportedNode{
		{Type: "android.input_text", RuntimeHandler: "android.input_text"},
		{Type: "android.launch_app", RuntimeHandler: "android.launch_app"},
		{Type: "android.long_press", RuntimeHandler: "android.long_press"},
		{Type: "android.press_back", RuntimeHandler: "android.press_back"},
		{Type: "android.press_home", RuntimeHandler: "android.press_home"},
		{Type: "android.swipe", RuntimeHandler: "android.swipe"},
		{Type: "android.tap", RuntimeHandler: "android.tap"},
		{Type: "flow.delay", RuntimeHandler: "flow.delay"},
		{Type: "flow.end", RuntimeHandler: "flow.end"},
		{Type: "flow.start", RuntimeHandler: "flow.start"},
	}
	if got := handlers.SupportedNodes(); !reflect.DeepEqual(got, wantSupported) {
		t.Fatalf("supported nodes = %v, want %v", got, wantSupported)
	}
	for _, nodeType := range []string{"flow.delay", "flow.end", "flow.start"} {
		t.Run(nodeType, func(t *testing.T) {
			run, ok := handlers.Lookup(nodeType, nodeType)
			if !ok {
				t.Fatal("registered handler was not found")
			}
			call := registry.Invocation{}
			expected := map[string]any{}
			if nodeType == "flow.delay" {
				call.Inputs = map[string]json.RawMessage{"durationMs": json.RawMessage("0")}
				expected["durationMs"] = 0
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if output, err := run(ctx, call); err != nil || !reflect.DeepEqual(output, expected) {
				t.Fatalf("output = %#v, error = %v, want %#v", output, err, expected)
			}
			var failure xerror.CommandError
			if output, err := run(t.Context(), call); output != nil ||
				!errors.As(err, &failure) || failure.Code != constants.InternalError || failure.Retryable {
				t.Fatalf("output = %#v, error = %#v, want invalid attempt error", output, err)
			}
			cancel()
			if output, err := run(ctx, call); output != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("output = %#v, error = %v, want cancellation", output, err)
			}
			expired, expire := context.WithTimeout(t.Context(), 0)
			defer expire()
			if output, err := run(expired, call); output != nil || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("output = %#v, error = %v, want deadline exceeded", output, err)
			}
		})
	}
}
