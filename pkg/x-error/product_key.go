package xerror

import "errors"

var (
	ErrProductKeyEnvelopeInvalid       = errors.New("product authorization could not be accepted")
	ErrProductKeyEnvelopeConfiguration = errors.New("product key envelope caller contract is invalid")
)
