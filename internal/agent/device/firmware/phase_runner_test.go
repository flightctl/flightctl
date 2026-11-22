package firmware

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/stretchr/testify/require"
)

type phaseTestApproval struct {
	revoked bool
}

func (p *phaseTestApproval) WithApprovedRelease(_ context.Context, _, _, _ string, fn func(policy.Receipt) error) error {
	if p.revoked {
		return errors.New("release revoked")
	}
	return fn(policy.Receipt{Revision: 1, Digest: policy.Hash([]byte("policy"))})
}

type phaseTestJob struct {
	onStart      func() error
	onWait       func() (executer.PhaseExecOutput, error)
	starts       int
	cleanupError error
}

func (j *phaseTestJob) Start(context.Context) error {
	j.starts++
	if j.onStart != nil {
		return j.onStart()
	}
	return nil
}

func (j *phaseTestJob) Wait(context.Context) (executer.PhaseExecOutput, error) {
	return j.onWait()
}

func (j *phaseTestJob) Close() error { return j.cleanupError }

func TestPhaseRunnerDurableAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		revoke     bool
		lost       bool
		cleanup    bool
		largeLimit bool
	}{
		{name: "When a verified result is approved it should accept it after recording dispatch"},
		{name: "When policy revokes the release during execution it should hold the call", revoke: true},
		{name: "When a dispatch loses its result restart should keep the spent budget", lost: true},
		{name: "When cleanup cannot be proven a valid result should remain pending", cleanup: true},
		{name: "When the approved result might exceed durable storage it should refuse dispatch", largeLimit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, d, req := phaseProtocolFixture(t)
			if tc.largeLimit {
				d.Limits.MaxResultBytes = 3 << 20
				d.Limits.MaxCheckpointBytes = 2 << 20
			}
			req.Identity.CallID = phaseCallID(1)
			member := phaseTarget{Kind: phaseComponent, Name: "system", Version: "2", Digest: policy.Hash([]byte("artifact"))}
			validation, err := newPhaseValidationContext(m, d, req, []phaseTarget{member})
			require.NoError(t, err)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			dir, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, dir.Close()) })
			store := phaseRecordStore{dir: dir}
			previous := phaseRecord{AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint}
			require.NoError(t, store.save(previous))
			p := &phaseTestApproval{}
			job := &phaseTestJob{
				onStart: func() error {
					current, err := store.load()
					require.NoError(t, err)
					require.Equal(t, &req.Identity, current.Pending)
					require.EqualValues(t, 1, current.Dispatches)
					return nil
				},
				onWait: func() (executer.PhaseExecOutput, error) {
					p.revoked = tc.revoke
					if tc.lost {
						return executer.PhaseExecOutput{}, errors.New("lost phase worker")
					}
					value := phaseResult{
						Identity: req.Identity, Kind: phaseVerified,
						Evidence:     json.RawMessage(`{}`),
						Observations: []phaseObservation{{phaseTarget: member, Active: true}},
					}
					data, err := json.Marshal(value)
					return executer.PhaseExecOutput{Result: data}, err
				},
			}
			if tc.cleanup {
				job.cleanupError = executer.ErrPhaseCleanup
			}
			result, err := executeRecordedPhase(t.Context(), store, p, job, validation, 2)
			current, readErr := store.load()
			require.NoError(t, readErr)
			if tc.largeLimit {
				require.ErrorContains(t, err, "exceeds durable runner storage")
				require.Equal(t, 0, job.starts)
				require.EqualValues(t, 0, current.Dispatches)
				require.Nil(t, current.Pending)
				require.Equal(t, previous.Checkpoint, current.Checkpoint)
				return
			}
			require.EqualValues(t, 1, current.Dispatches)
			require.Equal(t, previous.Checkpoint, current.Checkpoint)
			if tc.revoke || tc.lost || tc.cleanup {
				require.Error(t, err)
				require.Empty(t, result.Kind)
				require.Equal(t, &req.Identity, current.Pending)
				p.revoked = false
				_, err = executeRecordedPhase(t.Context(), phaseRecordStore{dir: dir}, p, job, validation, 2)
				require.ErrorIs(t, err, errPhasePending)
				require.Equal(t, 1, job.starts)
			} else {
				require.NoError(t, err)
				require.Equal(t, phaseVerified, result.Kind)
				require.Nil(t, current.Pending)
				require.Equal(t, phaseVerified, current.Accepted.Kind)
			}
		})
	}
}

func TestPhaseProjectionStopsOnUnresolvedSetup(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	state, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	parent, err := state.OpenDir("phase-roots", fileio.DirOptions{Create: true, Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, parent.Close()) })
	orphan, err := parent.OpenDir("active", fileio.DirOptions{Create: true, Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, orphan.Close()) })
	require.NoError(t, orphan.WriteFileExclusive("fixture", []byte("previous"), 0o600))
	next := verifiedExecution{state: state, manifest: &Manifest{}}
	require.ErrorIs(t, next.project(t.Context(), "attempt", []byte("new request")), errPhasePending)
	if next.roots != nil {
		require.NoError(t, next.roots.Close())
	}
	data, err := orphan.ReadFile("fixture", 32)
	require.NoError(t, err)
	require.Equal(t, "previous", string(data))
}

func TestPhaseRunnerRejectsChangedAttemptIdentity(t *testing.T) {
	_, _, req := phaseProtocolFixture(t)
	req.Identity.CallID = phaseCallID(2)
	record := phaseRecord{
		AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint, Dispatches: 1,
		Binding: phaseBinding(req.Identity),
	}
	limits := policy.ExecutionLimits{MaxCycles: 2}
	require.NoError(t, checkRecordedPhase(record, phaseValidationContext{request: req, limits: limits}, 3))
	for _, edit := range []func(*phaseIdentity){
		func(id *phaseIdentity) { id.ConfigDigest = policy.Hash(nil) },
		func(id *phaseIdentity) { id.IntentDigest = policy.Hash(nil) },
		func(id *phaseIdentity) { id.ManifestDigest = policy.Hash(nil) },
		func(id *phaseIdentity) { id.DescriptorDigest = policy.Hash(nil) },
	} {
		changed := req
		edit(&changed.Identity)
		err := checkRecordedPhase(record, phaseValidationContext{request: changed, limits: limits}, 3)
		require.ErrorContains(t, err, "durable runner input")
	}
}

type phaseFaultAnchor struct {
	fileio.Anchor
	writes int
}

func (a *phaseFaultAnchor) ReplaceFileDurable(name string, data []byte, mode fs.FileMode) error {
	if err := a.Anchor.ReplaceFileDurable(name, data, mode); err != nil {
		return err
	}
	a.writes++
	if a.writes == 2 {
		return errors.New("failure after visible result replacement")
	}
	return nil
}

func TestPhaseRunnerHoldsProjectionOnUncertainResultWrite(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	state, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	m, d, req := phaseProtocolFixture(t)
	req.Identity.CallID = phaseCallID(1)
	ctx, err := newPhaseValidationContext(m, d, req, nil)
	require.NoError(t, err)
	require.NoError(t, (phaseRecordStore{dir: state}).save(phaseRecord{AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint}))
	execution := verifiedExecution{state: state, manifest: &Manifest{}}
	require.NoError(t, execution.project(t.Context(), req.Identity.AttemptID, []byte("fixture")))
	var job phaseTestJob
	job.onWait = func() (executer.PhaseExecOutput, error) {
		data, err := json.Marshal(phaseResult{Identity: req.Identity, Kind: phasePending})
		return executer.PhaseExecOutput{Result: data}, err
	}
	got, err := executeRecordedPhase(t.Context(), phaseRecordStore{dir: &phaseFaultAnchor{Anchor: state}}, &phaseTestApproval{}, &job, ctx, 2)
	require.ErrorIs(t, err, errPhaseDurabilityUncertain)
	require.Empty(t, got.Kind)
	require.True(t, phaseOutcomeUncertain(err))
	require.NoError(t, execution.close(phaseOutcomeUncertain(err)))

	reopened, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	visible, err := (phaseRecordStore{dir: reopened}).load()
	require.NoError(t, err)
	require.Nil(t, visible.Pending, "the injected failure occurred after replacement")
	next := verifiedExecution{state: reopened, manifest: &Manifest{}}
	require.ErrorIs(t, next.project(t.Context(), "attempt", []byte("next")), errPhasePending)
	require.NoError(t, next.roots.Close())
}

func TestPhaseRunnerRejectsEarlierCallReplay(t *testing.T) {
	_, _, req := phaseProtocolFixture(t)
	req.Identity.CallID = phaseCallID(1)
	accepted := req.Identity
	accepted.CallID = phaseCallID(2)
	record := phaseRecord{
		AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint, Dispatches: 2,
		Binding: phaseBinding(req.Identity), Accepted: &phaseResult{Identity: accepted, Kind: phaseVerified},
	}
	err := checkRecordedPhase(record, phaseValidationContext{request: req, limits: policy.ExecutionLimits{MaxCycles: 2}}, 3)
	require.ErrorContains(t, err, "stale or out of order")
}
