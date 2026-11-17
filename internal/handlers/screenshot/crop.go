package screenshot

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func selectPNG(ctx context.Context, frame []byte, mode string, region image.Rectangle) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("decode_screenshot", err)
	}
	if len(frame) == 0 || len(frame) > constants.MaxScreenshotBytes {
		return nil, invalidFrame()
	}
	source := bytes.NewReader(frame)
	header, err := png.DecodeConfig(source)
	if err != nil || !execution.ValidScreenshotDimensions(header.Width, header.Height) {
		return nil, invalidFrame()
	}
	if mode == constants.ScreenshotFullScreen {
		// D5 does the full PNG validation before staging the unchanged bytes.
		return frame, nil
	}
	if mode == constants.AndroidTargetPercent {
		const scale = constants.MaxAndroidScreenPercent
		region.Min.X = region.Min.X * header.Width / scale
		region.Min.Y = region.Min.Y * header.Height / scale
		region.Max.X = (region.Max.X*header.Width + scale - 1) / scale
		region.Max.Y = (region.Max.Y*header.Height + scale - 1) / scale
	}
	if !region.In(image.Rect(0, 0, header.Width, header.Height)) || region.Empty() {
		return nil, screenshotError("resolve_screenshot_region", "region_outside_frame", constants.CommandInputUnsupported,
			"Screenshot pixel region falls outside the captured frame")
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("decode_screenshot", err)
	}
	source.Reset(frame)
	decoded, err := png.Decode(source)
	if err != nil || source.Len() != 0 {
		return nil, invalidFrame()
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("encode_screenshot_region", err)
	}

	// All standard PNG decoder image types offer a SubImage view of the pixels.
	croppable, ok := decoded.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, screenshotError("encode_screenshot_region", "unsupported_image", constants.CommandFailed,
			"Android screenshot format cannot be cropped")
	}
	writer := pngBuffer{ctx: ctx}
	encodeErr := png.Encode(&writer, croppable.SubImage(region.Add(decoded.Bounds().Min)))
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("encode_screenshot_region", err)
	}
	switch {
	case errors.Is(encodeErr, errPNGTooLarge):
		return nil, screenshotError("encode_screenshot_region", "crop_too_large", constants.CommandFailed,
			"Cropped screenshot exceeds the 16 MiB artifact limit")
	case encodeErr != nil:
		return nil, screenshotError("encode_screenshot_region", "encode_png_failed", constants.CommandFailed,
			"Could not encode the screenshot region")
	}
	return writer.data.Bytes(), nil
}

func invalidFrame() error {
	return screenshotError("decode_screenshot", "invalid_frame", constants.CommandFailed,
		"Android returned an invalid or oversized screenshot")
}

var errPNGTooLarge = errors.New("encoded screenshot exceeds its byte limit")

type pngBuffer struct {
	ctx  context.Context
	data bytes.Buffer
}

func (b *pngBuffer) Write(data []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > constants.MaxScreenshotBytes-b.data.Len() {
		return 0, errPNGTooLarge
	}
	return b.data.Write(data)
}
