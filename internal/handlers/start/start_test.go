package start

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestInvoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if output, err := Invoke(ctx, registry.Invocation{}); err != nil || output == nil || len(output) != 0 {
		t.Fatalf("output = %#v, error = %v, want empty output and no error", output, err)
	}
	call := registry.Invocation{Inputs: map[string]json.RawMessage{"extra": json.RawMessage("1")}}
	output, err := Invoke(ctx, call)
	var failure xerror.CommandError
	if output != nil || !errors.As(err, &failure) || failure.Code != constants.CommandInputUnsupported || failure.Retryable {
		t.Fatalf("output = %#v, error = %#v, want non-retryable invalid input", output, err)
	}
}
