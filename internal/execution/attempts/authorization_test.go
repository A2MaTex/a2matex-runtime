package attempts

import (
	"context"
	"errors"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestAuthorizationHooksFenceRealBeginAndInvoke(t *testing.T) {
	for _, point := range []string{"before begin", "after begin", "allow"} {
		t.Run(point, func(t *testing.T) {
			request := requestFor("flow.start")
			checkpoints := &checkpointStub{}
			calls := 0
			deps := dependencies(t, request, func(context.Context, registry.Invocation) (map[string]any, error) {
				calls++
				return map[string]any{}, nil
			}, checkpoints)
			coordinator, err := New(deps)
			if err != nil {
				t.Fatal(err)
			}
			request.BeforeBegin = func(_ context.Context, timeout int64) (bool, error) {
				if timeout != request.Node.TimeoutMS {
					t.Fatal("wrong timeout")
				}
				return point != "before begin", nil
			}
			request.BeforeInvoke = func(context.Context, int64) error {
				if checkpoints.begins != 1 {
					t.Fatal("hook before durable begin")
				}
				if point == "after begin" {
					return xerror.ErrExecutionControlState
				}
				return nil
			}
			result, err := coordinator.Run(t.Context(), request)
			switch point {
			case "before begin":
				if err != nil || !result.Parked || result.Began || calls != 0 || checkpoints.begins != 0 {
					t.Fatal(result, err, calls)
				}
			case "after begin":
				if !errors.Is(err, xerror.ErrExecutionControlState) || !result.Began || result.Finished || calls != 0 || checkpoints.finishes != 0 {
					t.Fatal(result, err, calls)
				}
			case "allow":
				if err != nil || !result.Finished || calls != 1 || checkpoints.finishes != 1 {
					t.Fatal(result, err, calls)
				}
			}
		})
	}
}
