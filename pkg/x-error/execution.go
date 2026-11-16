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

var ErrExecutionStoreInUse = errors.New("execution store lock unavailable")

// ExecutionFailureCode projects classified execution errors into operational logs.
func ExecutionFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrCheckpointFatal):
		return "CHECKPOINT_FATAL"
	case errors.Is(err, ErrRecoveryIntegrity):
		return "RECOVERY_INTEGRITY_FAILED"
	case errors.Is(err, ErrExecutionCommitUnknown):
		return "EXECUTION_COMMIT_UNKNOWN"
	case errors.Is(err, ErrExecutionPersistence):
		return "EXECUTION_PERSISTENCE_FAILED"
	case errors.Is(err, ErrExecutionStaleState):
		return "EXECUTION_STALE_STATE"
	case errors.Is(err, ErrExecutionInvalidInput):
		return "EXECUTION_INVALID_INPUT"
	default:
		return ""
	}
}
