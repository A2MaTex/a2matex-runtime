package delay

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/flow"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Invoke waits within the attempt deadline and returns the requested duration.
func Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := flow.CheckAttempt(ctx); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	var value *float64
	if len(call.Config) != 0 || len(call.Inputs) != 1 ||
		json.Unmarshal(call.Inputs["durationMs"], &value) != nil || value == nil ||
		*value < 0 || *value > constants.MaxFlowDelayMS || math.Trunc(*value) != *value {
		return nil, xerror.WithOperation("validate_inputs",
			xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "Delay requires empty config and one integer durationMs from 0 to 300000"})
	}
	if err := flow.CheckAttempt(ctx); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}

	durationMS := int(*value)
	if durationMS > 0 {
		timer := time.NewTimer(time.Duration(durationMS) * time.Millisecond)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return nil, xerror.WithOperation("wait_for_delay", ctx.Err())
		case <-timer.C:
		}
	}
	if err := flow.CheckAttempt(ctx); err != nil {
		return nil, xerror.WithOperation("wait_for_delay", err)
	}
	return map[string]any{"durationMs": durationMS}, nil
}
