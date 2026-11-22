//go:build !linux

package executer

import "context"

// LinuxPhaseExecutor rejects invocations on platforms without the Linux profile.
type LinuxPhaseExecutor struct{}

// PhaseJob has no execution scope on this platform.
type PhaseJob struct{}

// PreparePhase reports that this platform cannot install the phase boundary.
func (*LinuxPhaseExecutor) PreparePhase(context.Context, PhaseExecSpec) (*PhaseJob, error) {
	return nil, ErrPhaseUnsupported
}

// Start reports that phase execution is unsupported.
func (*PhaseJob) Start(context.Context) error { return ErrPhaseUnsupported }

// Wait reports that phase execution is unsupported.
func (*PhaseJob) Wait(context.Context) (PhaseExecOutput, error) {
	return PhaseExecOutput{}, ErrPhaseUnsupported
}

// Close releases the unsupported job.
func (*PhaseJob) Close() error { return nil }

// CurrentPhaseNamespaceIdentity reports that Linux namespace proof is unavailable.
func CurrentPhaseNamespaceIdentity() (PhaseNamespaceIdentity, error) {
	return PhaseNamespaceIdentity{}, ErrPhaseUnsupported
}

// CurrentHostPhaseNamespaceIdentity reports that the Linux namespace baseline is unavailable.
func CurrentHostPhaseNamespaceIdentity() (PhaseNamespaceIdentity, error) {
	return PhaseNamespaceIdentity{}, ErrPhaseUnsupported
}

// RunPhaseLauncher reports that the Linux namespace proof is unavailable.
func RunPhaseLauncher([]string) error { return ErrPhaseUnsupported }
