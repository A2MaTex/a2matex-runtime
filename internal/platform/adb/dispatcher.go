package adb

import (
	"context"
	"fmt"
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
		return xerror.WithOperation("check_attempt", err)
	}
	args, minimumDuration, err := commandArgs(command)
	if err != nil {
		return xerror.WithOperation("validate_command", err)
	}
	_, err = d.runDevice(ctx, serial, "dispatch_command", args, constants.ProcessOutputLimitBytes, minimumDuration)
	return err
}

// runDevice takes only adapter-built arguments and binds them to the selected device.
func (d *Dispatcher) runDevice(ctx context.Context, serial, operation string, args []string, stdoutLimit int, minimumDuration time.Duration) (processrunner.Result, error) {
	if err := ctx.Err(); err != nil {
		return processrunner.Result{}, xerror.WithOperation(operation, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok || !ValidSerial(serial) {
		return processrunner.Result{}, xerror.WithOperation("check_dispatch_context",
			xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"})
	}
	toolchain, err := d.resolver.Resolve(ctx)
	if contextErr := ctx.Err(); contextErr != nil {
		return processrunner.Result{}, xerror.WithOperation("resolve_adb", contextErr)
	}
	if err != nil {
		return processrunner.Result{}, &xerror.OperationError{
			Operation: "resolve_adb",
			// Resolver errors contain only classified candidate and probe details.
			Detail: err.Error(),
			Err:    xerror.CommandError{Code: constants.ADBUnavailable, Message: "ADB is unavailable", Retryable: true},
		}
	}
	remaining := time.Until(deadline)
	if remaining <= minimumDuration {
		detail := "attempt deadline elapsed before starting the ADB process"
		if remaining > 0 {
			detail = "remaining attempt time cannot cover the gesture duration"
		}
		return processrunner.Result{}, &xerror.OperationError{
			Operation: "check_command_deadline",
			Detail:    detail,
			Err:       context.DeadlineExceeded,
		}
	}

	args = append([]string{"-s", serial}, args...)
	var result processrunner.Result
	if stdoutLimit == constants.ProcessOutputLimitBytes {
		result = d.resolver.run(ctx, toolchain.Executable, args, remaining)
	} else {
		result = processrunner.RunWithStdoutLimit(ctx, toolchain.Executable, args, remaining, stdoutLimit)
	}
	err = dispatchError(result)
	if err == nil {
		return result, nil
	}
	detail := string(result.Outcome)
	if result.ExitCode != nil {
		detail = fmt.Sprintf("%s (exit code %d)", result.Outcome, *result.ExitCode)
	}
	return result, &xerror.OperationError{Operation: operation, Detail: detail, Err: err}
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
		args := swipeArgs(command.StartX, command.StartY, command.EndX, command.EndY, command.DurationMS)
		return args, time.Duration(command.DurationMS) * time.Millisecond, nil
	case LongPressCommand:
		if !command.Valid() {
			return nil, 0, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "long press coordinates or duration are out of range"}
		}
		args := swipeArgs(command.X, command.Y, command.X, command.Y, command.DurationMS)
		return args, time.Duration(command.DurationMS) * time.Millisecond, nil
	default:
		return nil, 0, xerror.CommandError{Code: constants.InternalError, Message: "device command has no adapter"}
	}
}

func swipeArgs(startX, startY, endX, endY, durationMS int) []string {
	return []string{"shell", "-T", "input", "swipe",
		strconv.Itoa(startX), strconv.Itoa(startY),
		strconv.Itoa(endX), strconv.Itoa(endY), strconv.Itoa(durationMS)}
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
