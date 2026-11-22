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
