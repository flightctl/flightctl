package firmware

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
)

// Reason is the failure reason projected into firmware status by callers.
type Reason string

const (
	// ReasonUpdaterUnavailable means approved updater code could not be
	// obtained or authenticated.
	ReasonUpdaterUnavailable Reason = "UpdaterUnavailable"
	// ReasonFirmwareNotSupported means this device cannot support the approved
	// release: its protocol, runtime dependencies or requested access.
	ReasonFirmwareNotSupported Reason = "FirmwareNotSupported"
	// ReasonResourceLimitExceeded means extraction exceeded a budget admitted
	// for the release; FetchError.ExceededBudget names that budget.
	ReasonResourceLimitExceeded Reason = Reason(v1beta1.FirmwareErrorReasonResourceLimitExceeded)
	// ReasonInsufficientSpace means the cache lacked the free space the
	// release requires for extraction.
	ReasonInsufficientSpace Reason = "InsufficientSpace"
)

var (
	ErrRetainedPayloadMissing         = errors.New("retained updater payload is missing")
	ErrRetainedPayloadChanged         = errors.New("retained updater payload changed")
	ErrRetainedVerificationIncomplete = errors.New("retained updater payload verification did not complete")
	ErrPinConflict                    = errors.New("retention pin conflicts with the package identity")
	ErrNoExtractionBudget             = errors.New("updater release declares no extraction budget")
	ErrUnmountFailed                  = errors.New("updater image unmount failed")
	ErrEvidenceNotPersisted           = errors.New("updater host evidence was not persisted")
	ErrPublishContended               = errors.New("updater payload publication stayed contended after one retry")
)

const (
	signatureRejectionFormat = "updater %s/%s: publisher signature verification failed for %s; the image must be signed by the approved publisher key for this exact repository, and the host must enable sigstore attachment discovery (use-sigstore-attachments) for %s"
	maxCauseRunes            = 1024
)

// FetchError reports why updater code was not obtained or not authenticated.
// For a pull rejected by the signature policy, Error returns the documented
// actionable message and Unwrap returns the sanitized podman error.
type FetchError struct {
	Reason    Reason
	Retryable bool
	Err       error
	// ExceededBudget names the admitted release budget that was exceeded. It
	// is set exactly when Reason is ReasonResourceLimitExceeded.
	ExceededBudget string

	message string
}

func (e *FetchError) Error() string {
	switch {
	case e.message != "":
		return e.message
	case e.Err != nil:
		return e.Err.Error()
	default:
		return string(e.Reason)
	}
}

func (e *FetchError) Unwrap() error { return e.Err }

// Is exposes Retryable to the agent's retry classification: a retryable fetch
// error matches deviceerrors.ErrRetryable and any other matches
// deviceerrors.ErrNoRetry.
func (e *FetchError) Is(target error) bool {
	switch target {
	case deviceerrors.ErrRetryable:
		return e.Retryable
	case deviceerrors.ErrNoRetry:
		return !e.Retryable
	default:
		return false
	}
}

func newFetchError(reason Reason, retryable bool, release string, err error) *FetchError {
	return &FetchError{Reason: reason, Retryable: retryable, Err: fmt.Errorf("updater %w: %w", deviceerrors.WithElement(release), err)}
}

func unavailable(release string, retryable bool, err error) *FetchError {
	return newFetchError(ReasonUpdaterUnavailable, retryable, release, err)
}

func notSupported(release string, err error) *FetchError {
	return newFetchError(ReasonFirmwareNotSupported, false, release, err)
}

func signatureRejected(name, version, reference, repository string, cause error) *FetchError {
	e := newFetchError(ReasonUpdaterUnavailable, false, name+"/"+version, cause)
	e.message = fmt.Sprintf(signatureRejectionFormat, name, version, reference, repository)
	return e
}

// sanitizedCause reduces podman output to one bounded line without control
// characters so that it can be logged and wrapped safely.
func sanitizedCause(err error) error {
	text := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, err.Error())), " ")
	if runes := []rune(text); len(runes) > maxCauseRunes {
		text = string(runes[:maxCauseRunes]) + "..."
	}
	return errors.New(text)
}
