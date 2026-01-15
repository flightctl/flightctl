package errors

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flightctl/flightctl/internal/consts"
	"google.golang.org/grpc/codes"
)

// FormatError converts a raw error into a StructuredError.
func FormatError(err error) (string, time.Time) {
	se := &StructuredError{
		Timestamp: time.Now(),
	}
	se.extractPhaseAndComponent(err)
	se.determineStatusCode(err)
	se.inferCategory()
	se.Element = extractElement(err.Error())
	msg := se.buildMessage()
	return msg, se.Timestamp
}

// StructuredError represents a sanitized, structured error for status display.
type StructuredError struct {
	Phase      string
	Category   Category
	Component  string
	Element    string
	StatusCode codes.Code
	Timestamp  time.Time
}

var (
	ErrComponentUnknown = errors.New("unknown")
	ErrPhaseUnknown     = errors.New("Unknown")
)

// Category represents the high-level functional area describing WHAT failed.
type Category string

const (
	CategoryNetwork       Category = "Network"
	CategoryConfiguration Category = "Configuration"
	CategoryFilesystem    Category = "Filesystem"
	CategorySecurity      Category = "Security"
	CategoryStorage       Category = "Storage"
	CategoryResource      Category = "Resource"
	CategorySystem        Category = "System"
	CategoryUnknown       Category = "Unknown"
)

// keywordToCode is a simplified structure for cache-friendly iteration.
type keywordToCode struct {
	keyword string
	code    codes.Code
}

var phaseSentinels = []error{
	ErrPhasePreparing,
	ErrPhaseApplyingUpdate,
	ErrPhaseActivatingConfig,
}

// phaseToUpdateState maps phase sentinel errors to their corresponding UpdateState values.
var phaseToUpdateState = map[error]consts.UpdateState{
	ErrPhasePreparing:        consts.UpdateStatePreparing,
	ErrPhaseApplyingUpdate:   consts.UpdateStateApplyingUpdate,
	ErrPhaseActivatingConfig: consts.UpdateStateRebooting,
}

var componentSentinels = []error{
	ErrComponentResources,
	ErrComponentUpdatePolicy,
	ErrComponentApplications,
	ErrComponentConfig,
	ErrComponentSystemd,
	ErrComponentLifecycle,
	ErrComponentOS,
}

// These don't follow the standard DeviceSpec API keys.
var subComponentSentinels = map[error]error{
	ErrComponentDownloadPolicy: ErrComponentUpdatePolicy,
	ErrComponentPrefetch:       ErrComponentApplications,
	ErrComponentHooks:          ErrComponentLifecycle,
	ErrComponentOSReconciled:   ErrComponentOS,
}

// Components map directly to DeviceSpec API keys.
var componentKeywords = map[string][]keywordToCode{
	ErrComponentDownloadPolicy.Error(): {
		{"convert string to int64", codes.Internal},
		{"cannot be negative", codes.Internal},
		{"policy check failed", codes.Internal},
	},
	ErrComponentUpdatePolicy.Error(): {
		{"convert string to int64", codes.Internal},
		{"cannot be negative", codes.Internal},
		{"policy check failed", codes.Internal},
	},
	ErrComponentApplications.Error(): {
		{"app type mismatch", codes.FailedPrecondition},
		{"app providers", codes.Internal},
		{"collecting embedded", codes.Internal},
		{"ensuring app type", codes.Internal},
		{"detecting oci type", codes.Internal},
		{"resolving app name", codes.Internal},
		{"extracting artifact", codes.Unavailable},
		{"application not found", codes.Internal},
		{"extracting oci", codes.Internal},
		{"verifying image", codes.Internal},
		{"ensuring dependencies", codes.Internal},
		{"asinlineapplicationproviderspec", codes.Internal},
		{"decoding application content", codes.Internal},
		// Prefetch keywords
		{"reading existing auth file", codes.NotFound},
		{"writing inline auth file", codes.PermissionDenied},
		{"creating tmp dir for app", codes.PermissionDenied},
		{"provider spec", codes.Internal},
		{"volume", codes.Internal},
		{"convert inline config", codes.Internal},
		{"extracting nested targets", codes.Internal},
		{"scheduling prefetch targets", codes.Internal},
		{"getting image digest", codes.Unavailable},
		{"getting artifact digest", codes.Unavailable},
		{"prefetch collector", codes.Internal},
	},
	ErrComponentConfig.Error(): {
		{"failed to retrieve userid", codes.NotFound},
		{"failed to retrieve groupid", codes.NotFound},
		{"failed to remove obsolete files", codes.PermissionDenied},
		{"deleting files failed", codes.PermissionDenied},
		{"convert desired config to files", codes.Internal},
		{"convert current config to files", codes.Internal},
		{"safecast", codes.Internal},
		{"failed converting gid", codes.Internal},
		{"failed converting uid", codes.Internal},
		{"failed retrieving current user", codes.Internal},
	},
	ErrComponentSystemd.Error(): {
		{"invalid patterns", codes.InvalidArgument},
	},
	ErrComponentLifecycle.Error(): {
		// Lifecycle keywords
		{"failed to push status", codes.Unavailable},
		{"failed to update status with decommission", codes.Unavailable},
		// Hooks keywords (merged)
		{"validation failed", codes.InvalidArgument},
		{"invalid envvar format", codes.InvalidArgument},
		{"key cannot be empty", codes.InvalidArgument},
		{"key cannot contain spaces", codes.InvalidArgument},
		{"value cannot be empty", codes.InvalidArgument},
		{"key must be uppercase", codes.InvalidArgument},
		{"exit code", codes.InvalidArgument},
		{"parsing hook actions from", codes.InvalidArgument},
		{"reading hook actions from", codes.NotFound},
		{"workdir", codes.NotFound},
		{"ashookactionrun", codes.Internal},
		{"ashookconditionexpression", codes.Internal},
		{"ashookconditionpathop", codes.Internal},
		{"unknown hook action type", codes.Internal},
		{"unknown hook condition type", codes.Internal},
		{"failed to execute", codes.Unavailable},
		{"looking for hook", codes.InvalidArgument},
	},
	ErrComponentOS.Error(): {
		{"unable to parse image reference", codes.InvalidArgument},
		{"stage image", codes.Unavailable},
		{"apply image", codes.Unavailable},
	},
	ErrComponentResources.Error(): {
		{"resourcemonitorspec", codes.Internal},
	},
}

// globalKeywords are patterns not covered by typed errors.
var globalKeywords = []keywordToCode{
	{"permission denied", codes.PermissionDenied},
	{"read-only file system", codes.PermissionDenied},
	{"no space left", codes.ResourceExhausted},
	{"disk full", codes.ResourceExhausted},
	{"context canceled", codes.Canceled},
	{"context deadline", codes.DeadlineExceeded},

	{"failed to decode", codes.InvalidArgument},
	{"failed to parse", codes.InvalidArgument},
	{"parsing compose spec", codes.InvalidArgument},
	{"parsing quadlet spec", codes.InvalidArgument},
	{"does not exist", codes.NotFound},
	{"no such file", codes.NotFound},
	{"non-retryable", codes.NotFound},
	{"validating", codes.InvalidArgument},
	{"not set", codes.Internal},
	{"is required", codes.Internal},
	{"is empty", codes.Internal},
	{".type()", codes.Internal},
	{"discriminator", codes.Internal},
	{"marshal", codes.Internal},
	{"copying image", codes.Unavailable},
	{"removing application", codes.Internal},
	{"installing application", codes.Internal},
	{"failed to", codes.Internal},
	{"bootc", codes.Internal},
	// Generic catch-alls
	{"unknown", codes.Internal},
	{"unsupported", codes.Internal},
	{"time:", codes.Internal},
	{"invalid", codes.InvalidArgument},
}

var statusCategoryOverrides = map[codes.Code]Category{
	codes.Unavailable:        CategoryNetwork,
	codes.DeadlineExceeded:   CategoryNetwork,
	codes.Unauthenticated:    CategorySecurity,
	codes.PermissionDenied:   CategorySecurity,
	codes.DataLoss:           CategoryStorage,
	codes.NotFound:           CategoryFilesystem,
	codes.AlreadyExists:      CategoryFilesystem,
	codes.Internal:           CategorySystem,
	codes.ResourceExhausted:  CategoryResource,
	codes.InvalidArgument:    CategoryConfiguration,
	codes.OutOfRange:         CategoryConfiguration,
	codes.FailedPrecondition: CategoryConfiguration,
	codes.Unimplemented:      CategorySystem,
	codes.Canceled:           CategorySystem,
	codes.Aborted:            CategorySystem,
	codes.Unknown:            CategorySystem,
}

// extractPhaseAndComponent identifies the phase and component.
func (se *StructuredError) extractPhaseAndComponent(err error) {
	// if the sync process was changed without updating the formatting we default to unknown
	se.Phase = ErrPhaseUnknown.Error()
	se.Component = ErrComponentUnknown.Error()

	for _, phase := range phaseSentinels {
		if errors.Is(err, phase) {
			state := phaseToUpdateState[phase]
			se.Phase = string(state)
			break
		}
	}

	for _, component := range componentSentinels {
		if errors.Is(err, component) {
			se.Component = component.Error()
			return
		}
	}

	for subComponent, component := range subComponentSentinels {
		if errors.Is(err, subComponent) {
			se.Component = component.Error()
			return
		}
	}
}

// determineStatusCode finds the error code using typed errors first, then keyword fallback.
func (se *StructuredError) determineStatusCode(err error) {
	if code := ToCode(err); code != codes.Unknown {
		se.StatusCode = code
		return
	}

	// Fallback to keyword matching for untyped errors
	errDetailsLower := strings.ToLower(err.Error())

	if keywords, ok := componentKeywords[se.Component]; ok {
		for _, kw := range keywords {
			if strings.Contains(errDetailsLower, kw.keyword) {
				se.StatusCode = kw.code
				return
			}
		}
	}

	for _, kw := range globalKeywords {
		if strings.Contains(errDetailsLower, kw.keyword) {
			se.StatusCode = kw.code
			return
		}
	}

	se.StatusCode = codes.Unknown
}

// inferCategory infers the category from the status code and overrides the default category.
func (se *StructuredError) inferCategory() {
	if cat, ok := statusCategoryOverrides[se.StatusCode]; ok {
		se.Category = cat
		return
	}
	se.Category = CategoryUnknown
}

// elementPattern defines a prefix/suffix pair for extracting element names from error messages.
// If suffix is empty, the element is extracted from the prefix to the end of the string.
type elementPattern struct {
	prefix string
	suffix string
}

var elementPatterns = []elementPattern{
	// Volume patterns
	{"inspect volume \"", "\""},
	{"removing volume content \"", "\""},
	{"creating volume \"", "\""},
	{"extracting artifact to volume \"", "\""},

	// Service/quadlet patterns
	{"service: \"", "\""},
	{"service \"", "\""},
	{"getting service name for ", ":"},
	{"namespacing ", ":"},
	{"creating drop-in for ", ":"},
	{"reading drop-in directory ", ":"},

	// App patterns (specific first)
	{"copying image contents for app ", " ("},
	{"parsing compose spec for app ", " ("},
	{"validating compose spec for app ", " ("},
	{"parsing quadlet spec for app ", " ("},
	{"validating quadlet spec for app ", " ("},
	{"detecting OCI type for app ", " ("},
	{"extracting artifact contents for app ", " ("},
	{"creating tmp dir for app ", " ("},
	{"verify embedded app ", ":"},
	{"getting provider type for app ", ":"},
	{"getting image spec for app ", ":"},
	{"extracting nested targets for app ", ":"},
	{"for app ", " ("},
	{"for app ", ":"},
	{"embedded app ", ":"},

	// Target patterns
	{"starting target ", ":"},
	{"stopping target ", ":"},
	{"pulling oci target ", ": "},
	{"failed to enqueue target ", ": "},

	// OCI/Image patterns
	{"getting image digest for ", ":"},
	{"getting artifact digest for ", ":"},
	{"OCI reference ", " not found"},
	{"extracting OCI: ", " contents"},

	// File/directory patterns
	{"failed to create directory \"", "\""},
	{"creating directory ", ":"},
	{"creating file ", ":"},
	{"writing file ", ":"},
	{"could not remove file ", ":"},
	{"could not overwrite file ", " with"},
	{"failed to resolve symlink ", ":"},
	{"failed to stat symlink target ", ":"},
	{"invalid file path in tar: ", ","},
	{"remove file \"", "\""},
	{"write file ", ":"},
	{"reading \"", "\""},
	{"writing to \"", "\""},
	{"for file \"", "\""},
	{"copying ", ":"},
	{"reading tmp directory ", ":"},
	{"failed to check if directory ", " exists"},

	// User/group patterns
	{"failed to retrieve UserID for username: ", ""},
	{"failed to retrieve GroupID for group: ", ""},

	// Hook patterns
	{"reading hook actions from \"", "\""},
	{"parsing hook actions from \"", "\""},
	{"unknown hook action type \"", "\""},
	{"unknown hook condition type \"", "\""},
	{"workdir ", ":"},

	// Misc patterns
	{"unknown monitor type: ", ""},
	{"invalid regex: ", ","},
	{"unsupported content encoding: \"", "\""},
	{"unsupported action type: ", ""},
	{"invalid oci type ", ""},

	// Generic quoted fallback (must be last)
	{"\"", "\""},
}

// extractElement extracts a resource/element name from an error message string.
func extractElement(errStr string) string {
	if errStr == "" {
		return ""
	}

	for _, p := range elementPatterns {
		_, after, found := strings.Cut(errStr, p.prefix)
		if !found {
			continue
		}

		var element string
		if p.suffix == "" {
			element = after
		} else {
			captured, _, foundSuffix := strings.Cut(after, p.suffix)
			if !foundSuffix {
				continue
			}
			element = captured
		}
		if valid := cleanAndValidateElement(element); valid != "" {
			return valid
		}
	}

	return ""
}

// cleanAndValidateElement trims whitespace and truncates to 64 runes (UTF-8 safe).
func cleanAndValidateElement(element string) string {
	element = strings.TrimSpace(element)
	if element == "" {
		return ""
	}

	lenElement := utf8.RuneCountInString(element)
	indexElement := lenElement - 64
	if lenElement > 64 {
		runes := []rune(element)
		element = "..." + string(runes[indexElement:])
	}

	return element
}

func (se *StructuredError) buildMessage() string {
	element := ""
	if se.Element != "" {
		element = fmt.Sprintf(" for %q", se.Element)
	}

	return fmt.Sprintf("[%s] While %s, %s failed%s: %s issue - %s",
		se.Timestamp,
		se.Phase,
		se.Component,
		element,
		se.Category,
		statusCodeMessage(se.StatusCode),
	)
}

func statusCodeMessage(code codes.Code) string {
	switch code {
	case codes.Canceled:
		return "Operation was cancelled"
	case codes.InvalidArgument:
		return "Invalid configuration or input"
	case codes.NotFound:
		return "Required resource not found"
	case codes.AlreadyExists:
		return "Resource already exists"
	case codes.PermissionDenied:
		return "Permission denied"
	case codes.ResourceExhausted:
		return "Insufficient resources (disk space, memory)"
	case codes.FailedPrecondition:
		return "Precondition not met (waiting for dependencies)"
	case codes.Aborted:
		return "Operation was aborted"
	case codes.OutOfRange:
		return "Value out of acceptable range"
	case codes.Unimplemented:
		return "Feature not supported"
	case codes.Unavailable:
		return "Service unavailable (network issue)"
	case codes.DeadlineExceeded:
		return "Request timed out"
	case codes.Internal:
		return "Internal error occurred"
	case codes.DataLoss:
		return "Unrecoverable data loss detected"
	case codes.Unauthenticated:
		return "Authentication failed"
	default:
		// Unknown status code
		return "An error occurred"
	}
}
