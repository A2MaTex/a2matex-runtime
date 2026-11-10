package xerror

import "errors"

var (
	ErrExecutionNotFound      = errors.New("execution record not found")
	ErrExecutionInvalidInput  = errors.New("invalid execution repository input")
	ErrExecutionPersistence   = errors.New("execution persistence failed")
	ErrExecutionCommitUnknown = errors.New("execution commit outcome unknown")
	ErrExecutionStaleState    = errors.New("execution state no longer matches")
	ErrEventCursorExpired     = errors.New("execution event cursor expired")
	ErrEventCursorFuture      = errors.New("execution event cursor is ahead of committed history")
	ErrCheckpointFatal        = errors.New("fatal execution checkpoint failure")
	ErrCheckpointStopped      = errors.New("execution checkpoint service stopped")
	ErrRecoveryIntegrity      = errors.New("inconsistent execution state during startup recovery")
)
