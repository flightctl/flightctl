package firmware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	if store.attemptID != id.AttemptID {
		return phaseResult{}, errors.New("firmware runner record is scoped to a different attempt")
	}
	record, err := store.load()
	if err != nil {
		return phaseResult{}, err
	}
	if record.Retired {
		return phaseResult{}, errPhaseRetired
	}
	if record.Pending != nil {
		return phaseResult{}, errPhasePending
	}
	if record.AttemptID == "" {
		record.AttemptID = id.AttemptID
	}
	if len(record.Accepted) == 0 && len(record.CheckpointRaw) == 0 && validation.request.Checkpoint != nil {
		record.Checkpoint = validation.request.Checkpoint
		record.CheckpointRaw, err = json.Marshal(validation.request.Checkpoint)
		if err != nil {
			return phaseResult{}, fmt.Errorf("encode initial firmware checkpoint: %w", err)
		}
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
	var checkpointRaw, selectedRaw json.RawMessage
	if accepted.Checkpoint != nil {
		checkpointRaw, err = phaseResultRawField(out.Result, "checkpoint")
		if err != nil || len(checkpointRaw) == 0 {
			return phaseResult{}, errors.New("accepted phase checkpoint is missing its wire value")
		}
	}
	if accepted.Prepared != nil {
		selectedRaw, err = phaseResultRawField(out.Result, "prepared")
		if err != nil || len(selectedRaw) == 0 {
			return phaseResult{}, errors.New("accepted phase selection is missing its wire value")
		}
	}
	err = approval.WithApprovedRelease(ctx, id.Name, id.Version, id.DescriptorDigest, func(receipt policy.Receipt) error {
		record.Pending = nil
		record.PendingReceipt = nil
		record.Accepted = bytes.Clone(out.Result)
		record.AcceptedReceipt = &receipt
		if accepted.Checkpoint != nil {
			record.Checkpoint = accepted.Checkpoint
			record.CheckpointRaw = checkpointRaw
		}
		if accepted.Prepared != nil {
			record.Selected = accepted.Prepared
			record.SelectedRaw = selectedRaw
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
	if record.Retired {
		return errPhaseRetired
	}
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
	source          *phaseSource
	dispatchCeiling int64
	attempts        int64
	reboots         int64
}

type phaseSource struct {
	repository string
	file       *os.File
}

type phaseRunner struct {
	approval                 phaseApproval
	retention                *Retention
	executor                 *executer.LinuxPhaseExecutor
	launcherPath             string
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
	grant, err := minimalPhaseGrant(descriptor, req.Identity.Action, invocation.source)
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
	store := phaseRecordStore{dir: payload.state, attemptID: req.Identity.AttemptID}
	record, err := store.load()
	if err != nil {
		return phaseResult{}, err
	}
	if record.Retired {
		return phaseResult{}, errPhaseRetired
	}
	if record.Pending != nil {
		return phaseResult{}, errPhasePending
	}
	if err := payload.project(ctx, req.Identity.AttemptID, requestBytes); err != nil {
		return phaseResult{}, err
	}
	spec, err := phaseExecutionSpec(payload.root.Path(), payload.manifest.Entrypoint, req.Identity.Action, grant, invocation.source, r.launcherPath, descriptor.Limits)
	if err != nil {
		return phaseResult{}, err
	}
	job, err := r.executor.PreparePhase(ctx, spec)
	if err != nil {
		return phaseResult{}, err
	}
	return executeRecordedPhase(ctx, store, r.approval, job, validation, invocation.dispatchCeiling)
}

func minimalPhaseGrant(d *policy.ReleaseDescriptor, action phaseAction, source *phaseSource) (*policy.AccessGrant, error) {
	var grant *policy.AccessGrant
	switch action {
	case phasePrepare:
		grant = d.Access.Prepare
	case phaseVerify:
		grant = d.Access.Verify
	default:
		return nil, errors.New("firmware phase is disabled")
	}
	if grant == nil || len(grant.CredentialRepositories)+len(grant.ReadPaths)+len(grant.WritePaths)+len(grant.ReadDevices)+len(grant.WriteDevices) != 0 {
		return nil, fmt.Errorf("%w: firmware access grant exceeds the minimal Linux profile", executer.ErrPhaseUnsupported)
	}
	switch action {
	case phasePrepare:
		if len(grant.NetworkRepositories) > 1 ||
			(len(grant.NetworkRepositories) == 0 && source != nil) ||
			(len(grant.NetworkRepositories) == 1 && (source == nil || source.file == nil || source.repository != grant.NetworkRepositories[0])) {
			return nil, fmt.Errorf("%w: prepare source does not match its admitted repository grant", executer.ErrPhaseUnsupported)
		}
	case phaseVerify:
		if source != nil || len(grant.NetworkRepositories) != 0 || grant.WorkspaceWrite {
			return nil, fmt.Errorf("%w: verify cannot receive source access or workspace writes", executer.ErrPhaseUnsupported)
		}
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

func phaseExecutionSpec(root, entrypoint string, action phaseAction, grant *policy.AccessGrant, source *phaseSource, launcher string, limits policy.ExecutionLimits) (executer.PhaseExecSpec, error) {
	if launcher == "" {
		launcher = executer.DefaultPhaseLauncherPath
	}
	launcher, err := filepath.EvalSymlinks(launcher)
	if err != nil {
		return executer.PhaseExecSpec{}, fmt.Errorf("resolve phase launcher path: %w", err)
	}
	namespaces, err := executer.CurrentPhaseNamespaceIdentity()
	if err != nil {
		return executer.PhaseExecSpec{}, err
	}
	hostNamespaces, err := executer.CurrentHostPhaseNamespaceIdentity()
	if err != nil {
		return executer.PhaseExecSpec{}, err
	}
	if namespaces != hostNamespaces {
		return executer.PhaseExecSpec{}, fmt.Errorf("%w: agent is not in the PID 1 system-manager namespace baseline", executer.ErrPhaseUnsupported)
	}
	spec := executer.PhaseExecSpec{
		RootDir: root, Entrypoint: "/" + entrypoint,
		Args:         []string{"--protocol", "1", "--action", string(action), "--request", "/request.json"},
		WritableWork: grant.WorkspaceWrite,
		LauncherPath: launcher, HostNamespaces: hostNamespaces,
		Limits: executer.PhaseExecLimits{
			ResultBytes: limits.MaxResultBytes, StderrBytes: limits.MaxStderrBytes,
			ScratchBytes: limits.MaxScratchBytes, MemoryBytes: limits.MaxMemoryBytes, Processes: limits.MaxProcesses,
			Invocation: time.Duration(limits.MaxInvocationSeconds) * time.Second,
			Cleanup:    5 * time.Second,
		},
	}
	if source != nil {
		spec.SourceFD = source.file
	}
	return spec, nil
}
