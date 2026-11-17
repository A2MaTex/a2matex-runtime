package xerror

import "errors"

var ErrInputBindingDependencies = errors.New("input binding resolution requires installed node definitions")

// InputBindingError identifies a known destination without exposing its source or value.
// Only the wrapped CommandError belongs in an A6 error envelope.
type InputBindingError struct {
	Field string
	Err   error
}

func (e *InputBindingError) Error() string { return e.Err.Error() }

func (e *InputBindingError) Unwrap() error { return e.Err }
