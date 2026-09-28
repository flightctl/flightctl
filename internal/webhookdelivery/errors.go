package webhookdelivery

import (
	"errors"
	"fmt"
)

// NonRetryableError wraps an error that must not be retried by Deliver.
type NonRetryableError struct {
	Err error
}

func (e *NonRetryableError) Error() string { return e.Err.Error() }
func (e *NonRetryableError) Unwrap() error { return e.Err }

// NonRetryable returns a NonRetryableError wrapping err.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &NonRetryableError{Err: err}
}

// NonRetryablef returns a NonRetryableError with a formatted message.
func NonRetryablef(format string, args ...any) error {
	return &NonRetryableError{Err: fmt.Errorf(format, args...)}
}

// IsNonRetryable reports whether err (or any wrapped error) is non-retryable.
func IsNonRetryable(err error) bool {
	var nre *NonRetryableError
	return errors.As(err, &nre)
}
