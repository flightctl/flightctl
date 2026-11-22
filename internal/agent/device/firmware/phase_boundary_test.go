//go:build linux

package firmware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	receiver "github.com/flightctl/flightctl/internal/agent/device/firmwarepolicy"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--internal-phase-launcher" {
		if executer.RunPhaseLauncher(os.Args[1:]) != nil {
			os.Exit(20)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "--protocol" {
		os.Exit(checkPhaseHelper())
	}
	os.Exit(m.Run())
}

func checkPhaseHelper() int {
	if len(os.Args) != 7 {
		return 10
	}
	action := os.Args[4]
	if (action != "prepare" && action != "verify") ||
		!slices.Equal(os.Args, []string{"/payload/updater", "--protocol", "1", "--action", action, "--request", "/request.json"}) {
		return 10
	}
	raw, err := os.ReadFile("/request.json")
	if err != nil {
		return 11
	}
	var req phaseRequest
	if json.Unmarshal(raw, &req) != nil || string(req.Config) != `{"value":1.25}` ||
		req.Checkpoint == nil || string(req.Checkpoint.Bytes) != `{ "step": 1 }` {
		return 12
	}
	if action == "prepare" {
		source, err := io.ReadAll(os.Stdin)
		if err != nil || string(source) != "synthetic-artifact" {
			return 12
		}
	} else if !bytes.Equal(req.Observation, []byte(`{"boot":"active"}`)) {
		return 12
	}
	result := phaseResult{Identity: req.Identity}
	target := fixturePhaseTarget()
	if action == "prepare" {
		result.Kind = phasePrepared
		result.OSReadiness = "Current"
		selected, err := json.Marshal([]phaseTarget{target})
		if err != nil {
			return 13
		}
		digest := policy.Hash(selected)
		result.Prepared = &phasePreparedEvidence{
			Artifacts: []phaseTarget{target}, SelectionDigest: digest,
			Authentication: &phaseSelectionReceipt{SelectionDigest: digest, Evidence: json.RawMessage(`{}`)},
			Preflight:      &phaseSelectionReceipt{SelectionDigest: digest, Evidence: json.RawMessage(`{}`)},
		}
	} else {
		result.Kind = phaseVerified
		result.Evidence = json.RawMessage(`{}`)
		if req.Identity.AttemptID != "missing-member" || req.Identity.CallID != phaseCallID(3) {
			result.Observations = []phaseObservation{{phaseTarget: target, Active: true}}
		}
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		return 13
	}
	return 0
}

func fixturePhaseTarget() phaseTarget {
	return phaseTarget{Kind: phaseComponent, Name: "system", Version: "2", Digest: policy.Hash([]byte("fixture-artifact"))}
}

func TestPhaseRunnerHelperInputs(t *testing.T) {
	if os.Getenv("FLIGHTCTL_TEST_PHASE_BOUNDARY") != "1" {
		t.Skip("requires an explicit privileged Linux profile test")
	}
	require.Equal(t, 0, os.Geteuid())
	t.Run("When revocation arrives before acceptance it should retain the previous result", func(t *testing.T) {
		testPhaseRunnerHelperInputs(t, false, false)
	})
	t.Run("When the helper omits the required member it should retain the previous result", func(t *testing.T) {
		testPhaseRunnerHelperInputs(t, true, false)
	})
	t.Run("When a completed attempt is released it should admit a new attempt", func(t *testing.T) {
		testPhaseRunnerHelperInputs(t, false, true)
	})
}

func testPhaseRunnerHelperInputs(t *testing.T, omitMember, releaseCompleted bool) {
	t.Helper()
	// A fixture beneath libexec inherits an executable SELinux label without
	// changing host enforcement or relabeling the deployment's cache.
	dataDir, err := os.MkdirTemp("/usr/libexec", "flightctl-phase-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dataDir)) })

	m, d, req := phaseProtocolFixture(t)
	if omitMember {
		req.Identity.AttemptID = "missing-member"
	}
	d.Limits.MaxProcesses = 32
	d.Limits.MaxMemoryBytes = 128 << 20
	d.Limits.MaxScratchBytes = 32 << 10
	d.Access = &policy.PhaseAccess{
		Prepare: &policy.AccessGrant{WorkspaceWrite: true, NetworkRepositories: []string{"fixture-source"}},
		Apply:   &policy.AccessGrant{}, Verify: &policy.AccessGrant{},
	}
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	raw, err := json.Marshal(cfg.Releases[0].Publisher)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &d.Publisher))
	bindingBytes, err := json.Marshal(d)
	require.NoError(t, err)
	binding := policy.Binding{Name: d.Name, Version: d.Version, Descriptor: bindingBytes, DescriptorDigest: policy.Hash(bindingBytes)}
	req.Identity.DescriptorDigest = binding.DescriptorDigest

	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	retention := NewRetention(dataDir, rw, log.NewPrefixLogger("phase-boundary"))
	cache, err := retention.openCache(true)
	require.NoError(t, err)
	t.Cleanup(cache.Close)
	pin := publishPhaseTestPayload(t, rw, dataDir, m, d, binding, cache.updaters, req.Identity.AttemptID)
	require.NoError(t, retention.Pin(pin))
	badPin := pin
	badPin.AttemptID = "other-attempt"
	_, err = retention.acquireExecution(t.Context(), badPin)
	require.ErrorIs(t, err, ErrPinConflict)
	lease, err := retention.acquireExecution(t.Context(), pin)
	require.NoError(t, err)
	leaseOpen := true
	t.Cleanup(func() {
		if leaseOpen {
			_ = lease.close(false)
		}
	})
	require.ErrorIs(t, retention.Release(pin.AttemptID), ErrPinConflict)
	err = lease.close(false)
	leaseOpen = false
	require.NoError(t, err)
	req.Identity.ManifestDigest = pin.ManifestDigest
	recordStore := phaseRecordStore{dir: cache.firmware, attemptID: req.Identity.AttemptID}
	require.NoError(t, recordStore.save(phaseRecord{AttemptID: req.Identity.AttemptID, Checkpoint: req.Checkpoint}))

	gate := newPhaseBoundaryApproval(t, dataDir, rw, binding)
	runner := phaseRunner{
		approval: gate, retention: retention, executor: &executer.LinuxPhaseExecutor{},
		launcherPath:             stagePhaseLauncher(t),
		selfContainedCheckpoints: true,
	}
	invocation := phaseInvocation{
		binding: binding, pin: pin, request: req, targets: []phaseTarget{{Kind: phaseComponent, Name: "system"}},
		attempts: 1, dispatchCeiling: 4,
	}
	observation := bytes.Clone(req.Observation)
	sourceReader, sourceWriter, err := os.Pipe()
	require.NoError(t, err)
	_, err = sourceWriter.Write([]byte("synthetic-artifact"))
	require.NoError(t, err)
	require.NoError(t, sourceWriter.Close())
	t.Cleanup(func() { require.NoError(t, sourceReader.Close()) })
	invocation.source = &phaseSource{repository: "fixture-source", file: sourceReader}
	prepare := invocation.request.Identity
	prepare.Action, prepare.PlanDigest, prepare.ObservationDigest, prepare.CallID = phasePrepare, "", "", phaseCallID(1)
	invocation.request.Observation = nil
	grant, err := json.Marshal(d.Access.Prepare)
	require.NoError(t, err)
	prepare.GrantDigest = policy.Hash(grant)
	invocation.request.Identity = prepare
	got, err := runner.runPhase(t.Context(), invocation)
	require.NoError(t, err)
	require.Equal(t, phasePrepared, got.Kind)
	require.Equal(t, prepare, got.Identity)
	require.Equal(t, []phaseTarget{fixturePhaseTarget()}, got.Prepared.Artifacts)

	verify := req.Identity
	verify.CallID, verify.PlanDigest = phaseCallID(2), got.Prepared.SelectionDigest
	invocation.source = nil
	invocation.request.Observation = observation
	grant, err = json.Marshal(d.Access.Verify)
	require.NoError(t, err)
	verify.GrantDigest = policy.Hash(grant)
	invocation.request.Identity = verify
	got, err = runner.runPhase(t.Context(), invocation)
	require.NoError(t, err)
	require.Equal(t, phaseVerified, got.Kind)
	require.Equal(t, verify, got.Identity)
	record, err := recordStore.load()
	require.NoError(t, err)
	require.EqualValues(t, 2, record.Dispatches)
	require.Equal(t, req.Checkpoint, record.Checkpoint)
	require.Nil(t, record.Pending)
	if releaseCompleted {
		require.NoError(t, retention.Release(pin.AttemptID))
		retired, err := recordStore.load()
		require.NoError(t, err)
		require.Equal(t, req.Identity.AttemptID, retired.AttemptID)
		require.ErrorIs(t, retention.Pin(pin), ErrPinConflict, "a completed attempt ID cannot be pinned again")
		newAttempt := "next-attempt"
		invocation.pin.AttemptID = newAttempt
		require.NoError(t, retention.Pin(invocation.pin))
		invocation.request.Identity.AttemptID = newAttempt
		invocation.request.Identity.CallID = phaseCallID(1)
		invocation.targets = []phaseTarget{fixturePhaseTarget()}
		newStore := phaseRecordStore{dir: cache.firmware, attemptID: newAttempt}
		require.NoError(t, newStore.save(phaseRecord{
			AttemptID: newAttempt, Checkpoint: invocation.request.Checkpoint,
		}))
		got, err := runner.runPhase(t.Context(), invocation)
		require.NoError(t, err)
		require.Equal(t, phaseVerified, got.Kind)
		require.Equal(t, newAttempt, got.Identity.AttemptID)
		stale, err := recordStore.load()
		require.NoError(t, err)
		require.Equal(t, req.Identity.AttemptID, stale.AttemptID)
		current, err := newStore.load()
		require.NoError(t, err)
		require.Equal(t, newAttempt, current.AttemptID)
		return
	}
	if !omitMember {
		gate.revokeAt = gate.calls + 2
	}
	invocation.request.Identity.CallID = phaseCallID(3)
	got, err = runner.runPhase(t.Context(), invocation)
	if omitMember {
		require.ErrorContains(t, err, "every required target")
	} else {
		require.ErrorIs(t, err, receiver.ErrUnavailable)
	}
	require.Empty(t, got.Kind)
	record, err = recordStore.load()
	require.NoError(t, err)
	require.EqualValues(t, 3, record.Dispatches)
	require.Equal(t, &invocation.request.Identity, record.Pending)
	require.Equal(t, verify, storedPhaseResult(t, record).Identity)
	require.Equal(t, req.Checkpoint, record.Checkpoint)
	require.EqualValues(t, 1, record.AcceptedReceipt.Revision)

	restarted := NewRetention(dataDir, rw, log.NewPrefixLogger("phase-restart"))
	require.ErrorIs(t, restarted.Release(pin.AttemptID), ErrPinConflict)
	next := phaseRunner{
		approval: gate, retention: restarted, executor: &executer.LinuxPhaseExecutor{},
		launcherPath: runner.launcherPath, selfContainedCheckpoints: true,
	}
	invocation.request.Identity.CallID = phaseCallID(4)
	_, err = next.runPhase(t.Context(), invocation)
	require.ErrorIs(t, err, errPhasePending)
	reopened, err := recordStore.load()
	require.NoError(t, err)
	require.EqualValues(t, 3, reopened.Dispatches)
}

func stagePhaseLauncher(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(executable)
	require.NoError(t, err)
	dir, err := os.MkdirTemp("/usr/libexec", "flightctl-phase-launcher-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o755))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	path := filepath.Join(dir, "phase-launcher")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, os.Chmod(path, 0o555))
	return path
}

type phaseBoundaryApproval struct {
	manager    *receiver.Manager
	revocation policy.View
	calls      int
	revokeAt   int
}

func newPhaseBoundaryApproval(t *testing.T, dir string, rw fileio.ReadWriter, binding policy.Binding) *phaseBoundaryApproval {
	t.Helper()
	manager, err := receiver.New("fixture-device", dir, rw, client.NewMockManagement(gomock.NewController(t)), log.NewPrefixLogger("phase-approval"))
	require.NoError(t, err)
	first, err := policy.New(1, []policy.Binding{binding}, nil)
	require.NoError(t, err)
	revocation, err := policy.New(2, []policy.Binding{binding}, []policy.Tombstone{{
		Name: binding.Name, Version: binding.Version, DescriptorDigest: binding.DescriptorDigest, Reason: "fixture", Revision: 2,
	}})
	require.NoError(t, err)
	_, err = manager.Receive(t.Context(), first)
	require.NoError(t, err)
	return &phaseBoundaryApproval{manager: manager, revocation: revocation}
}

func (gate *phaseBoundaryApproval) WithApprovedRelease(ctx context.Context, name, version, digest string, fn func(policy.Receipt) error) error {
	gate.calls++
	if gate.calls == gate.revokeAt {
		if _, err := gate.manager.Receive(ctx, gate.revocation); err != nil {
			return err
		}
	}
	return gate.manager.WithApprovedRelease(ctx, name, version, digest, fn)
}

func publishPhaseTestPayload(t *testing.T, rw fileio.ReadWriter, dir string, manifest *Manifest, descriptor *policy.ReleaseDescriptor, binding policy.Binding, cache fileio.Anchor, attemptID string) Pin {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(executable)
	require.NoError(t, err)
	sourcePath := filepath.Join(dir, "source")
	require.NoError(t, os.MkdirAll(filepath.Join(sourcePath, "updater", "payload"), 0o700))
	source, err := rw.OpenAnchor(sourcePath, fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	bundle, err := source.OpenDir("updater", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bundle.Close()) })
	payload, err := bundle.OpenDir("payload", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, payload.Close()) })
	require.NoError(t, payload.WriteFileExclusive("updater", data, 0o700))

	manifest.APIVersion, manifest.Entrypoint = ManifestAPIVersion, "payload/updater"
	manifest.Dependencies = []policy.RuntimeDependency{}
	manifest.Files = []ManifestFile{{
		Path: manifest.Entrypoint, Size: int64(len(data)), SHA256: strings.TrimPrefix(policy.Hash(data), "sha256:"), Executable: true,
	}}
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, bundle.WriteFileExclusive("manifest.json", raw, 0o600))
	limits := policy.ExtractionLimits{
		MaxExtractedManifestBytes: 16384, MaxExtractedFiles: 1,
		MaxExtractedFileBytes: int64(len(data)), MaxExtractedTotalBytes: int64(len(data)),
		ExtractionScratchBytes: int64(len(data)) + 16384, MaxExtractionSeconds: 15,
	}
	staged, err := StageTree(t.Context(), source, cache, raw, manifest, limits, runtime.GOARCH)
	require.NoError(t, err)
	defer func() { _ = staged.Discard(t.Context()) }()
	name := strings.TrimPrefix(descriptor.PackageDigest, "sha256:")
	_, err = staged.Publish(t.Context(), name)
	require.NoError(t, err)
	return Pin{
		AttemptID: attemptID, Role: PinRoleActive, Reference: descriptor.PackageReference(),
		PackageDigest: descriptor.PackageDigest, DescriptorDigest: binding.DescriptorDigest,
		ManifestDigest: policy.Hash(raw), PayloadDir: name, Limits: limits,
	}
}
