package screenshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const testArtifactID = "11111111-1111-4111-8111-111111111111"

type captureFunc func(context.Context, string) ([]byte, error)

func (f captureFunc) CaptureScreenshot(ctx context.Context, serial string) ([]byte, error) {
	return f(ctx, serial)
}

func TestScreenshotModesStageExpectedPixels(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const serial = "emulator-5554"
	frame, original := testPNG(t, 7, 5)
	var captures int
	handler := NewScreenshot(captureFunc(func(gotCtx context.Context, gotSerial string) ([]byte, error) {
		if gotCtx != ctx || gotSerial != serial {
			t.Fatal("capture changed the attempt context or device serial")
		}
		captures++
		return frame, nil
	}))
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(catalog, []registry.Registration{
		{Type: constants.ScreenshotNodeType, RuntimeHandler: constants.ScreenshotNodeType, Run: handler.Invoke},
	})
	if err != nil {
		t.Fatal(err)
	}
	invoke, ok := reg.Lookup(constants.ScreenshotNodeType, constants.ScreenshotNodeType)
	if !ok {
		t.Fatal("Screenshot registration missing")
	}
	for _, test := range []struct {
		name, mode, inputs string
		bounds             image.Rectangle
	}{
		{"full", constants.ScreenshotFullScreen, "{}", image.Rect(0, 0, 7, 5)},
		{"pixels", constants.AndroidTargetPixels, `{"startX":1,"startY":1,"endX":3,"endY":2}`, image.Rect(1, 1, 3, 2)},
		{"percent", constants.AndroidTargetPercent, `{"startXPercent":20,"startYPercent":20,"endXPercent":40,"endYPercent":30}`, image.Rect(1, 1, 3, 2)},
		{"percent all edges", constants.AndroidTargetPercent, `{"startXPercent":0,"startYPercent":0,"endXPercent":100,"endYPercent":100}`, image.Rect(0, 0, 7, 5)},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, stages := captures, 0
			call := invocation(t, test.mode, test.inputs)
			call.ADBSerial = serial
			call.StageScreenshot = func(gotCtx context.Context, data []byte) (string, error) {
				stages++
				if gotCtx != ctx {
					t.Fatal("staging changed the attempt context")
				}
				if test.mode == constants.ScreenshotFullScreen && !bytes.Equal(data, frame) {
					t.Fatal("full-screen mode must forward the original PNG")
				}
				selected, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if selected.Bounds().Size() != test.bounds.Size() {
					t.Fatalf("crop size = %v, want %v", selected.Bounds().Size(), test.bounds.Size())
				}
				for y := 0; y < test.bounds.Dy(); y++ {
					for x := 0; x < test.bounds.Dx(); x++ {
						got := color.NRGBAModel.Convert(selected.At(x, y))
						want := color.NRGBAModel.Convert(original.At(test.bounds.Min.X+x, test.bounds.Min.Y+y))
						if got != want {
							t.Fatalf("incorrect pixel at %d,%d", x, y)
						}
					}
				}
				return testArtifactID, nil
			}
			output, err := invoke(ctx, call)
			if err != nil || len(output) != 1 || output["artifactId"] != testArtifactID || captures != before+1 || stages != 1 {
				t.Fatalf("output %+v, error %v, captures %d, stages %d", output, err, captures-before, stages)
			}
		})
	}
}

func TestScreenshotFailureBoundaries(t *testing.T) {
	frame, _ := testPNG(t, 7, 5)
	oversized, _ := testPNG(t, constants.MaxScreenshotDimension+1, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const region = `{"startX":1,"startY":1,"endX":3,"endY":2}`
	for _, test := range []struct {
		name, mode, inputs, code, reason string
		data                             []byte
		stageError                       error
		captures, stages                 int
	}{
		{"mode missing", "", "{}", constants.CommandInputUnsupported, "invalid_region_inputs", frame, nil, 0, 0},
		{"full with coordinates", constants.ScreenshotFullScreen, region, constants.CommandInputUnsupported, "invalid_region_inputs", frame, nil, 0, 0},
		{"reversed", constants.AndroidTargetPixels, `{"startX":3,"startY":1,"endX":2,"endY":2}`, constants.CommandInputUnsupported, "invalid_region_inputs", frame, nil, 0, 0},
		{"outside frame", constants.AndroidTargetPixels, `{"startX":1,"startY":1,"endX":8,"endY":2}`, constants.CommandInputUnsupported, "region_outside_frame", frame, nil, 1, 0},
		{"truncated crop source", constants.AndroidTargetPixels, region, constants.CommandFailed, "invalid_frame", frame[:len(frame)-10], nil, 1, 0},
		{"over dimension limit", constants.ScreenshotFullScreen, "{}", constants.CommandFailed, "invalid_frame", oversized, nil, 1, 0},
		{"invalid full PNG at storage", constants.ScreenshotFullScreen, "{}", constants.CommandFailed, "invalid_png", frame, xerror.ErrArtifactImageInvalid, 1, 1},
		{"storage fails", constants.ScreenshotFullScreen, "{}", constants.PersistenceFailed, "artifact_write_failed", frame, xerror.ErrExecutionPersistence, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var captures, stages int
			handler := NewScreenshot(captureFunc(func(context.Context, string) ([]byte, error) {
				captures++
				return test.data, nil
			}))
			call := invocation(t, test.mode, test.inputs)
			call.StageScreenshot = func(context.Context, []byte) (string, error) {
				stages++
				return "", test.stageError
			}
			output, err := handler.Invoke(ctx, call)
			var failure xerror.CommandError
			var operation *xerror.OperationError
			if output != nil || !errors.As(err, &failure) || failure.Code != test.code || !errors.As(err, &operation) ||
				operation.Detail != test.reason || failure.Retryable || captures != test.captures || stages != test.stages {
				t.Fatalf("output %+v, error %v, captures %d, stages %d", output, err, captures, stages)
			}
		})
	}
}

func TestScreenshotDoesNotPublishWhenStagingCancels(t *testing.T) {
	frame, _ := testPNG(t, 7, 5)
	handler := NewScreenshot(captureFunc(func(context.Context, string) ([]byte, error) { return frame, nil }))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	call := invocation(t, constants.ScreenshotFullScreen, "{}")
	call.StageScreenshot = func(context.Context, []byte) (string, error) {
		cancel()
		return testArtifactID, nil
	}
	if output, err := handler.Invoke(ctx, call); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled invocation output %+v, error %v", output, err)
	}
}

func TestCroppedPNGWriterLimit(t *testing.T) {
	writer := pngBuffer{ctx: t.Context()}
	if _, err := writer.Write(make([]byte, constants.MaxScreenshotBytes)); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write([]byte{0}); n != 0 || !errors.Is(err, errPNGTooLarge) {
		t.Fatalf("write = %d, error %v", n, err)
	}
}

func invocation(t *testing.T, mode, inputs string) registry.Invocation {
	t.Helper()
	call := registry.Invocation{ADBSerial: "emulator-5554"}
	if mode != "" {
		call.Config = map[string]json.RawMessage{"targetMode": json.RawMessage(`"` + mode + `"`)}
	}
	if err := json.Unmarshal([]byte(inputs), &call.Inputs); err != nil {
		t.Fatal(err)
	}
	return call
}

func testPNG(t *testing.T, width, height int) ([]byte, *image.NRGBA) {
	t.Helper()
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pixels.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, pixels); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), pixels
}
