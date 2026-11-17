package xerror

import "errors"

var (
	ErrRuntimeIRPayloadTooLarge = errors.New("workflow admission content exceeds its byte limit")
	ErrRuntimeIRDependencies    = errors.New("workflow admission requires installed definitions and executable handlers")
)
