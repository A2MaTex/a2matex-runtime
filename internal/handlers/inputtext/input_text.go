package inputtext

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

type InputText struct {
	dispatcher Dispatcher
}

func NewInputText(dispatcher Dispatcher) *InputText {
	return &InputText{dispatcher: dispatcher}
}

func (h *InputText) Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if _, ok := ctx.Deadline(); !ok || !adb.ValidSerial(call.ADBSerial) {
		return nil, xerror.WithOperation("check_dispatch_context",
			xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"})
	}
	if len(call.Config) != 0 {
		return nil, invalidInput("unexpected_config_fields", "Input Text requires empty config")
	}
	text, present := call.Inputs["text"]
	if len(call.Inputs) != 1 || !present {
		return nil, invalidInput("unexpected_input_fields", "Input Text requires only the text field")
	}
	var value *string
	if json.Unmarshal(text, &value) != nil || value == nil {
		return nil, invalidInput("invalid_text_type", "Input Text requires text to be a string")
	}
	command := adb.InputTextCommand{Text: *value}
	if err := command.Validate(); err != nil {
		return nil, err
	}
	if command.Text == "" {
		return map[string]any{}, nil
	}
	if err := h.dispatcher.Dispatch(ctx, call.ADBSerial, command); err != nil {
		return nil, xerror.WithOperation("dispatch_command", err)
	}
	return map[string]any{}, nil
}

func invalidInput(reason, message string) error {
	return &xerror.OperationError{
		Operation: "validate_inputs",
		Detail:    reason,
		Err:       xerror.CommandError{Code: constants.CommandInputUnsupported, Message: message},
	}
}
