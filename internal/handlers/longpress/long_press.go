package longpress

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/targeting"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type LongPressDevice interface {
	Dispatch(context.Context, string, adb.DeviceCommand) error
	DefaultDisplaySize(context.Context, string) (adb.DisplaySize, error)
	SelectorPoint(context.Context, string, string) (adb.TapCommand, error)
}

type LongPress struct {
	device LongPressDevice
}

type target struct {
	mode       string
	selectorID string
	command    adb.LongPressCommand
}

var selectorIDPattern = regexp.MustCompile(constants.AndroidSelectorIDPattern)

func NewLongPress(device LongPressDevice) *LongPress {
	return &LongPress{device: device}
}

// Invoke resolves one point and dispatches one Long Press on the selected device.
func (h *LongPress) Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if _, ok := ctx.Deadline(); !ok || !adb.ValidSerial(call.ADBSerial) {
		return nil, xerror.WithOperation("check_dispatch_context",
			xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"})
	}
	target, err := readTarget(call)
	if err != nil {
		return nil, err
	}
	command, err := h.resolvePoint(ctx, call.ADBSerial, target)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if !command.Valid() {
		return nil, xerror.WithOperation("resolve_long_press_point",
			xerror.CommandError{Code: constants.CommandFailed, Message: "resolved Android Long Press point is out of range"})
	}
	if err := h.device.Dispatch(ctx, call.ADBSerial, command); err != nil {
		return nil, xerror.WithOperation("dispatch_command", err)
	}
	return map[string]any{}, nil
}

func readTarget(call registry.Invocation) (target, error) {
	var value target
	if len(call.Config) != 1 || json.Unmarshal(call.Config["targetMode"], &value.mode) != nil {
		return target{}, invalidInput("Long Press config must contain only targetMode")
	}
	duration, ok := targeting.ReadInteger(call.Inputs["durationMs"], 1, constants.MaxAndroidLongPressDurationMS)
	if !ok {
		return target{}, invalidInput(fmt.Sprintf("durationMs must be an integer from 1 to %d", constants.MaxAndroidLongPressDurationMS))
	}
	value.command.DurationMS = duration

	xKey, yKey := "x", "y"
	limit := constants.MaxAndroidPixelCoordinate
	switch value.mode {
	case constants.AndroidTargetPixels:
	case constants.AndroidTargetPercent:
		xKey, yKey = "xPercent", "yPercent"
		limit = constants.MaxAndroidScreenPercent
	case constants.AndroidTargetSelector:
		if len(call.Inputs) != 2 || json.Unmarshal(call.Inputs["selector_id"], &value.selectorID) != nil ||
			!selectorIDPattern.MatchString(value.selectorID) {
			return target{}, invalidInput("SELECTOR_ID requires only selector_id as a full Android resource ID and durationMs")
		}
		return value, nil
	default:
		return target{}, invalidInput("Long Press targetMode must be ABSOLUTE_PIXELS, SCREEN_PERCENT, or SELECTOR_ID")
	}
	x, validX := targeting.ReadInteger(call.Inputs[xKey], 0, limit)
	y, validY := targeting.ReadInteger(call.Inputs[yKey], 0, limit)
	if len(call.Inputs) != 3 || !validX || !validY {
		return target{}, invalidInput(fmt.Sprintf("%s requires only integer %s and %s from 0 to %d and durationMs", value.mode, xKey, yKey, limit))
	}
	value.command.X, value.command.Y = x, y
	return value, nil
}

func (h *LongPress) resolvePoint(ctx context.Context, serial string, value target) (adb.LongPressCommand, error) {
	command := value.command
	switch value.mode {
	case constants.AndroidTargetPercent:
		size, err := h.device.DefaultDisplaySize(ctx, serial)
		if err != nil {
			return adb.LongPressCommand{}, xerror.WithOperation("read_display", err)
		}
		if size.Width <= 0 || size.Height <= 0 {
			return adb.LongPressCommand{}, xerror.WithOperation("read_display",
				xerror.CommandError{Code: constants.CommandFailed, Message: "cannot determine the current default display size"})
		}
		command.X = targeting.PercentPixel(command.X, size.Width)
		command.Y = targeting.PercentPixel(command.Y, size.Height)
	case constants.AndroidTargetSelector:
		point, err := h.device.SelectorPoint(ctx, serial, value.selectorID)
		if err != nil {
			return adb.LongPressCommand{}, xerror.WithOperation("resolve_selector", err)
		}
		command.X, command.Y = point.X, point.Y
	}
	return command, nil
}

func invalidInput(message string) error {
	return xerror.WithOperation("validate_inputs", xerror.CommandError{Code: constants.CommandInputUnsupported, Message: message})
}
