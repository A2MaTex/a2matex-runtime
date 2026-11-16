package xerror

import "errors"

// OperationError carries log-safe context separately from the classified error.
type OperationError struct {
	Operation string
	Detail    string
	Err       error
}

func (e *OperationError) Error() string {
	return e.Operation + ": " + e.Err.Error()
}

func (e *OperationError) Unwrap() error {
	return e.Err
}

// WithOperation supplies context without replacing a more specific operation.
func WithOperation(operation string, err error) error {
	if err == nil {
		return nil
	}
	var failure *OperationError
	if errors.As(err, &failure) {
		return err
	}
	return &OperationError{Operation: operation, Err: err}
}
