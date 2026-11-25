package firmware

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
)

// contractBundleEnv names an output directory of
// packaging/images/firmware-updater/build.sh.
const contractBundleEnv = "FLIGHTCTL_TEST_UPDATER_BUNDLE"

var (
	contractComponentDigest = policy.Hash([]byte("contract component"))
	contractTargets         = []phaseTarget{{Kind: phaseComponent, Name: "system", Version: "2", Digest: contractComponentDigest}}
)

type contractBundle struct {
	dir        string
	arch       string
	raw        []byte
	manifest   *Manifest
	descriptor *policy.ReleaseDescriptor
	binding    policy.Binding
}

func loadContractBundle(t *testing.T) contractBundle {
	t.Helper()
	dir := os.Getenv(contractBundleEnv)
	if dir == "" {
		t.Skipf("set %s to a build.sh output directory to test the built updater contract bundle", contractBundleEnv)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "context", "updater", "manifest.json"))
	require.NoError(t, err)
	manifest, err := ParseManifest(raw)
	require.NoError(t, err)
	var inputs struct {
		Name     string              `json:"name"`
		Version  string              `json:"version"`
		Protocol int                 `json:"protocol"`
		Schemas  policy.SchemaBundle `json:"schemas"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "descriptor-inputs.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &inputs))
	var report struct {
		Platform string `json:"platform"`
	}
	data, err = os.ReadFile(filepath.Join(dir, "report.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &report))

	d := &policy.ReleaseDescriptor{
		Name: inputs.Name, Version: inputs.Version, PackageDigest: policy.Hash([]byte("contract bundle package")), Protocol: inputs.Protocol,
		Schemas: inputs.Schemas, Dependencies: []policy.RuntimeDependency{},
		Access: &policy.PhaseAccess{Prepare: &policy.AccessGrant{}, Apply: &policy.AccessGrant{}, Verify: &policy.AccessGrant{}},
		Limits: policy.ExecutionLimits{MaxRequestBytes: 65536, MaxResultBytes: 65536, MaxStderrBytes: 4096, MaxCheckpointBytes: 4096,
			MaxScratchBytes: 1 << 20, MaxMemoryBytes: 128 << 20, MaxProcesses: 32, MaxInvocationSeconds: 60, MaxCycles: 3, MaxReboots: 1, MaxAttempts: 1},
		BudgetKeySet: []string{"maxCycles"},
	}
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	publisher, err := json.Marshal(cfg.Releases[0].Publisher)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(publisher, &d.Publisher))
	descriptor, err := json.Marshal(d)
	require.NoError(t, err)
	binding := policy.Binding{Name: d.Name, Version: d.Version, Descriptor: descriptor, DescriptorDigest: policy.Hash(descriptor)}
	_, err = policy.DecodeDescriptor(binding)
	require.NoError(t, err, "the bundle's descriptor inputs must form a valid release descriptor")
	return contractBundle{dir: dir, arch: strings.TrimPrefix(report.Platform, "linux/"), raw: raw, manifest: manifest, descriptor: d, binding: binding}
}

// contractRequest returns the request the agent dispatches for action, with a
// synthetic one-component selection and an input checkpoint.
func contractRequest(t *testing.T, b contractBundle, action phaseAction, callID, plan string) phaseRequest {
	t.Helper()
	config := []byte(`{"artifacts":[{"name":"system","version":"2","digest":"` + contractComponentDigest + `"}]}`)
	grant, err := json.Marshal(&policy.AccessGrant{})
	require.NoError(t, err)
	checkpoint := &phaseCheckpoint{Format: "updater-contract-fixture/v1", Revision: 1, Bytes: []byte(`{"step":1}`)}
	checkpoint.Digest = policy.Hash(checkpoint.Bytes)
	req := phaseRequest{
		Identity: phaseIdentity{
			Protocol: SupportedProtocol, Action: action, AttemptID: "contract-attempt", CallID: callID, Cycle: 1,
			Name: b.manifest.Name, Version: b.manifest.Version, DescriptorDigest: b.binding.DescriptorDigest,
			PackageDigest: b.descriptor.PackageDigest, ManifestDigest: policy.Hash(b.raw), ConfigDigest: policy.Hash(config),
			IntentDigest: policy.Hash([]byte("contract intent")), PlanDigest: plan, Schemas: b.manifest.Schemas, GrantDigest: policy.Hash(grant),
			CheckpointDigest: checkpoint.Digest, CheckpointFormat: checkpoint.Format, CheckpointRevision: checkpoint.Revision,
		},
		Config:     config,
		Checkpoint: checkpoint,
	}
	if action == phaseVerify {
		req.Observation = []byte(`{"components":[{"name":"system","version":"2","digest":"` + contractComponentDigest + `","active":true}]}`)
		req.Identity.ObservationDigest = policy.Hash(req.Observation)
	}
	return req
}

func TestContractBundleRequestSchemaRejectsApply(t *testing.T) {
	b := loadContractBundle(t)
	encode := func(action phaseAction) error {
		validation, err := newPhaseValidationContext(b.manifest, b.descriptor, contractRequest(t, b, action, "1", ""), contractTargets)
		require.NoError(t, err)
		_, err = validation.encodeRequest()
		return err
	}
	require.NoError(t, encode(phasePrepare), "When prepare is dispatched the bundle's request schema should accept it")
	require.ErrorContains(t, encode(phaseApply), "invalid phase request", "When apply is dispatched the bundle's request schema should reject it")
}

// TestContractBundleSpeaksTheAgentProtocol runs the packaged executable
// directly, outside the bounded runner, to check protocol compatibility: the
// bundle's schemas accept the agent's encoded requests and the agent accepts
// the bundle's results.
func TestContractBundleSpeaksTheAgentProtocol(t *testing.T) {
	b := loadContractBundle(t)
	if b.arch != runtime.GOARCH {
		t.Skipf("the bundle targets linux/%s; executing it needs a matching native host", b.arch)
	}
	executable := filepath.Join(b.dir, "context", "updater", filepath.FromSlash(b.manifest.Entrypoint))
	run := func(t *testing.T, req phaseRequest) phaseResult {
		t.Helper()
		validation, err := newPhaseValidationContext(b.manifest, b.descriptor, req, contractTargets)
		require.NoError(t, err)
		data, err := validation.encodeRequest()
		require.NoError(t, err, "the bundle's request schema must accept the agent's encoded request")
		path := filepath.Join(t.TempDir(), "request.json")
		require.NoError(t, os.WriteFile(path, data, 0o600))
		cmd := exec.Command(executable, "--protocol", "1", "--action", string(req.Identity.Action), "--request", path) //nolint:gosec // G204: the packaged contract fixture under test with fixed protocol arguments
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		require.NoError(t, cmd.Run(), stderr.String())
		result, err := decodePhaseResult(stdout.Bytes(), validation)
		require.NoError(t, err, "the agent must accept the bundle's result")
		require.Equal(t, req.Identity, result.Identity)
		return result
	}
	var plan string
	t.Run("When prepare runs it should return the agent-approved selection", func(t *testing.T) {
		got := run(t, contractRequest(t, b, phasePrepare, "1", ""))
		require.Equal(t, phasePrepared, got.Kind)
		require.Equal(t, contractTargets, got.Prepared.Artifacts)
		plan = got.Prepared.SelectionDigest
	})
	t.Run("When verify observes the selection active it should return Verified for every target", func(t *testing.T) {
		require.NotEmpty(t, plan)
		got := run(t, contractRequest(t, b, phaseVerify, "2", plan))
		require.Equal(t, phaseVerified, got.Kind)
		require.Equal(t, []phaseObservation{{phaseTarget: contractTargets[0], Active: true}}, got.Observations)
	})
}

func TestContractBundleRunsThroughTheBoundedRunner(t *testing.T) {
	b := loadContractBundle(t)
	if os.Getenv("FLIGHTCTL_TEST_PHASE_BOUNDARY") != "1" {
		t.Skip("requires an explicit privileged Linux profile test")
	}
	require.Equal(t, 0, os.Geteuid())
	if b.arch != runtime.GOARCH {
		t.Skipf("the bundle targets linux/%s; executing it needs a matching native host", b.arch)
	}
	// A data directory beneath libexec inherits an executable SELinux label
	// without changing host enforcement or relabeling the deployment's cache.
	dataDir, err := os.MkdirTemp("/usr/libexec", "flightctl-contract-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dataDir)) })

	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	retention := NewRetention(dataDir, rw, log.NewPrefixLogger("contract-bundle"))
	cache, err := retention.openCache(true)
	require.NoError(t, err)
	t.Cleanup(cache.Close)
	source, err := rw.OpenAnchor(filepath.Join(b.dir, "context"), fileio.DirOptions{ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	limits := policy.ExtractionLimits{
		MaxExtractedManifestBytes: 65536, MaxExtractedFiles: 16, MaxExtractedFileBytes: 8 << 20,
		MaxExtractedTotalBytes: 16 << 20, ExtractionScratchBytes: 17 << 20, MaxExtractionSeconds: 60,
	}
	staged, err := StageTree(t.Context(), source, cache.updaters, b.raw, b.manifest, limits, runtime.GOARCH)
	require.NoError(t, err)
	defer func() { _ = staged.Discard(t.Context()) }()
	name := strings.TrimPrefix(b.descriptor.PackageDigest, "sha256:")
	_, err = staged.Publish(t.Context(), name)
	require.NoError(t, err)

	const attempt = "contract-attempt"
	pin := Pin{
		AttemptID: attempt, Role: PinRoleActive, Reference: b.descriptor.PackageReference(), PackageDigest: b.descriptor.PackageDigest,
		DescriptorDigest: b.binding.DescriptorDigest, ManifestDigest: policy.Hash(b.raw), PayloadDir: name, Limits: limits,
	}
	require.NoError(t, retention.Pin(pin))
	prepare := contractRequest(t, b, phasePrepare, phaseCallID(1), "")
	store := phaseRecordStore{dir: cache.firmware, attemptID: attempt}
	require.NoError(t, store.save(phaseRecord{AttemptID: attempt, Checkpoint: prepare.Checkpoint}))
	runner := phaseRunner{
		approval: newPhaseBoundaryApproval(t, dataDir, rw, b.binding), retention: retention, executor: &executer.LinuxPhaseExecutor{},
		launcherPath: stagePhaseLauncher(t), selfContainedCheckpoints: true,
	}
	invocation := phaseInvocation{binding: b.binding, pin: pin, request: prepare, targets: contractTargets, attempts: 1, dispatchCeiling: 4}

	got, err := runner.runPhase(t.Context(), invocation)
	require.NoError(t, err, "When the verified extracted bundle runs prepare through the bounded runner it should be accepted")
	require.Equal(t, phasePrepared, got.Kind)
	require.Equal(t, prepare.Identity, got.Identity)
	invocation.request = contractRequest(t, b, phaseVerify, phaseCallID(2), got.Prepared.SelectionDigest)
	got, err = runner.runPhase(t.Context(), invocation)
	require.NoError(t, err, "When verify follows the durable selection it should be accepted")
	require.Equal(t, phaseVerified, got.Kind)
	record, err := store.load()
	require.NoError(t, err)
	require.EqualValues(t, 2, record.Dispatches)
	require.Nil(t, record.Pending)
}
