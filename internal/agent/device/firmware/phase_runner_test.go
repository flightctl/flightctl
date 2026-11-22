package firmware

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
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
				d.Limits.MaxResultBytes = 9 << 20
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
			store := phaseRecordStore{dir: dir, attemptID: req.Identity.AttemptID}
			checkpointRaw, err := json.Marshal(req.Checkpoint)
			require.NoError(t, err)
			previous := phaseRecord{AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint, CheckpointRaw: checkpointRaw}
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
				_, err = executeRecordedPhase(t.Context(), phaseRecordStore{dir: dir, attemptID: req.Identity.AttemptID}, p, job, validation, 2)
				require.ErrorIs(t, err, errPhasePending)
				require.Equal(t, 1, job.starts)
			} else {
				require.NoError(t, err)
				require.Equal(t, phaseVerified, result.Kind)
				require.Nil(t, current.Pending)
				require.Equal(t, phaseVerified, storedPhaseResult(t, current).Kind)
			}
		})
	}
}

func TestPhaseRunnerScopedSourceGrant(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer func() {
		require.NoError(t, reader.Close())
		require.NoError(t, writer.Close())
	}()
	source := &phaseSource{repository: "fixture-repository", file: reader}
	descriptor := &policy.ReleaseDescriptor{Access: &policy.PhaseAccess{
		Prepare: &policy.AccessGrant{WorkspaceWrite: true, NetworkRepositories: []string{"fixture-repository"}},
		Verify:  &policy.AccessGrant{},
	}}
	grant, err := minimalPhaseGrant(descriptor, phasePrepare, source)
	require.NoError(t, err)
	require.Equal(t, "fixture-repository", grant.NetworkRepositories[0])

	for _, tc := range []struct {
		name   string
		grant  *policy.AccessGrant
		source *phaseSource
		action phaseAction
	}{
		{name: "When the prepare grant names a different repository it should reject", action: phasePrepare,
			grant: &policy.AccessGrant{NetworkRepositories: []string{"other"}}, source: source},
		{name: "When a source is missing for a granted prepare repository it should reject", action: phasePrepare,
			grant: &policy.AccessGrant{NetworkRepositories: []string{"fixture-repository"}}},
		{name: "When verify receives a source descriptor it should reject", action: phaseVerify,
			grant: &policy.AccessGrant{}, source: source},
		{name: "When verify receives a repository grant it should reject", action: phaseVerify,
			grant: &policy.AccessGrant{NetworkRepositories: []string{"fixture-repository"}}},
		{name: "When verify receives workspace write access it should reject", action: phaseVerify,
			grant: &policy.AccessGrant{WorkspaceWrite: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			descriptor.Access.Prepare = tc.grant
			descriptor.Access.Verify = tc.grant
			_, err := minimalPhaseGrant(descriptor, tc.action, tc.source)
			require.ErrorIs(t, err, executer.ErrPhaseUnsupported)
		})
	}
}

func TestPhaseRecordCapacityDoesNotAccumulateReplacedResults(t *testing.T) {
	_, _, req := phaseProtocolFixture(t)
	padding := strings.Repeat("<", 1<<20)
	accepted := []byte(`{"identity":{},"kind":"Pending","evidence":{"padding":"` + padding + `"}}`)
	record := phaseRecord{
		AttemptID: req.Identity.AttemptID, Dispatches: 1, Binding: phaseBinding(req.Identity),
		Accepted: accepted, Pending: &req.Identity,
	}
	require.NoError(t, record.checkResultCapacity(1<<20))
	stored, err := marshalPhaseRecord(record)
	require.NoError(t, err)
	require.LessOrEqual(t, len(stored), int(phaseRecordByteFieldSize(int64(len(accepted))))+4096)
}

func TestPhaseRecordPersistsUnicodeSubobjectsWithoutExpansion(t *testing.T) {
	const target = "system"
	version := strings.Repeat("\u2028", 900_000)
	digest := policy.Hash([]byte("artifact"))
	selectionDigest := policy.Hash([]byte("selection"))
	selection := `{"artifacts":[{"kind":"component","name":"` + target + `","version":"` + version + `","digest":"` + digest + `"}],"selectionDigest":"` + selectionDigest + `","authentication":{"selectionDigest":"` + selectionDigest + `","evidence":{}},"preflight":{"selectionDigest":"` + selectionDigest + `","evidence":{}}}`
	accepted := `{"identity":{"attemptID":"unicode-attempt"},"kind":"Prepared","prepared":` + selection + `}`
	record := phaseRecord{
		AttemptID: "unicode-attempt", Dispatches: 1, Binding: phaseAttemptBinding{DescriptorDigest: "fixture"},
		Accepted: []byte(accepted), SelectedRaw: []byte(selection),
	}
	data, err := marshalPhaseRecord(record)
	require.NoError(t, err)
	base, err := marshalPhaseRecord(phaseRecord{AttemptID: record.AttemptID, Dispatches: 1})
	require.NoError(t, err)
	require.LessOrEqual(t, len(data), int(phaseRecordByteFieldSize(int64(len(accepted))+int64(len(selection))))+len(base)+256)
	require.LessOrEqual(t, len(data), maxPhaseRecordBytes)

	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	dir, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dir.Close()) })
	store := phaseRecordStore{dir: dir, attemptID: record.AttemptID}
	require.NoError(t, store.save(record))
	reloaded, err := store.load()
	require.NoError(t, err)
	require.Equal(t, version, reloaded.Selected.Artifacts[0].Version)
	require.Equal(t, record.SelectedRaw, reloaded.SelectedRaw)
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
	store := phaseRecordStore{dir: state, attemptID: req.Identity.AttemptID}
	require.NoError(t, store.save(phaseRecord{AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint}))
	execution := verifiedExecution{state: state, manifest: &Manifest{}}
	require.NoError(t, execution.project(t.Context(), req.Identity.AttemptID, []byte("fixture")))
	var job phaseTestJob
	job.onWait = func() (executer.PhaseExecOutput, error) {
		data, err := json.Marshal(phaseResult{Identity: req.Identity, Kind: phasePending})
		return executer.PhaseExecOutput{Result: data}, err
	}
	got, err := executeRecordedPhase(t.Context(), phaseRecordStore{dir: &phaseFaultAnchor{Anchor: state}, attemptID: req.Identity.AttemptID}, &phaseTestApproval{}, &job, ctx, 2)
	require.ErrorIs(t, err, errPhaseDurabilityUncertain)
	require.Empty(t, got.Kind)
	require.True(t, phaseOutcomeUncertain(err))
	require.NoError(t, execution.close(phaseOutcomeUncertain(err)))

	reopened, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	visible, err := (phaseRecordStore{dir: reopened, attemptID: req.Identity.AttemptID}).load()
	require.NoError(t, err)
	require.Nil(t, visible.Pending, "the injected failure occurred after replacement")
	next := verifiedExecution{state: reopened, manifest: &Manifest{}}
	require.ErrorIs(t, next.project(t.Context(), "attempt", []byte("next")), errPhasePending)
	require.NoError(t, next.roots.Close())
}

func storedPhaseResult(t *testing.T, record phaseRecord) phaseResult {
	t.Helper()
	var result phaseResult
	require.NoError(t, json.Unmarshal(record.Accepted, &result))
	return result
}

func TestPhaseRunnerRejectsEarlierCallReplay(t *testing.T) {
	_, _, req := phaseProtocolFixture(t)
	req.Identity.CallID = phaseCallID(1)
	accepted := req.Identity
	accepted.CallID = phaseCallID(2)
	record := phaseRecord{
		AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint, Dispatches: 2,
		Binding: phaseBinding(req.Identity), Accepted: func() json.RawMessage {
			data, err := json.Marshal(phaseResult{Identity: accepted, Kind: phaseVerified})
			require.NoError(t, err)
			return data
		}(),
	}
	err := checkRecordedPhase(record, phaseValidationContext{request: req, limits: policy.ExecutionLimits{MaxCycles: 2}}, 3)
	require.ErrorContains(t, err, "stale or out of order")
}
