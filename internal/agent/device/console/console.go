package console

const (
	StdinID  byte = 0
	StdoutID byte = 1
	StderrID byte = 2
	ErrID    byte = 3
	ResizeID byte = 4
	CloseID  byte = 255

	// StreamProtocolV5Name defines the protocol for v5 streams
	// This must be the same as defined in "k8s.io/apimachinery/pkg/util/remotecommand"
	StreamProtocolV5Name = "v5.channel.k8s.io"

	// NonZeroExitCodeReason is the reason used in status when a command exits with a non-zero code
	NonZeroExitCodeReason = StatusReason("NonZeroExitCode")

	// ExitCodeCauseType is the cause type used to indicate the exit code
	ExitCodeCauseType = CauseType("ExitCode")
)

// Status is a return value for console command execution status.
// This is compatible with Kubernetes metav1.Status for protocol compatibility.
type Status struct {
	// Status of the operation. One of: "Success" or "Failure".
	Status string `json:"status,omitempty"`
	// A machine-readable description of why this operation is in the "Failure" status.
	Reason StatusReason `json:"reason,omitempty"`
	// Extended data associated with the reason.
	Details *StatusDetails `json:"details,omitempty"`
	// Suggested HTTP return code for this status (exit code).
	Code int32 `json:"code,omitempty"`
}

// StatusDetails provides additional information about a console command failure.
type StatusDetails struct {
	// The Causes array includes more details associated with the failure.
	Causes []StatusCause `json:"causes,omitempty"`
}

// StatusCause provides more information about a console command failure.
type StatusCause struct {
	// A machine-readable description of the cause of the error.
	Type CauseType `json:"reason,omitempty"`
	// A human-readable description of the cause of the error (e.g., exit code).
	Message string `json:"message,omitempty"`
}

// StatusReason is an enumeration of possible failure causes.
type StatusReason string

// CauseType is a machine-readable value providing more detail about what occurred.
type CauseType string

// Status values
const (
	StatusSuccess = "Success"
	StatusFailure = "Failure"
)
