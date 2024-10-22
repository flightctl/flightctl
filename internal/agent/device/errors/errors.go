package errors

import (
	"context"
	"errors"
	"fmt"
	"net"
)

var (
	ErrRetryable = errors.New("retryable error")
	ErrNoRetry   = errors.New("no retry")

	// bootstrap
	ErrEnrollmentRequestFailed = fmt.Errorf("enrollment request failed")
	ErrEnrollmentRequestDenied = fmt.Errorf("enrollment request denied")

	// applications
	ErrAppNameRequired        = errors.New("application name is required")
	ErrAppNotFound            = errors.New("application not found")
	ErrUnsupportedAppType     = errors.New("unsupported application type")
	ErrParseAppType           = errors.New("failed to parse application type")
	ErrAppDependency          = errors.New("failed to resolve application dependency")
	ErrUnsupportedAppProvider = errors.New("unsupported application provider")

	// spec
	ErrMissingRenderedSpec  = fmt.Errorf("missing rendered spec")
	ErrReadingRenderedSpec  = fmt.Errorf("reading rendered spec")
	ErrWritingRenderedSpec  = fmt.Errorf("writing rendered spec")
	ErrCheckingFileExists   = fmt.Errorf("checking if file exists")
	ErrCopySpec             = fmt.Errorf("copying spec")
	ErrGettingBootcStatus   = fmt.Errorf("getting current bootc status")
	ErrGettingDeviceSpec    = fmt.Errorf("getting device spec")
	ErrParseRenderedVersion = fmt.Errorf("failed to convert version to integer")
	ErrUnmarshalSpec        = fmt.Errorf("unmarshalling spec")
	ErrInvalidSpecType      = fmt.Errorf("invalid spec type")
	ErrInvalidSpec          = fmt.Errorf("invalid spec")

	// hooks
	ErrInvalidTokenFormat             = errors.New("invalid token: formatting")
	ErrTokenNotSupported              = errors.New("invalid token: not supported")
	ErrActionTypeNotFound             = errors.New("failed to find action type")
	ErrUnsupportedFilesystemOperation = errors.New("unsupported filesystem operation")

	// networking
	ErrNoContent   = fmt.Errorf("no content")
	ErrNilResponse = fmt.Errorf("received nil response")

	// authentication
	ErrAuthenticationFailed = errors.New("authentication failed")
)

// TODO: tighten up the retryable errors ideally all retryable errors should be explicitly defined
func IsRetryable(err error) bool {
	switch {
	case IsTimeoutError(err):
		return true
	case errors.Is(err, ErrRetryable):
		return true
	case errors.Is(err, ErrNoContent):
		// no content is a retryable error it means the server does not have a
		// new template version
		return true
	case errors.Is(err, ErrNoRetry):
		return false
	case errors.Is(err, ErrAuthenticationFailed):
		return false
	default:
		// this will need to be updated as we identify more errors that are not
		// retryable but for now we will retry and mark degraded.
		return true
	}
}

func Is(err, target error) bool {
	return errors.Is(err, target)
}

func New(msg string) error {
	return errors.New(msg)
}

func Join(errs ...error) error {
	return errors.Join(errs...)
}

func IsTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return false
}
