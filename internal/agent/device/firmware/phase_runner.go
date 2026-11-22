package firmware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/executer"
)

type phaseApproval interface {
	WithApprovedRelease(context.Context, string, string, string, func(policy.Receipt) error) error
}

type phaseJob interface {
	Start(context.Context) error
	Wait(context.Context) (executer.PhaseExecOutput, error)
	Close() error
}

func executeRecordedPhase(ctx context.Context, store phaseRecordStore, approval phaseApproval, job phaseJob, validation phaseValidationContext, ceiling int64) (result phaseResult, err error) {
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, job.Close())
		}
	}()
	id := validation.request.Identity
	if id.Action != phasePrepare && id.Action != phaseVerify {
		return phaseResult{}, errors.New("firmware phase is disabled")
	}
	record, err := store.load()
	if err != nil {
		return phaseResult{}, err
	}
	if record.Pending != nil {
		return phaseResult{}, errPhasePending
	}
	if record.AttemptID == "" {
		record.AttemptID = id.AttemptID
	}
	if err := checkRecordedPhase(record, validation, ceiling); err != nil {
		return phaseResult{}, err
	}
	if record.Selected != nil {
		validation.targets = record.Selected.Artifacts
	}
	err = approval.WithApprovedRelease(ctx, id.Name, id.Version, id.DescriptorDigest, func(receipt policy.Receipt) error {
		record.Pending = &id
		record.Binding = phaseBinding(id)
		record.Dispatches++
		record.PendingReceipt = &receipt
		if err := record.checkResultCapacity(validation.limits.MaxResultBytes); err != nil {
			return err
		}
		if err := store.save(record); err != nil {
			return err
		}
		return job.Start(ctx)
	})
	if err != nil {
		return phaseResult{}, err
	}
	out, runErr := job.Wait(ctx)
	closeErr := job.Close()
	closed = true
	if err := errors.Join(runErr, closeErr); err != nil {
		return phaseResult{}, err
	}
	if out.ExitCode != 0 {
		return phaseResult{}, errors.New("firmware updater exited unsuccessfully")
	}
	accepted, err := decodePhaseResult(out.Result, validation)
	if err != nil {
		return phaseResult{}, err
	}
	err = approval.WithApprovedRelease(ctx, id.Name, id.Version, id.DescriptorDigest, func(receipt policy.Receipt) error {
		record.Pending = nil
		record.PendingReceipt = nil
		record.Accepted = &accepted
		record.AcceptedReceipt = &receipt
		if accepted.Checkpoint != nil {
			record.Checkpoint = accepted.Checkpoint
		}
		if accepted.Prepared != nil {
			record.Selected = accepted.Prepared
		}
		return store.save(record)
	})
	if err != nil {
		return phaseResult{}, err
	}
	return accepted, nil
}

func checkRecordedPhase(record phaseRecord, validation phaseValidationContext, ceiling int64) error {
	id := validation.request.Identity
	if record.AttemptID != id.AttemptID || !samePhaseCheckpoint(record.Checkpoint, validation.request.Checkpoint) ||
		(record.Dispatches > 0 && record.Binding != phaseBinding(id)) {
		return errors.New("firmware phase differs from durable runner input")
	}
	if ceiling <= 0 || record.Dispatches >= ceiling || id.Cycle > validation.limits.MaxCycles {
		return errors.New("firmware phase exhausted its dispatch or cycle budget")
	}
	if id.CallID != phaseCallID(record.Dispatches+1) {
		return errors.New("firmware phase call identity is stale or out of order")
	}
	if record.Selected != nil {
		digest, err := phaseSelectionDigest(record.Selected.Artifacts)
		if err != nil || digest != record.Selected.SelectionDigest || id.PlanDigest != digest {
			return errors.New("firmware phase differs from the durably selected manifest")
		}
	}
	return nil
}

// A call ordinal is unique within its correlated attempt, including after restart.
func phaseCallID(dispatch int64) string {
	return strconv.FormatInt(dispatch, 10)
}

func phaseOutcomeUncertain(err error) bool {
	return errors.Is(err, executer.ErrPhaseCleanup) || errors.Is(err, errPhaseDurabilityUncertain)
}

func samePhaseCheckpoint(a, b *phaseCheckpoint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Format == b.Format && a.Revision == b.Revision && a.Digest == b.Digest && bytes.Equal(a.Bytes, b.Bytes)
}

type phaseInvocation struct {
	binding         policy.Binding
	pin             Pin
	request         phaseRequest
	targets         []phaseTarget
	dispatchCeiling int64
	attempts        int64
	reboots         int64
}

type phaseRunner struct {
	approval                 phaseApproval
	retention                *Retention
	executor                 *executer.LinuxPhaseExecutor
	selfContainedCheckpoints bool
}

func (r *phaseRunner) runPhase(ctx context.Context, invocation phaseInvocation) (result phaseResult, err error) {
	if !r.selfContainedCheckpoints {
		return phaseResult{}, fmt.Errorf("%w: approved self-contained checkpoint contract required", executer.ErrPhaseUnsupported)
	}
	select {
	case r.retention.cache.phaseGate <- struct{}{}:
		defer func() { <-r.retention.cache.phaseGate }()
	case <-ctx.Done():
		return phaseResult{}, ctx.Err()
	}
	descriptor, err := policy.DecodeDescriptor(invocation.binding)
	if err != nil {
		return phaseResult{}, err
	}
	if invocation.pin.Reference != descriptor.PackageReference() {
		return phaseResult{}, ErrPinConflict
	}
	req := invocation.request
	grant, err := minimalPhaseGrant(descriptor, req.Identity.Action)
	if err != nil {
		return phaseResult{}, err
	}
	if err := checkInvocation(invocation, grant, descriptor.Limits); err != nil {
		return phaseResult{}, err
	}
	payload, err := r.retention.acquireExecution(ctx, invocation.pin)
	if err != nil {
		return phaseResult{}, err
	}
	defer func() {
		err = errors.Join(err, payload.close(phaseOutcomeUncertain(err)))
		if err != nil {
			result = phaseResult{}
		}
	}()
	validation, err := newPhaseValidationContext(payload.manifest, descriptor, req, invocation.targets)
	if err != nil {
		return phaseResult{}, err
	}
	requestBytes, err := validation.encodeRequest()
	if err != nil {
		return phaseResult{}, err
	}
	store := phaseRecordStore{dir: payload.state}
	record, err := store.load()
	if err != nil {
		return phaseResult{}, err
	}
	if record.Pending != nil {
		return phaseResult{}, errPhasePending
	}
	if err := payload.project(ctx, req.Identity.AttemptID, requestBytes); err != nil {
		return phaseResult{}, err
	}
	job, err := r.executor.PreparePhase(ctx, phaseExecutionSpec(payload.root.Path(), payload.manifest.Entrypoint, req.Identity.Action, grant, descriptor.Limits))
	if err != nil {
		return phaseResult{}, err
	}
	return executeRecordedPhase(ctx, store, r.approval, job, validation, invocation.dispatchCeiling)
}

func minimalPhaseGrant(d *policy.ReleaseDescriptor, action phaseAction) (*policy.AccessGrant, error) {
	var grant *policy.AccessGrant
	switch action {
	case phasePrepare:
		grant = d.Access.Prepare
	case phaseVerify:
		grant = d.Access.Verify
	default:
		return nil, errors.New("firmware phase is disabled")
	}
	if grant == nil || len(grant.NetworkRepositories)+len(grant.CredentialRepositories)+len(grant.ReadPaths)+len(grant.WritePaths)+len(grant.ReadDevices)+len(grant.WriteDevices) != 0 {
		return nil, fmt.Errorf("%w: firmware access grant exceeds the minimal Linux profile", executer.ErrPhaseUnsupported)
	}
	return grant, nil
}

func checkInvocation(input phaseInvocation, grant *policy.AccessGrant, limits policy.ExecutionLimits) error {
	pin := input.pin
	id := input.request.Identity
	if pin.Role != PinRoleActive || id.AttemptID != pin.AttemptID || id.ManifestDigest != pin.ManifestDigest ||
		id.PackageDigest != pin.PackageDigest || id.DescriptorDigest != pin.DescriptorDigest ||
		input.binding.DescriptorDigest != pin.DescriptorDigest {
		return ErrPinConflict
	}
	if input.attempts < 1 || input.attempts > limits.MaxAttempts || input.reboots < 0 || input.reboots > limits.MaxReboots {
		return errors.New("firmware attempt or reboot budget is exhausted")
	}
	data, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	if policy.Hash(data) != id.GrantDigest {
		return errors.New("firmware phase access grant differs from the pinned authorization")
	}
	return nil
}

func phaseExecutionSpec(root, entrypoint string, action phaseAction, grant *policy.AccessGrant, limits policy.ExecutionLimits) executer.PhaseExecSpec {
	return executer.PhaseExecSpec{
		RootDir: root, Entrypoint: "/" + entrypoint,
		Args:         []string{"--protocol", "1", "--action", string(action), "--request", "/request.json"},
		WritableWork: grant.WorkspaceWrite,
		Limits: executer.PhaseExecLimits{
			ResultBytes: limits.MaxResultBytes, StderrBytes: limits.MaxStderrBytes,
			ScratchBytes: limits.MaxScratchBytes, MemoryBytes: limits.MaxMemoryBytes, Processes: limits.MaxProcesses,
			Invocation: time.Duration(limits.MaxInvocationSeconds) * time.Second,
			Cleanup:    5 * time.Second,
		},
	}
}
