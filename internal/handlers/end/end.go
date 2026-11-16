package end

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/flow"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
)

// Invoke completes the terminal node so the engine can settle the device execution.
func Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	return flow.CompleteEmpty(ctx, call)
}
