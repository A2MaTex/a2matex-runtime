package xerror

import "errors"

var (
	ErrRunAdmissionInvalid  = errors.New("invalid run admission request")
	ErrRunAdmissionContract = errors.New("run admission dependency contract failed")
)
