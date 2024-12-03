package errors

import (
	"context"
	"errors"
	"net"
)

var (
	ErrRetryable = errors.New("retryable error")
	ErrNoRetry   = errors.New("no retry")

	// bootstrap
	ErrEnrollmentRequestFailed = errors.New("enrollment request failed")
	ErrEnrollmentRequestDenied = errors.New("enrollment request denied")

	// applications
	ErrAppNameRequired        = errors.New("application name is required")
	ErrAppNotFound            = errors.New("application not found")
	ErrUnsupportedAppType     = errors.New("unsupported application type")
	ErrParseAppType           = errors.New("failed to parse application type")
	ErrAppDependency          = errors.New("failed to resolve application dependency")
	ErrUnsupportedAppProvider = errors.New("unsupported application provider")

	// spec
	ErrMissingRenderedSpec  = errors.New("missing rendered spec")
	ErrReadingRenderedSpec  = errors.New("reading rendered spec")
	ErrWritingRenderedSpec  = errors.New("writing rendered spec")
	ErrCheckingFileExists   = errors.New("checking if file exists")
	ErrCopySpec             = errors.New("copying spec")
	ErrGettingBootcStatus   = errors.New("getting current bootc status")
	ErrGettingDeviceSpec    = errors.New("getting device spec")
	ErrParseRenderedVersion = errors.New("failed to convert version to integer")
	ErrUnmarshalSpec        = errors.New("unmarshalling spec")
	ErrInvalidSpecType      = errors.New("invalid spec type")
	ErrInvalidSpec          = errors.New("invalid spec")

	// hooks
	ErrInvalidTokenFormat             = errors.New("invalid token: formatting")
	ErrTokenNotSupported              = errors.New("invalid token: not supported")
	ErrActionTypeNotFound             = errors.New("failed to find action type")
	ErrUnsupportedFilesystemOperation = errors.New("unsupported filesystem operation")

	// networking
	ErrNoContent   = errors.New("no content")
	ErrNilResponse = errors.New("received nil response")

	// authentication
	ErrAuthenticationFailed = errors.New("authentication failed")

	// io
	ErrReadingPath = errors.New("failed reading path")
	ErrPathIsDir   = errors.New("provided path is a directory")
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
