package errors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/flightctl/flightctl/pkg/poll"
	"google.golang.org/grpc/codes"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	ErrRetryable = errors.New("retryable error")
	ErrNoRetry   = errors.New("no retry")

	// phases - used to wrap errors indicating which sync phase failed
	ErrPhasePreparing        = errors.New("before update")
	ErrPhaseApplyingUpdate   = errors.New("sync device")
	ErrPhaseActivatingConfig = errors.New("after update")

	// components - used to wrap errors indicating which component failed
	ErrComponentResources      = errors.New("resources")
	ErrComponentDownloadPolicy = errors.New("download policy")
	ErrComponentUpdatePolicy   = errors.New("update policy")
	ErrComponentPrefetch       = errors.New("prefetch")
	ErrComponentApplications   = errors.New("applications")
	ErrComponentHooks          = errors.New("hooks")
	ErrComponentConfig         = errors.New("config")
	ErrComponentSystemd        = errors.New("systemd")
	ErrComponentLifecycle      = errors.New("lifecycle")
	ErrComponentOS             = errors.New("os")
	ErrComponentOSReconciled   = errors.New("os reconciliation")

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
	ErrKubernetesAppsDisabled = errors.New("kubernetes applications disabled")

	// compose
	ErrNoComposeFile     = errors.New("no valid compose file found")
	ErrNoComposeServices = errors.New("no services found in compose spec")

	// quadlet
	ErrNoQuadletFile     = errors.New("no quadlet file found")
	ErrNoQuadletWorkload = errors.New("no quadlet workloads found")

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
	ErrPrefetchNotReady     = errors.New("oci prefetch not ready")
	ErrOCICollectorNotReady = errors.New("oci target collector not ready")

	// bootc
	ErrBootcStatusInvalidJSON = errors.New("bootc status did not return valid JSON")

	// Certificate management errors
	ErrCreateCertificateSigningRequest = errors.New("failed to create certificate signing request")

	// resource monitoring
	ErrCriticalResourceAlert = errors.New("critical resource alert")

	// policy errors
	ErrInt64Conversion   = errors.New("convert string to int64")
	ErrNegativeValue     = errors.New("cannot be negative")
	ErrPolicyCheckFailed = errors.New("policy check failed")

	// application errors
	ErrAppTypeMismatch             = errors.New("app type mismatch")
	ErrAppProviders                = errors.New("app providers")
	ErrCollectingEmbedded          = errors.New("collecting embedded")
	ErrEnsuringAppType             = errors.New("ensuring app type")
	ErrDetectingOCIType            = errors.New("detecting oci type")
	ErrResolvingAppName            = errors.New("resolving app name")
	ErrExtractingArtifact          = errors.New("extracting artifact")
	ErrExtractingOCI               = errors.New("extracting oci")
	ErrVerifyingImage              = errors.New("verifying image")
	ErrEnsuringDependencies        = errors.New("ensuring dependencies")
	ErrDecodingApplicationContent  = errors.New("decoding application content")
	ErrReadingAuthFile             = errors.New("reading auth file")
	ErrParsingAuthFile             = errors.New("parsing auth file")
	ErrWritingInlineConfigFile     = errors.New("writing inline config file")
	ErrWritingEnvFile              = errors.New("writing env file")
	ErrGettingVolumes              = errors.New("getting volumes")
	ErrCreatingTmpDir              = errors.New("creating tmp dir")
	ErrGettingProviderSpec         = errors.New("getting provider spec")
	ErrConvertInlineConfigProvider = errors.New("convert inline config provider")
	ErrExtractingNestedTargets     = errors.New("extracting nested targets")
	ErrSchedulingPrefetchTargets   = errors.New("scheduling prefetch targets")
	ErrGettingImageDigest          = errors.New("getting image digest")
	ErrGettingArtifactDigest       = errors.New("getting artifact digest")
	ErrPrefetchCollector           = errors.New("prefetch collector")

	// config errors
	ErrFailedToRetrieveUserID      = errors.New("failed to retrieve userid")
	ErrFailedToRetrieveGroupID     = errors.New("failed to retrieve groupid")
	ErrFailedToRemoveObsoleteFiles = errors.New("failed to remove obsolete files")
	ErrDeletingFilesFailed         = errors.New("deleting files failed")
	ErrConvertDesiredConfigToFiles = errors.New("convert desired config to files")
	ErrConvertCurrentConfigToFiles = errors.New("convert current config to files")
	ErrFailedConvertingGID         = errors.New("failed converting gid")
	ErrFailedConvertingUID         = errors.New("failed converting uid")
	ErrFailedRetrievingCurrentUser = errors.New("failed retrieving current user")

	// systemd errors
	ErrInvalidPatterns = errors.New("invalid patterns")

	// lifecycle/hooks errors
	ErrFailedToPushStatus                   = errors.New("failed to push status")
	ErrFailedToUpdateStatusWithDecommission = errors.New("failed to update status with decommission")
	ErrInvalidEnvvarFormat                  = errors.New("invalid envvar format")
	ErrExitCode                             = errors.New("exit code")
	ErrParsingHookActionsFrom               = errors.New("parsing hook actions from")
	ErrReadingHookActionsFrom               = errors.New("reading hook actions from")
	ErrUnknownHookActionType                = errors.New("unknown hook action type")
	ErrUnknownHookConditionType             = errors.New("unknown hook condition type")
	ErrFailedToExecute                      = errors.New("failed to execute")
	ErrLookingForHook                       = errors.New("looking for hook")

	// OS errors
	ErrUnableToParseImageReference = errors.New("unable to parse image reference into a valid bootc target")
	ErrStageImage                  = errors.New("stage image")
	ErrApplyImage                  = errors.New("apply image")

	// application lifecycle errors
	ErrParsingComposeSpec    = errors.New("parsing compose spec")
	ErrParsingQuadletSpec    = errors.New("parsing quadlet spec")
	ErrValidatingComposeSpec = errors.New("validating compose spec")
	ErrValidatingQuadletSpec = errors.New("validating quadlet spec")
	ErrRemovingApplication   = errors.New("removing application")
	ErrInstallingApplication = errors.New("installing application")
	ErrCopyingImage          = errors.New("copying image")

	ErrPermissionDenied   = os.ErrPermission
	ErrDoesNotExist       = os.ErrNotExist
	ErrReadOnlyFileSystem = syscall.EROFS
	ErrNoSpaceLeft        = syscall.ENOSPC
	ErrDiskFull           = errors.New("disk full")
	ErrFailedToDecode     = errors.New("failed to decode")
	ErrFailedToParse      = errors.New("failed to parse")
	ErrNoSuchFile         = errors.New("no such file")
	ErrNonRetryable       = errors.New("non-retryable")
	ErrNotSet             = errors.New("not set")
	ErrIsRequired         = errors.New("is required")
	ErrIsEmpty            = errors.New("is empty")
	ErrMarshal            = errors.New("marshal") // json marshalling error
	ErrPodmanFailed       = errors.New("podman failed")
	ErrTime               = errors.New("time:")

	stderrKeywords = map[string]error{
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
		"unexpected EOF":         ErrNetwork,
		// context
		"context canceled":          context.Canceled,
		"context deadline exceeded": context.DeadlineExceeded,
		// container image resolution
		"short-name resolution enforced": ErrImageShortName,
		// no such object
		"no such object": ErrNotFound,
	}

	// errorTypeToCode maps error types from stderrKeywords to status codes.
	ErrorTypeToCode = map[error]codes.Code{
		// authentication
		ErrAuthenticationFailed: codes.Unauthenticated,

		// not found / filesystem
		ErrNotFound:            codes.NotFound,
		ErrNotExist:            codes.NotFound,
		ErrReadingPath:         codes.NotFound,
		ErrMissingRenderedSpec: codes.NotFound,

		// networking
		ErrNetwork:       codes.Unavailable,
		ErrImageNotFound: codes.Unavailable,
		ErrNoContent:     codes.Unavailable,
		ErrNilResponse:   codes.Unavailable,

		// context
		context.Canceled:         codes.Canceled,
		context.DeadlineExceeded: codes.DeadlineExceeded,

		// invalid argument / configuration
		ErrImageShortName:     codes.InvalidArgument,
		ErrNoComposeFile:      codes.InvalidArgument,
		ErrNoComposeServices:  codes.InvalidArgument,
		ErrNoQuadletFile:      codes.InvalidArgument,
		ErrNoQuadletWorkload:  codes.InvalidArgument,
		ErrInvalidTokenFormat: codes.InvalidArgument,
		ErrTokenNotSupported:  codes.InvalidArgument,
		ErrRunActionInvalid:   codes.InvalidArgument,
		ErrPathIsDir:          codes.InvalidArgument,
		ErrInvalidPath:        codes.InvalidArgument,
		ErrInvalidSpec:        codes.InvalidArgument,

		// internal errors
		ErrAppDependency:            codes.Internal,
		ErrParseAppType:             codes.Internal,
		ErrActionTypeNotFound:       codes.Internal,
		ErrInvalidSpecType:          codes.Internal,
		ErrInvalidPolicyType:        codes.Internal,
		ErrParseRenderedVersion:     codes.Internal,
		ErrUnmarshalSpec:            codes.Internal,
		ErrBootcStatusInvalidJSON:   codes.Internal,
		ErrGettingBootcStatus:       codes.Internal,
		ErrUnknownApplicationStatus: codes.Internal,

		// unimplemented
		ErrUnsupportedAppType:             codes.Unimplemented,
		ErrUnsupportedVolumeType:          codes.Unimplemented,
		ErrUnsupportedAppProvider:         codes.Unimplemented,
		ErrUnsupportedFilesystemOperation: codes.Unimplemented,

		// failed precondition
		ErrAppLabel:               codes.FailedPrecondition,
		ErrDownloadPolicyNotReady: codes.FailedPrecondition,
		ErrUpdatePolicyNotReady:   codes.FailedPrecondition,
		ErrPrefetchNotReady:       codes.FailedPrecondition,
		ErrOCICollectorNotReady:   codes.FailedPrecondition,

		// resource exhausted
		ErrCriticalResourceAlert: codes.ResourceExhausted,

		// permission denied
		ErrReadingRenderedSpec: codes.PermissionDenied,
		ErrWritingRenderedSpec: codes.PermissionDenied,

		// enrollment
		ErrEnrollmentRequestFailed: codes.Aborted,
		ErrEnrollmentRequestDenied: codes.PermissionDenied,

		// policy errors
		ErrInt64Conversion:   codes.Internal,
		ErrNegativeValue:     codes.Internal,
		ErrPolicyCheckFailed: codes.Internal,

		// application errors
		ErrAppTypeMismatch:             codes.FailedPrecondition,
		ErrAppProviders:                codes.Internal,
		ErrCollectingEmbedded:          codes.Internal,
		ErrEnsuringAppType:             codes.Internal,
		ErrDetectingOCIType:            codes.Internal,
		ErrResolvingAppName:            codes.Internal,
		ErrExtractingArtifact:          codes.Unavailable,
		ErrExtractingOCI:               codes.Internal,
		ErrVerifyingImage:              codes.Internal,
		ErrEnsuringDependencies:        codes.Internal,
		ErrDecodingApplicationContent:  codes.Internal,
		ErrReadingAuthFile:             codes.NotFound,
		ErrParsingAuthFile:             codes.InvalidArgument,
		ErrWritingInlineConfigFile:     codes.PermissionDenied,
		ErrWritingEnvFile:              codes.PermissionDenied,
		ErrGettingVolumes:              codes.Internal,
		ErrCreatingTmpDir:              codes.PermissionDenied,
		ErrGettingProviderSpec:         codes.Internal,
		ErrConvertInlineConfigProvider: codes.Internal,
		ErrExtractingNestedTargets:     codes.Internal,
		ErrSchedulingPrefetchTargets:   codes.Internal,
		ErrGettingImageDigest:          codes.Unavailable,
		ErrGettingArtifactDigest:       codes.Unavailable,
		ErrPrefetchCollector:           codes.Internal,

		// config errors
		ErrFailedToRetrieveUserID:      codes.NotFound,
		ErrFailedToRetrieveGroupID:     codes.NotFound,
		ErrFailedToRemoveObsoleteFiles: codes.PermissionDenied,
		ErrDeletingFilesFailed:         codes.PermissionDenied,
		ErrConvertDesiredConfigToFiles: codes.Internal,
		ErrConvertCurrentConfigToFiles: codes.Internal,
		ErrFailedConvertingGID:         codes.Internal,
		ErrFailedConvertingUID:         codes.Internal,
		ErrFailedRetrievingCurrentUser: codes.Internal,

		// systemd errors
		ErrInvalidPatterns: codes.InvalidArgument,

		// lifecycle/hooks errors
		ErrFailedToPushStatus:                   codes.Unavailable,
		ErrFailedToUpdateStatusWithDecommission: codes.Unavailable,
		ErrInvalidEnvvarFormat:                  codes.InvalidArgument,
		ErrExitCode:                             codes.InvalidArgument,
		ErrParsingHookActionsFrom:               codes.InvalidArgument,
		ErrReadingHookActionsFrom:               codes.NotFound,
		ErrUnknownHookActionType:                codes.Internal,
		ErrUnknownHookConditionType:             codes.Internal,
		ErrFailedToExecute:                      codes.Unavailable,
		ErrLookingForHook:                       codes.InvalidArgument,

		// OS errors
		ErrUnableToParseImageReference: codes.InvalidArgument,
		ErrStageImage:                  codes.Unavailable,
		ErrApplyImage:                  codes.Unavailable,

		// application lifecycle errors
		ErrParsingComposeSpec:    codes.InvalidArgument,
		ErrParsingQuadletSpec:    codes.InvalidArgument,
		ErrValidatingComposeSpec: codes.InvalidArgument,
		ErrValidatingQuadletSpec: codes.InvalidArgument,
		ErrRemovingApplication:   codes.Internal,
		ErrInstallingApplication: codes.Internal,
		ErrCopyingImage:          codes.Unavailable,

		// error message patterns
		ErrPermissionDenied:   codes.PermissionDenied,
		ErrReadOnlyFileSystem: codes.PermissionDenied,
		ErrNoSpaceLeft:        codes.ResourceExhausted,
		ErrDiskFull:           codes.ResourceExhausted,
		ErrFailedToDecode:     codes.InvalidArgument,
		ErrFailedToParse:      codes.InvalidArgument,
		ErrDoesNotExist:       codes.NotFound,
		ErrNoSuchFile:         codes.NotFound,
		ErrNonRetryable:       codes.NotFound,
		ErrNotSet:             codes.Internal,
		ErrIsRequired:         codes.Internal,
		ErrIsEmpty:            codes.Internal,
		ErrMarshal:            codes.Internal,
		ErrPodmanFailed:       codes.Internal,
		ErrTime:               codes.Internal,
	}
)

type stderrError struct {
	wrapped error
	reason  string
	code    int
	stderr  string
}

func (e *stderrError) Error() string {
	return fmt.Sprintf("%s: code: %d: %s", e.wrapped.Error(), e.code, e.stderr)
}

func (e *stderrError) Unwrap() error {
	return e.wrapped
}

func (e *stderrError) Reason() string {
	return e.reason
}

type reasoner interface {
	Reason() string
}

// Element is an error type that carries an element identifier through
// the error chain. It implements the error interface so it can be wrapped
// and extracted via errors.As.
type Element struct {
	Value string
}

// WithElement creates a new Element error with the given identifier.
func WithElement(s string) *Element {
	return &Element{Value: s}
}

// Error implements the error interface.
func (e *Element) Error() string {
	return e.Value
}

// GetElement extracts the element identifier from an error chain.
// Returns an empty string if no Element is found.
func GetElement(err error) string {
	var elem *Element
	if errors.As(err, &elem) {
		return elem.Value
	}
	return ""
}

// Reason extracts the underlying reason from any error if it implements a Reason method
// If no Reason method is detected, Error is returned
func Reason(err error) string {
	if err == nil {
		return ""
	}

	var r reasoner
	if errors.As(err, &r) {
		return r.Reason()
	}

	return err.Error()
}

// TODO: tighten up the retryable errors ideally all retryable errors should be explicitly defined
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		// see https://pkg.go.dev/net#DNSError
		return dnsErr.Temporary()
	case IsTimeoutError(err):
		return true
	case errors.Is(err, ErrRetryable):
		return true
	case errors.Is(err, ErrNetwork):
		return true
	case errors.Is(err, ErrDownloadPolicyNotReady), errors.Is(err, ErrUpdatePolicyNotReady):
		return true
	case errors.Is(err, ErrPrefetchNotReady), errors.Is(err, ErrOCICollectorNotReady):
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
	case errors.Is(err, syscall.ECONNRESET):
		// connection reset by peer is a transient network error
		return true
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case strings.Contains(err.Error(), "unexpected EOF"):
		// HTTP client wraps EOF errors from broken connections
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

	if errors.Is(err, syscall.ETIMEDOUT) {
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
	for check, err := range stderrKeywords {
		if strings.Contains(stderr, check) {
			return &stderrError{
				wrapped: err,
				reason:  check,
				code:    exitCode,
				stderr:  stderr,
			}
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

// ToCode returns the gRPC status code for an error.
func ToCode(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	for sentinel, code := range ErrorTypeToCode {
		if errors.Is(err, sentinel) {
			return code
		}
	}

	return codes.Unknown
}
