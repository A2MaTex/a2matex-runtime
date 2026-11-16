package adb

import (
	"context"
	"strconv"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

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

func validPixelPoint(x, y int) bool {
	return x >= 0 && x <= constants.MaxAndroidPixelCoordinate &&
		y >= 0 && y <= constants.MaxAndroidPixelCoordinate
}

// Dispatcher executes a typed command against one selected device.
type Dispatcher struct {
	resolver *Resolver
}

func NewDispatcher(resolver *Resolver) *Dispatcher {
	return &Dispatcher{resolver: resolver}
}

// Dispatch executes the command once within the caller's attempt deadline.
func (d *Dispatcher) Dispatch(ctx context.Context, serial string, command DeviceCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	args, minimumDuration, err := commandArgs(command)
	if err != nil {
		return err
	}
	_, err = d.runDevice(ctx, serial, args, constants.ProcessOutputLimitBytes, minimumDuration)
	return err
}

// runDevice takes only adapter-built arguments and binds them to the selected device.
func (d *Dispatcher) runDevice(ctx context.Context, serial string, args []string, stdoutLimit int, minimumDuration time.Duration) (processrunner.Result, error) {
	if err := ctx.Err(); err != nil {
		return processrunner.Result{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || !ValidSerial(serial) {
		return processrunner.Result{}, xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"}
	}
	toolchain, err := d.resolver.Resolve(ctx)
	if contextErr := ctx.Err(); contextErr != nil {
		return processrunner.Result{}, contextErr
	}
	if err != nil {
		return processrunner.Result{}, xerror.CommandError{Code: constants.ADBUnavailable, Message: "ADB is unavailable", Retryable: true}
	}
	remaining := time.Until(deadline)
	if remaining <= minimumDuration {
		return processrunner.Result{}, context.DeadlineExceeded
	}

	args = append([]string{"-s", serial}, args...)
	var result processrunner.Result
	if stdoutLimit == constants.ProcessOutputLimitBytes {
		result = d.resolver.run(ctx, toolchain.Executable, args, remaining)
	} else {
		result = processrunner.RunWithStdoutLimit(ctx, toolchain.Executable, args, remaining, stdoutLimit)
	}
	return result, dispatchError(result)
}

func commandArgs(command DeviceCommand) ([]string, time.Duration, error) {
	switch command := command.(type) {
	case TapCommand:
		if !command.Valid() {
			return nil, 0, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "tap coordinates are out of range"}
		}
		return []string{"shell", "-T", "input", "tap", strconv.Itoa(command.X), strconv.Itoa(command.Y)}, 0, nil
	case SwipeCommand:
		if !command.Valid() {
			return nil, 0, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "swipe coordinates or duration are out of range"}
		}
		args := []string{"shell", "-T", "input", "swipe",
			strconv.Itoa(command.StartX), strconv.Itoa(command.StartY),
			strconv.Itoa(command.EndX), strconv.Itoa(command.EndY), strconv.Itoa(command.DurationMS)}
		return args, time.Duration(command.DurationMS) * time.Millisecond, nil
	default:
		return nil, 0, xerror.CommandError{Code: constants.InternalError, Message: "device command has no adapter"}
	}
}

func dispatchError(result processrunner.Result) error {
	switch result.Outcome {
	case processrunner.Succeeded:
		if result.ExitCode != nil && *result.ExitCode == 0 {
			return nil
		}
	case processrunner.InvalidRequest:
		return xerror.CommandError{Code: constants.InternalError, Message: "device command could not be invoked"}
	case processrunner.StartFailed:
		return xerror.CommandError{Code: constants.CommandStartFailed, Message: "device command could not start", Retryable: true}
	case processrunner.TimedOut:
		return context.DeadlineExceeded
	case processrunner.Cancelled:
		return context.Canceled
	}
	return xerror.CommandError{Code: constants.CommandFailed, Message: "device command failed"}
}
