package start

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/flow"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
)

// Invoke completes the entry node so the engine can follow its success edge.
func Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	return flow.CompleteEmpty(ctx, call)
}
