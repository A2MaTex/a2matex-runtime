// Package outcomes projects attempt evidence into safe execution results.
package outcomes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Result is an internal proposal, not a durable state or an API payload.
// Stop prohibits ordinary routing and retries; it does not authorize a write.
type Result struct {
	Status string
	Output json.RawMessage
	Error  *xerror.CommandError
	Stop   bool
}

// Normalize consumes G6 evidence and an optional error from another attempt stage.
// checkedOutput must be the actual G3 snapshot; this function does not revalidate it.
func Normalize(ctx context.Context, observation invocation.Observation, checkedOutput json.RawMessage, stageError error) Result {
	if persistenceFailure(stageError) || persistenceFailure(observation.HandlerError) {
		return stopped(constants.PersistenceFailed)
	}
	parentError := ctx.Err()
	if deadline, ok := ctx.Deadline(); parentError == nil && ok && !time.Now().Before(deadline) {
		parentError = context.DeadlineExceeded
	}
	stageCommand, validStage := classify(stageError)
	handlerCommand, validHandler := classify(observation.HandlerError)
	if !validStage || !validHandler || (stageError != nil && observation.HandlerError != nil) {
		return stopped(constants.InternalError)
	}
	for _, signal := range [...]error{parentError, observation.ParentError, observation.AttemptError} {
		if invalidSignal(signal) {
			return stopped(constants.InternalError)
		}
	}
	command, source := stageCommand, stageError
	if source == nil {
		command, source = handlerCommand, observation.HandlerError
	}
	switch command.Code {
	case constants.PersistenceFailed, constants.InternalError:
		return stopped(command.Code)
	}

	if errors.Is(parentError, context.Canceled) || errors.Is(observation.ParentError, context.Canceled) {
		return Result{Status: constants.ExecutionCancelled, Stop: true}
	}
	if errors.Is(parentError, context.DeadlineExceeded) || errors.Is(observation.ParentError, context.DeadlineExceeded) {
		return timedOut(true)
	}
	if errors.Is(observation.AttemptError, context.Canceled) || errors.Is(source, context.Canceled) {
		// An isolated command cancellation does not establish owner cancellation.
		return stopped(constants.InternalError)
	}
	if errors.Is(observation.AttemptError, context.DeadlineExceeded) || errors.Is(source, context.DeadlineExceeded) {
		return timedOut(false)
	}
	if command.Code != "" {
		command.Retryable = command.Retryable &&
			(command.Code == constants.CommandStartFailed || command.Code == constants.ADBUnavailable)
		result := Result{Status: constants.ExecutionFailed, Error: &command}
		switch command.Code {
		case constants.CommandTimedOut, constants.NodeTimedOut:
			result.Status = constants.ExecutionTimedOut
		case constants.DeviceNotFound, constants.DeviceUnauthorized, constants.DeviceOffline,
			constants.DeviceNotReady, constants.DeviceDisconnected:
			result.Stop = true
		}
		return result
	}
	if observation.Output == nil || len(checkedOutput) == 0 || len(checkedOutput) > constants.MaxEventBytes {
		return stopped(constants.InternalError)
	}
	return Result{Status: constants.ExecutionSucceeded, Output: bytes.Clone(checkedOutput)}
}

func persistenceFailure(err error) bool {
	if err == nil {
		return false
	}
	for _, sentinel := range [...]error{
		xerror.ErrExecutionPersistence, xerror.ErrExecutionCommitUnknown,
		xerror.ErrCheckpointFatal, xerror.ErrCheckpointStopped,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// classify accepts single-cause chains from the approved Runtime producers.
// It never formats an error: wrapped text may contain protected data.
func classify(err error) (xerror.CommandError, bool) {
	if err == nil {
		return xerror.CommandError{}, true
	}
	if multipleCauses(err) {
		return xerror.CommandError{}, false
	}
	for _, sentinel := range [...]error{
		xerror.ErrInvocationInvalid, xerror.ErrRoutingInvalid,
		xerror.ErrInputBindingDependencies, xerror.ErrOutputValidationDependencies,
		xerror.ErrExecutionInvalidInput, xerror.ErrExecutionStaleState, xerror.ErrRecoveryIntegrity,
	} {
		if errors.Is(err, sentinel) {
			return xerror.CommandError{}, false
		}
	}
	var command xerror.CommandError
	if !errors.As(err, &command) {
		var pointer *xerror.CommandError
		if !errors.As(err, &pointer) {
			return command, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		}
		if pointer == nil {
			return command, false
		}
		command = *pointer
	}
	message := command.Message
	if message == "" || len(message) > constants.MaxRuntimeErrorMessageChars*utf8.UTFMax ||
		!utf8.ValidString(message) || utf8.RuneCountInString(message) > constants.MaxRuntimeErrorMessageChars {
		return xerror.CommandError{}, false
	}
	switch command.Code {
	case constants.CommandStartFailed, constants.CommandInputUnsupported, constants.CommandFailed,
		constants.CommandTimedOut, constants.NodeTimedOut, constants.NodeOutputInvalid, constants.ADBUnavailable,
		constants.DeviceNotFound, constants.DeviceUnauthorized, constants.DeviceOffline,
		constants.DeviceNotReady, constants.DeviceDisconnected, constants.InternalError, constants.PersistenceFailed:
		return command, true
	default:
		return xerror.CommandError{}, false
	}
}

func multipleCauses(err error) bool {
	for err != nil {
		if _, multiple := err.(interface{ Unwrap() []error }); multiple {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func invalidSignal(err error) bool {
	if err == nil {
		return false
	}
	return multipleCauses(err) || errors.Is(err, context.Canceled) == errors.Is(err, context.DeadlineExceeded)
}

func stopped(code string) Result {
	message := constants.OutcomeInternalErrorMessage
	if code == constants.PersistenceFailed {
		message = constants.PersistenceFailedMessage
	}
	return Result{Status: constants.ExecutionFailed, Stop: true,
		Error: &xerror.CommandError{Code: code, Message: message}}
}

func timedOut(stop bool) Result {
	return Result{Status: constants.ExecutionTimedOut, Stop: stop,
		Error: &xerror.CommandError{Code: constants.NodeTimedOut, Message: constants.NodeTimedOutMessage}}
}
