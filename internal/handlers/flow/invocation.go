package flow

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// CheckAttempt verifies that the caller's attempt is still live.
func CheckAttempt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return xerror.CommandError{Code: constants.InternalError, Message: "node attempt deadline is required"}
	}
	if !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// CompleteEmpty implements the shared behavior of Start and End.
func CompleteEmpty(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := CheckAttempt(ctx); err != nil {
		return nil, err
	}
	if len(call.Config) != 0 || len(call.Inputs) != 0 {
		return nil, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "node requires empty config and inputs"}
	}
	if err := CheckAttempt(ctx); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}
