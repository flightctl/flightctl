package errors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/flightctl/flightctl/pkg/poll"
	"k8s.io/apimachinery/pkg/util/wait"
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
	ErrUnsupportedVolumeType  = errors.New("unsupported volume type")
	ErrParseAppType           = errors.New("failed to parse application type")
	ErrAppDependency          = errors.New("failed to resolve application dependency")
	ErrUnsupportedAppProvider = errors.New("unsupported application provider")
	ErrAppLabel               = errors.New("required label not found")

	// compose
	ErrNoComposeFile     = errors.New("no valid compose file found")
	ErrNoComposeServices = errors.New("no services found in compose spec")

	// application status
	ErrUnknownApplicationStatus = errors.New("unknown application status")

	// container images
	ErrImageShortName = errors.New("failed to resolve image short name: use the full name i.e registry/image:tag")

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
	ErrRunActionInvalid               = errors.New("invalid run action")
	ErrUnsupportedFilesystemOperation = errors.New("unsupported filesystem operation")

	// networking
	ErrNoContent   = errors.New("no content")
	ErrNilResponse = errors.New("received nil response")
	ErrNetwork     = errors.New("network")

	// authentication
	ErrAuthenticationFailed = errors.New("authentication failed")

	// io
	ErrReadingPath = errors.New("failed reading path")
	ErrPathIsDir   = errors.New("provided path is a directory")
	ErrNotFound    = errors.New("not found")
	ErrNotExist    = os.ErrNotExist
	ErrInvalidPath = errors.New("invalid path")

	// images
	ErrImageNotFound = errors.New("image not found")

	// policy
	ErrDownloadPolicyNotReady = errors.New("download policy not ready")
	ErrUpdatePolicyNotReady   = errors.New("update policy not ready")
	ErrInvalidPolicyType      = errors.New("invalid policy type")

	// prefetch
	ErrPrefetchNotReady = errors.New("oci prefetch not ready")

	// bootc
	ErrBootcStatusInvalidJSON = errors.New("bootc status did not return valid JSON")

	// Certificate management errors
	ErrCreateCertificateSigningRequest = errors.New("failed to create certificate signing request")
)

// TODO: tighten up the retryable errors ideally all retryable errors should be explicitly defined
func IsRetryable(err error) bool {
	switch {
	case IsTimeoutError(err):
		return true
	case errors.Is(err, ErrRetryable):
		return true
	case errors.Is(err, ErrNetwork):
		return true
	case errors.Is(err, ErrDownloadPolicyNotReady), errors.Is(err, ErrUpdatePolicyNotReady):
		return true
	case errors.Is(err, ErrPrefetchNotReady):
		return true
	case errors.Is(err, ErrNoContent):
		// no content is a retryable error it means the server does not have a
		// new template version
		return true
	case errors.Is(err, ErrBootcStatusInvalidJSON):
		// this is a retryable error because it means the bootc status did not
		// return valid JSON. this is a bug in the bootc status and we should
		// retry the request as the error is transient.
		return true
	case errors.Is(err, poll.ErrMaxSteps):
		return true
	case errors.Is(err, ErrNoRetry):
		return false
	case errors.Is(err, ErrAuthenticationFailed):
		return false
	default:
		// this will need to be updated as we identify more errors that are
		// retryable but for now we will fail the update.
		return false
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

	if wait.Interrupted(err) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return false
}

// FromStderr converts stderr output from a command into an error type.
func FromStderr(stderr string, exitCode int) error {
	// mapping is used to convert stderr output from os.exec into an error
	errMap := map[string]error{
		// authentication
		"authentication required": ErrAuthenticationFailed,
		"unauthorized":            ErrAuthenticationFailed,
		"access denied":           ErrAuthenticationFailed,
		// not found
		"not found":        ErrNotFound,
		"manifest unknown": ErrImageNotFound,
		// networking
		"no such host":           ErrNetwork,
		"connection refused":     ErrNetwork,
		"unable to resolve host": ErrNetwork,
		"network is unreachable": ErrNetwork,
		"i/o timeout":            ErrNetwork,
		// context
		"context canceled":          context.Canceled,
		"context deadline exceeded": context.DeadlineExceeded,
		// container image resolution
		"short-name resolution enforced": ErrImageShortName,
		// no such object
		"no such object": ErrNotFound,
	}
	for check, err := range errMap {
		if strings.Contains(stderr, check) {
			return fmt.Errorf("%w: code: %d: %s", err, exitCode, stderr)
		}
	}
	return fmt.Errorf("code: %d: %s", exitCode, stderr)
}

func IsContext(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}
