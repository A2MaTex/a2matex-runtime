package swipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/targeting"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type SwipeDevice interface {
	Dispatch(context.Context, string, adb.DeviceCommand) error
	DefaultDisplaySize(context.Context, string) (adb.DisplaySize, error)
}

type Swipe struct {
	device SwipeDevice
}

func NewSwipe(device SwipeDevice) *Swipe {
	return &Swipe{device: device}
}

// Invoke resolves the two endpoints and dispatches one swipe on the selected device.
func (h *Swipe) Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if _, ok := ctx.Deadline(); !ok || !adb.ValidSerial(call.ADBSerial) {
		return nil, xerror.WithOperation("check_dispatch_context",
			xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"})
	}
	command, percent, err := readTarget(call)
	if err != nil {
		return nil, xerror.WithOperation("validate_inputs",
			xerror.CommandError{Code: constants.CommandInputUnsupported, Message: err.Error()})
	}
	if percent {
		size, err := h.device.DefaultDisplaySize(ctx, call.ADBSerial)
		if err != nil {
			return nil, xerror.WithOperation("read_display", err)
		}
		if size.Width <= 0 || size.Height <= 0 {
			return nil, xerror.WithOperation("read_display",
				xerror.CommandError{Code: constants.CommandFailed, Message: "cannot determine the current default display size"})
		}
		command.StartX = targeting.PercentPixel(command.StartX, size.Width)
		command.StartY = targeting.PercentPixel(command.StartY, size.Height)
		command.EndX = targeting.PercentPixel(command.EndX, size.Width)
		command.EndY = targeting.PercentPixel(command.EndY, size.Height)
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if !command.Valid() {
		return nil, xerror.WithOperation("resolve_swipe_points",
			xerror.CommandError{Code: constants.CommandFailed, Message: "resolved Android Swipe points are out of range"})
	}
	if err := h.device.Dispatch(ctx, call.ADBSerial, command); err != nil {
		return nil, xerror.WithOperation("dispatch_command", err)
	}
	return map[string]any{}, nil
}

func readTarget(call registry.Invocation) (adb.SwipeCommand, bool, error) {
	var command adb.SwipeCommand
	var mode string
	if len(call.Config) != 1 || json.Unmarshal(call.Config["targetMode"], &mode) != nil {
		return command, false, errors.New("Swipe config must contain only targetMode")
	}

	keys := [4]string{"startX", "startY", "endX", "endY"}
	limit := constants.MaxAndroidPixelCoordinate
	switch mode {
	case constants.AndroidTargetPixels:
	case constants.AndroidTargetPercent:
		keys = [4]string{"startXPercent", "startYPercent", "endXPercent", "endYPercent"}
		limit = constants.MaxAndroidScreenPercent
	default:
		return command, false, errors.New("Swipe targetMode must be ABSOLUTE_PIXELS or SCREEN_PERCENT")
	}

	duration, ok := targeting.ReadInteger(call.Inputs["durationMs"], 1, constants.MaxAndroidSwipeDurationMS)
	if !ok {
		return command, false, fmt.Errorf("durationMs must be an integer from 1 to %d", constants.MaxAndroidSwipeDurationMS)
	}
	var coordinates [4]int
	for i, key := range keys {
		raw, present := call.Inputs[key]
		if !present {
			return command, false, fmt.Errorf("%s requires %s and durationMs", mode, strings.Join(keys[:], ", "))
		}
		value, ok := targeting.ReadInteger(raw, 0, limit)
		if !ok {
			return command, false, fmt.Errorf("%s must be an integer from 0 to %d", key, limit)
		}
		coordinates[i] = value
	}
	if len(call.Inputs) != len(keys)+1 {
		return command, false, fmt.Errorf("%s accepts only %s and durationMs", mode, strings.Join(keys[:], ", "))
	}
	command = adb.SwipeCommand{
		StartX: coordinates[0], StartY: coordinates[1],
		EndX: coordinates[2], EndY: coordinates[3], DurationMS: duration,
	}
	return command, mode == constants.AndroidTargetPercent, nil
}
