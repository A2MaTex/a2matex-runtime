package xerror

// CommandError carries a sanitized A6 execution failure.
type CommandError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e CommandError) Error() string {
	return e.Message
}
