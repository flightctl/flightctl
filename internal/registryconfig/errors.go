package registryconfig

// inputError keeps parser and decoder input out of messages while preserving the cause.
type inputError struct {
	message string
	cause   error
}

func (e *inputError) Error() string {
	return e.message
}

func (e *inputError) Unwrap() error {
	return e.cause
}
