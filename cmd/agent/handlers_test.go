package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestBuiltInFlowHandlers(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := newHandlerRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	wantSupported := []registry.SupportedNode{
		{Type: "flow.delay", RuntimeHandler: "flow.delay"},
		{Type: "flow.end", RuntimeHandler: "flow.end"},
		{Type: "flow.start", RuntimeHandler: "flow.start"},
	}
	if got := handlers.SupportedNodes(); !reflect.DeepEqual(got, wantSupported) {
		t.Fatalf("supported nodes = %v, want %v", got, wantSupported)
	}
	for _, node := range wantSupported {
		t.Run(node.Type, func(t *testing.T) {
			run, ok := handlers.Lookup(node.Type, node.RuntimeHandler)
			if !ok {
				t.Fatal("registered handler was not found")
			}
			call := registry.Invocation{}
			expected := map[string]any{}
			if node.Type == "flow.delay" {
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
