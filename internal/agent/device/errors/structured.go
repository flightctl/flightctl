package errors

import (
	stderrors "errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
)

// StructuredError represents a formatted error for status display.
type StructuredError struct {
	Phase                 error
	Component             error
	Element               string
	Category              Category
	StatusCode            codes.Code
	Timestamp             time.Time
	criticalResourceAlert bool
}

// truncateElement truncates an element value to 64 characters for use in structured error messages.
// This ensures device status messages comply with API constraints while preserving the end of the path.
func truncateElement(element string) string {
	const maxLength = 64
	if len(element) > maxLength {
		return "..." + element[len(element)-maxLength:]
	}
	return element
}

// FormatError extracts phase and component from the error chain.
func FormatError(err error) *StructuredError {
	phase, rest := splitWrapped(err)
	component, rest := splitWrapped(rest)
	statusCode := ToCode(rest)

	return &StructuredError{
		Phase:                 phase,
		Component:             component,
		Element:               truncateElement(GetElement(err)),
		StatusCode:            statusCode,
		Category:              inferCategory(statusCode),
		Timestamp:             time.Now(),
		criticalResourceAlert: stderrors.Is(err, ErrCriticalResourceAlert),
	}
}

// splitWrapped extracts the first error from a joined error pair.
func splitWrapped(err error) (first, rest error) {
	// idea taken from core golang errors.As
	u, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return nil, err
	}
	if errs := u.Unwrap(); len(errs) >= 2 {
		return errs[0], errs[1]
	}
	return nil, err
}

var phaseDisplayNames = map[error]string{
	ErrPhasePreparing:        "Preparing",
	ErrPhaseApplyingUpdate:   "ApplyingUpdate",
	ErrPhaseActivatingConfig: "Rebooting",
}

func phaseDisplayName(err error) string {
	if name, ok := phaseDisplayNames[err]; ok {
		return name
	}
	if err != nil {
		return err.Error()
	}
	return "Unknown"
}

// Message returns the formatted error message string.
func (se *StructuredError) Message() string {
	// A critical resource (CPU/Memory/Disk) alert defers the update rather
	// than failing it. Use update-process wording here instead of the generic
	// "<component> failed" template below, which reads as an unrelated update
	// failure. Other errors can also map to ResourceExhausted and must retain
	// the generic failure message.
	if se.StatusCode == codes.ResourceExhausted && se.criticalResourceAlert {
		resource := se.Element
		if resource == "" {
			resource = "system"
		}
		resourceMessage := fmt.Sprintf("critical %s resource alert is active", resource)
		if resource == "Disk" {
			resourceMessage += "; insufficient disk storage space, please clear storage before retrying"
		}
		return fmt.Sprintf("[%s] Update deferred: %s; will retry once resource usage returns to normal",
			se.Timestamp.Format("2006-01-02 15:04:05"),
			resourceMessage,
		)
	}

	phase := phaseDisplayName(se.Phase)

	component := "unknown"
	if se.Component != nil {
		component = se.Component.Error()
	}

	statusMsg := statusCodeMessage(se.StatusCode)

	if se.Element != "" {
		return fmt.Sprintf("[%s] While %s: %s failed for %s: %s",
			se.Timestamp.Format("2006-01-02 15:04:05"),
			phase,
			component,
			se.Element,
			statusMsg,
		)
	}

	return fmt.Sprintf("[%s] While %s: %s failed: %s",
		se.Timestamp.Format("2006-01-02 15:04:05"),
		phase,
		component,
		statusMsg,
	)
}

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

func inferCategory(statusCode codes.Code) Category {
	if cat, ok := statusCategoryOverrides[statusCode]; ok {
		return cat
	}
	return CategoryUnknown
}

func statusCodeMessage(code codes.Code) string {
	switch code {
	case codes.Canceled:
		return "operation was cancelled"
	case codes.InvalidArgument:
		return "invalid configuration or input"
	case codes.NotFound:
		return "required resource not found"
	case codes.AlreadyExists:
		return "resource already exists"
	case codes.PermissionDenied:
		return "permission denied"
	case codes.ResourceExhausted:
		return "resource limit exceeded (CPU, memory, or disk)"
	case codes.FailedPrecondition:
		return "precondition not met (waiting for dependencies)"
	case codes.Aborted:
		return "operation was aborted"
	case codes.OutOfRange:
		return "value out of acceptable range"
	case codes.Unimplemented:
		return "feature not supported"
	case codes.Unavailable:
		return "service unavailable (network issue)"
	case codes.DeadlineExceeded:
		return "request timed out"
	case codes.Internal:
		return "internal error occurred"
	case codes.DataLoss:
		return "unrecoverable data loss detected"
	case codes.Unauthenticated:
		return "authentication failed"
	default:
		return "an error occurred"
	}
}
