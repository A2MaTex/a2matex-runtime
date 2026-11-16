package tap

import (
	"context"
	"encoding/json"
	"math"
	"regexp"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type TapDevice interface {
	Dispatch(context.Context, string, adb.DeviceCommand) error
	DefaultDisplaySize(context.Context, string) (adb.DisplaySize, error)
	SelectorPoint(context.Context, string, string) (adb.TapCommand, error)
}

type Tap struct {
	device TapDevice
}

func NewTap(device TapDevice) *Tap {
	return &Tap{device: device}
}

var selectorIDPattern = regexp.MustCompile(constants.AndroidSelectorIDPattern)

// Run resolves the node inputs and returns an empty output after one successful Tap.
func (h *Tap) Run(ctx context.Context, serial, mode string, inputs map[string]json.RawMessage) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok || !adb.ValidSerial(serial) {
		return nil, xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"}
	}
	point, err := h.resolvePoint(ctx, serial, mode, inputs)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !point.Valid() {
		return nil, xerror.CommandError{Code: constants.CommandFailed, Message: "resolved Android point is out of range"}
	}
	if err := h.device.Dispatch(ctx, serial, point); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

func (h *Tap) resolvePoint(ctx context.Context, serial, mode string, inputs map[string]json.RawMessage) (adb.TapCommand, error) {
	switch mode {
	case constants.AndroidTargetPixels:
		point, ok := readPoint(inputs, "x", "y", constants.MaxAndroidPixelCoordinate)
		if ok {
			return point, nil
		}
	case constants.AndroidTargetPercent:
		point, ok := readPoint(inputs, "xPercent", "yPercent", constants.MaxAndroidScreenPercent)
		if ok {
			size, err := h.device.DefaultDisplaySize(ctx, serial)
			if err != nil {
				return adb.TapCommand{}, err
			}
			if size.Width <= 0 || size.Height <= 0 {
				return adb.TapCommand{}, xerror.CommandError{Code: constants.CommandFailed, Message: "cannot determine the current default display size"}
			}
			return adb.TapCommand{X: percentPixel(point.X, size.Width), Y: percentPixel(point.Y, size.Height)}, nil
		}
	case constants.AndroidTargetSelector:
		var id string
		if len(inputs) == 1 && json.Unmarshal(inputs["selector_id"], &id) == nil && selectorIDPattern.MatchString(id) {
			return h.device.SelectorPoint(ctx, serial, id)
		}
	}
	return adb.TapCommand{}, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "invalid Android Tap target"}
}

func readPoint(inputs map[string]json.RawMessage, xKey, yKey string, limit int) (adb.TapCommand, bool) {
	if len(inputs) != 2 {
		return adb.TapCommand{}, false
	}
	x, validX := readCoordinate(inputs[xKey], limit)
	y, validY := readCoordinate(inputs[yKey], limit)
	return adb.TapCommand{X: x, Y: y}, validX && validY
}

func readCoordinate(raw json.RawMessage, limit int) (int, bool) {
	var value *float64
	if json.Unmarshal(raw, &value) != nil || value == nil ||
		*value < 0 || *value > float64(limit) || math.Trunc(*value) != *value {
		return 0, false
	}
	return int(*value), true
}

func percentPixel(percent, dimension int) int {
	const scale = constants.MaxAndroidScreenPercent
	return int((int64(percent)*int64(dimension-1) + scale/2) / scale)
}
