package adb

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// CaptureScreenshot captures one binary PNG frame from the selected device.
func (d *Dispatcher) CaptureScreenshot(ctx context.Context, serial string) ([]byte, error) {
	result, err := d.runDevice(ctx, serial, "capture_screenshot",
		[]string{"exec-out", "screencap", "-p"}, constants.MaxScreenshotBytes, 0)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("capture_screenshot", err)
	}
	if result.StdoutTruncated {
		return nil, screenshotCaptureError("capture_too_large", "Android screenshot exceeds the 16 MiB capture limit")
	}
	if len(result.Stdout) == 0 {
		return nil, screenshotCaptureError("empty_capture", "Android returned an empty screenshot")
	}
	return result.Stdout, nil
}

func screenshotCaptureError(reason, message string) error {
	return &xerror.OperationError{Operation: "capture_screenshot", Detail: reason,
		Err: xerror.CommandError{Code: constants.CommandFailed, Message: message}}
}
