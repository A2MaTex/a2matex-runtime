package tap

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
)

// Invoke adapts the common registry invocation to the existing Tap handler.
func (h *Tap) Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	var mode string
	if err := json.Unmarshal(call.Config["targetMode"], &mode); err != nil {
		mode = ""
	}
	return h.Run(ctx, call.ADBSerial, mode, call.Inputs)
}
