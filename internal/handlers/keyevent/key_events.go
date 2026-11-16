package keyevent

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Dispatcher interface {
	Dispatch(context.Context, string, adb.DeviceCommand) error
}

type KeyEvents struct {
	dispatcher Dispatcher
}

func NewKeyEvents(dispatcher Dispatcher) *KeyEvents {
	return &KeyEvents{dispatcher: dispatcher}
}

func (h *KeyEvents) PressBack(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	return h.press(ctx, call, adb.PressBackCommand{})
}

func (h *KeyEvents) PressHome(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	return h.press(ctx, call, adb.PressHomeCommand{})
}

func (h *KeyEvents) press(ctx context.Context, call registry.Invocation, command adb.DeviceCommand) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if _, ok := ctx.Deadline(); !ok || !adb.ValidSerial(call.ADBSerial) {
		return nil, xerror.WithOperation("check_dispatch_context",
			xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"})
	}
	if len(call.Config) != 0 || len(call.Inputs) != 0 {
		return nil, xerror.WithOperation("validate_inputs",
			xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "Android key events require empty config and inputs"})
	}
	if err := h.dispatcher.Dispatch(ctx, call.ADBSerial, command); err != nil {
		return nil, xerror.WithOperation("dispatch_command", err)
	}
	return map[string]any{}, nil
}
