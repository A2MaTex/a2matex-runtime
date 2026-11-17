// Package screenshot captures the current Android frame and stages the selected region.
package screenshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/targeting"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Device interface {
	CaptureScreenshot(context.Context, string) ([]byte, error)
}

type Screenshot struct {
	device Device
}

func NewScreenshot(device Device) *Screenshot {
	return &Screenshot{device: device}
}

func (h *Screenshot) Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if _, ok := ctx.Deadline(); !ok || !adb.ValidSerial(call.ADBSerial) || call.StageScreenshot == nil {
		return nil, screenshotError("check_screenshot_context", "invalid_invocation", constants.InternalError,
			"Screenshot requires an execution device, attempt deadline, and artifact store")
	}
	mode, region, err := readRegion(call)
	if err != nil {
		return nil, screenshotError("validate_screenshot_inputs", "invalid_region_inputs", constants.CommandInputUnsupported, err.Error())
	}
	frame, err := h.device.CaptureScreenshot(ctx, call.ADBSerial)
	if err != nil {
		return nil, xerror.WithOperation("capture_screenshot", err)
	}
	selected, err := selectPNG(ctx, frame, mode, region)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("stage_screenshot", err)
	}
	id, err := call.StageScreenshot(ctx, selected)
	switch {
	case ctx.Err() != nil:
		return nil, xerror.WithOperation("stage_screenshot", ctx.Err())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, xerror.WithOperation("stage_screenshot", err)
	case errors.Is(err, xerror.ErrArtifactImageInvalid):
		return nil, screenshotError("stage_screenshot", "invalid_png", constants.CommandFailed, "Android screenshot is not a valid PNG")
	case err != nil:
		return nil, screenshotError("stage_screenshot", "artifact_write_failed", constants.PersistenceFailed, "Could not save the Android screenshot")
	case !execution.ValidID(id):
		return nil, screenshotError("stage_screenshot", "invalid_artifact_id", constants.InternalError, "Screenshot storage returned an invalid artifact ID")
	}
	return map[string]any{"artifactId": id}, nil
}

func readRegion(call registry.Invocation) (string, image.Rectangle, error) {
	var mode string
	if len(call.Config) != 1 || json.Unmarshal(call.Config["targetMode"], &mode) != nil {
		return "", image.Rectangle{}, errors.New("Screenshot config must contain only targetMode")
	}
	keys := [4]string{"startX", "startY", "endX", "endY"}
	limit := constants.MaxScreenshotDimension
	switch mode {
	case constants.ScreenshotFullScreen:
		if len(call.Inputs) != 0 {
			return "", image.Rectangle{}, errors.New("FULL_SCREEN accepts no coordinates")
		}
		return mode, image.Rectangle{}, nil
	case constants.AndroidTargetPixels:
	case constants.AndroidTargetPercent:
		keys = [4]string{"startXPercent", "startYPercent", "endXPercent", "endYPercent"}
		limit = constants.MaxAndroidScreenPercent
	default:
		return "", image.Rectangle{}, errors.New("Screenshot targetMode must be FULL_SCREEN, ABSOLUTE_PIXELS, or SCREEN_PERCENT")
	}
	if len(call.Inputs) != len(keys) {
		return "", image.Rectangle{}, errors.New("Screenshot region requires exactly four coordinates")
	}
	var coordinates [4]int
	for i, key := range keys {
		value, ok := targeting.ReadInteger(call.Inputs[key], 0, limit)
		if !ok {
			return "", image.Rectangle{}, fmt.Errorf("%s must be an integer from 0 to %d", key, limit)
		}
		coordinates[i] = value
	}
	if coordinates[0] >= coordinates[2] || coordinates[1] >= coordinates[3] {
		return "", image.Rectangle{}, errors.New("Screenshot top-left point must come before the bottom-right point on both axes")
	}
	return mode, image.Rect(coordinates[0], coordinates[1], coordinates[2], coordinates[3]), nil
}

func screenshotError(operation, reason, code, message string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason,
		Err: xerror.CommandError{Code: code, Message: message}}
}
