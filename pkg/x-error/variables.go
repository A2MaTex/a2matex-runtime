package xerror

import "errors"

var (
	ErrVariableInvalid     = errors.New("scoped variable request or checkpoint confirmation is invalid")
	ErrVariableUnavailable = errors.New("scoped variable store is closed or fenced")
)
