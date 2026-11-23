package xerror

import "errors"

var (
	ErrExecutionControlState     = errors.New("execution control transition is not eligible")
	ErrExecutionLifecycleInvalid = errors.New("execution lifecycle state is invalid")
	ErrExecutionStopping         = errors.New("execution is stopping")
	ErrOwnerShutdownRequested    = errors.New("owner requested runtime shutdown")
	ErrOwnerDisconnected         = errors.New("runtime owner disconnected")
	ErrOwnerProtocol             = errors.New("runtime owner protocol failed")
)
