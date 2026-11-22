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
	if input, ok := command.(InputTextCommand); ok && input.Text == "" {
		_, err := checkDispatchContext(ctx, serial)
		return err
	}
	args, minimumDuration, err := commandArgs(command)
	if err != nil {
		return xerror.WithOperation("validate_command", err)
	}
	result, err := d.runDevice(ctx, serial, "dispatch_command", args, constants.ProcessOutputLimitBytes, minimumDuration)
	if err != nil {
		return err
	}
	// fmt.Printf("stdout: %s\n", result.Stdout)
	// fmt.Printf("stderr: %s\n", result.Stderr)
	// fmt.Printf("outcome: %d\n", result.Outcome)
	if launch, ok := command.(LaunchAppCommand); ok {
		err := parseLaunchResult(launch.PackageName, result)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return xerror.WithOperation("parse_launch_result", ctxErr)
		}
		return err
	}
	return nil
}

// runDevice takes only adapter-built arguments and binds them to the selected device.
func (d *Dispatcher) runDevice(ctx context.Context, serial, operation string, args []string, stdoutLimit int, minimumDuration time.Duration) (processrunner.Result, error) {
	if err := ctx.Err(); err != nil {
		return processrunner.Result{}, xerror.WithOperation(operation, err)
	}
	deadline, err := checkDispatchContext(ctx, serial)
	if err != nil {
		return processrunner.Result{}, err
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
	if contextErr := ctx.Err(); contextErr != nil {
		return result, xerror.WithOperation(operation, contextErr)
	}
	err = dispatchError(result)
	if err == nil {
		return result, nil
	}
	if result.Outcome == processrunner.NonzeroExit {
		err = d.classifyTransportFailure(ctx, serial, err)
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
	case PressBackCommand:
		return []string{"shell", "-T", "input", "keyevent", "KEYCODE_BACK"}, 0, nil
	case PressHomeCommand:
		return []string{"shell", "-T", "input", "keyevent", "KEYCODE_HOME"}, 0, nil
	case LaunchAppCommand:
		if !command.Valid() {
			return nil, 0, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "invalid Android package name"}
		}
		return launchArgs(command.PackageName), 0, nil
	case InputTextCommand:
		if err := command.Validate(); err != nil {
			return nil, 0, err
		}
		return inputTextArgs(command.Text), 0, nil
	default:
		return nil, 0, xerror.CommandError{Code: constants.InternalError, Message: "device command has no adapter"}
	}
}

func checkDispatchContext(ctx context.Context, serial string) (time.Time, error) {
	deadline, ok := ctx.Deadline()
	if !ok || !ValidSerial(serial) {
		return time.Time{}, xerror.WithOperation("check_dispatch_context",
			xerror.CommandError{Code: constants.InternalError, Message: "invalid device dispatch request"})
	}
	return deadline, nil
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
	case processrunner.CommandLineTooLong:
		return xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "The encoded ADB command is too long for Windows. Use shorter text"}
	case processrunner.StartFailed:
		return xerror.CommandError{Code: constants.CommandStartFailed, Message: "device command could not start", Retryable: true}
	case processrunner.TimedOut:
		return context.DeadlineExceeded
	case processrunner.Cancelled:
		return context.Canceled
	}
	return xerror.CommandError{Code: constants.CommandFailed, Message: "device command failed"}
}
