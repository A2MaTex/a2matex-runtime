package xerror

import "errors"

var (
	ErrProductRunConflict          = errors.New("execution request kind or root conflicts")
	ErrProductRunUnavailable       = errors.New("protected execution is unavailable")
	ErrProductAuthorizationExpired = errors.New("product authorization has expired")
	ErrProductRunParked            = errors.New("protected execution is parked")
)
