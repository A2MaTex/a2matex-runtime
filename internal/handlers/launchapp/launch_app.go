package launchapp

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Dispatcher interface {
	Dispatch(context.Context, string, adb.DeviceCommand) error
}

type LaunchApp struct {
	dispatcher Dispatcher
}

func NewLaunchApp(dispatcher Dispatcher) *LaunchApp {
	return &LaunchApp{dispatcher: dispatcher}
}

func (h *LaunchApp) Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if _, ok := ctx.Deadline(); !ok || !adb.ValidSerial(call.ADBSerial) {
		return nil, xerror.WithOperation("check_dispatch_context",
			xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"})
	}
	if len(call.Config) != 0 {
		return nil, invalidLaunchInput("unexpected_config_fields", "Launch App requires empty config")
	}
	packageName, present := call.Inputs["packageName"]
	if len(call.Inputs) != 1 || !present {
		return nil, invalidLaunchInput("unexpected_input_fields", "Launch App requires only packageName")
	}
	var command adb.LaunchAppCommand
	if json.Unmarshal(packageName, &command.PackageName) != nil || !command.Valid() {
		return nil, invalidLaunchInput("invalid_package_name", "Launch App requires a valid Android packageName")
	}
	if err := h.dispatcher.Dispatch(ctx, call.ADBSerial, command); err != nil {
		return nil, xerror.WithOperation("dispatch_command", err)
	}
	return map[string]any{}, nil
}

func invalidLaunchInput(reason, message string) error {
	return &xerror.OperationError{
		Operation: "validate_inputs",
		Detail:    reason,
		Err:       xerror.CommandError{Code: constants.CommandInputUnsupported, Message: message},
	}
}
