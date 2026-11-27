package xerror

import "errors"

var (
	ErrExecutionLeaseInvalid       = errors.New("product authorization could not be accepted")
	ErrExecutionLeaseConfiguration = errors.New("execution lease verifier configuration is invalid")
)
