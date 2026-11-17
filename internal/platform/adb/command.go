package adb

import "github.com/A2MaTex/a2matex-runtime/pkg/constants"

// DeviceCommand is an internal A4 command with no device or executable fields.
type DeviceCommand interface {
	deviceCommand()
}

// TapCommand carries resolved Android display coordinates.
type TapCommand struct {
	X int
	Y int
}

func (TapCommand) deviceCommand() {}

func (c TapCommand) Valid() bool {
	return validPixelPoint(c.X, c.Y)
}

// SwipeCommand carries two resolved Android display points and a gesture duration.
type SwipeCommand struct {
	StartX     int
	StartY     int
	EndX       int
	EndY       int
	DurationMS int
}

func (SwipeCommand) deviceCommand() {}

func (c SwipeCommand) Valid() bool {
	return validPixelPoint(c.StartX, c.StartY) && validPixelPoint(c.EndX, c.EndY) &&
		c.DurationMS > 0 && c.DurationMS <= constants.MaxAndroidSwipeDurationMS
}

// LongPressCommand holds one resolved Android display point for a fixed duration.
type LongPressCommand struct {
	X          int
	Y          int
	DurationMS int
}

func (LongPressCommand) deviceCommand() {}

func (c LongPressCommand) Valid() bool {
	return validPixelPoint(c.X, c.Y) && c.DurationMS > 0 && c.DurationMS <= constants.MaxAndroidLongPressDurationMS
}

// PressBackCommand and PressHomeCommand select fixed Android navigation keys.
type PressBackCommand struct{}
type PressHomeCommand struct{}

func (PressBackCommand) deviceCommand() {}
func (PressHomeCommand) deviceCommand() {}

// InputTextCommand carries the exact text for the currently focused field.
type InputTextCommand struct {
	Text string
}

func (InputTextCommand) deviceCommand() {}

func validPixelPoint(x, y int) bool {
	return x >= 0 && x <= constants.MaxAndroidPixelCoordinate &&
		y >= 0 && y <= constants.MaxAndroidPixelCoordinate
}
