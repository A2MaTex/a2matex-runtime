package xerror

import "errors"

var (
	ErrProductSessionInvalid       = errors.New("product session request is invalid")
	ErrProductSessionBusy          = errors.New("product session preparation is busy")
	ErrProductSessionConflict      = errors.New("product session request identity conflicts")
	ErrProductSessionUnavailable   = errors.New("product authorization session is unavailable")
	ErrProductSessionConfiguration = errors.New("product session dependency contract is invalid")
	ErrProductSessionKeyGeneration = errors.New("product session key could not be generated")
)
