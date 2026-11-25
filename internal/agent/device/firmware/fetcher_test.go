package firmware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	receiver "github.com/flightctl/flightctl/internal/agent/device/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/agent/reload"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	testDataDir       = "/var/lib/flightctl"
	testMountPoint    = "/var/lib/containers/storage/overlay/0123abcd/merged"
	sentinelAuthValue = "sentinel-registry-secret-6f1c"
)

var allowedSubcommands = []string{"pull", "image inspect", "image exists", "image mount", "image unmount"}

// fakePodman records every podman invocation and answers the subcommands the
// fetcher may use; any other subcommand fails the test.
type fakePodman struct {
	t                         *testing.T
	calls                     [][]string
	onPull                    func(args []string)
	pullStderr                string
	pullExit                  int
	digest                    string
	repoDigests               []string
	inspectOutput             string
	inspectExit               int
	mountExit                 int
	unmountStderr             string
	unmountExit, existsResult int
	pullDeadline              time.Time
}

func (p *fakePodman) execute(ctx context.Context, _ string, args ...string) (string, string, int) {
	p.calls = append(p.calls, append([]string(nil), args...))
	switch subcommand(args) {
	case "pull":
		p.pullDeadline, _ = ctx.Deadline()
		if p.onPull != nil {
			p.onPull(args)
		}
		if p.pullExit != 0 {
			return "", p.pullStderr, p.pullExit
		}
		return "sha256:0f0e0d0c\n", "", 0
	case "image inspect":
		if p.inspectExit != 0 {
			return "", "Error: image not known", p.inspectExit
		}
		if p.inspectOutput != "" {
			return p.inspectOutput + "\n", "", 0
		}
		if !slices.Contains(args, "{{json .}}") {
			p.t.Errorf("unexpected image inspect format: %v", args)
			return "", "unexpected format", 125
		}
		out, err := json.Marshal(map[string]any{"Id": "0f0e0d0c", "Digest": p.digest, "RepoDigests": p.repoDigests})
		require.NoError(p.t, err)
		return string(out) + "\n", "", 0
	case "image mount":
		if p.mountExit != 0 {
			return "", "Error: mount failed", p.mountExit
		}
		return testMountPoint + "\n", "", 0
	case "image unmount":
		return "", p.unmountStderr, p.unmountExit
	case "image exists":
		return "", "", p.existsResult
	}
	p.t.Errorf("unexpected podman command: %v", args)
	return "", "unexpected command", 125
}

func (p *fakePodman) subcommands() []string {
	names := make([]string, 0, len(p.calls))
	for _, call := range p.calls {
		names = append(names, subcommand(call))
	}
	return names
}

func subcommand(args []string) string {
	if len(args) >= 2 && args[0] == "image" {
		return "image " + args[1]
	}
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

type fetchHarness struct {
	t        *testing.T
	root     string
	rw       fileio.ReadWriter
	podman   *fakePodman
	client   *client.Podman
	logger   *log.PrefixLogger
	logs     *bytes.Buffer
	resolver dependency.PullConfigResolver
	cfg      *config.Config
	release  catalog.ReleaseBinding
	binding  policy.Binding
	manifest []byte
}

func baseRelease(t *testing.T) catalog.ReleaseBinding {
	t.Helper()
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	return cfg.Releases[0]
}

func bindRelease(t *testing.T, r catalog.ReleaseBinding) policy.Binding {
	t.Helper()
	data, err := json.Marshal(r)
	require.NoError(t, err)
	return policy.Binding{Name: r.Name, Version: r.Version, DescriptorDigest: policy.Hash(data), Descriptor: data}
}

func manifestFor(r catalog.ReleaseBinding) map[string]any {
	return map[string]any{
		"apiVersion":   firmware.ManifestAPIVersion,
		"name":         r.Name,
		"version":      r.Version,
		"protocol":     1,
		"entrypoint":   "payload/bin/updater",
		"dependencies": []any{},
		"schemas": map[string]any{
			"config":      r.Schemas.Config.Digest,
			"observation": r.Schemas.Observation.Digest,
			"request":     r.Schemas.Request.Digest,
			"result":      r.Schemas.Result.Digest,
			"checkpoint":  r.Schemas.Checkpoint.Digest,
		},
		"files": []any{map[string]any{"path": "payload/bin/updater", "sha256": strings.Repeat("a", 64), "size": 23, "executable": true}},
	}
}

func allowWorkspace() config.UpdaterAccessPolicy {
	return config.UpdaterAccessPolicy{Prepare: config.UpdaterAccessGrant{WorkspaceWrite: true}}
}

// newFetchHarness provisions a test root with an accepted policy approving the
// catalog fixture release, an image mount holding its manifest, and a local
// access policy that admits the release's requested access.
func newFetchHarness(t *testing.T) *fetchHarness {
	t.Helper()
	root := t.TempDir()
	logs := &bytes.Buffer{}
	logger := log.NewPrefixLogger("firmware-test")
	logger.SetOutput(logs)
	logger.SetLevel(logrus.TraceLevel)
	h := &fetchHarness{
		t:       t,
		root:    root,
		rw:      fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root))),
		logger:  logger,
		logs:    logs,
		release: baseRelease(t),
		cfg:     config.NewDefault(),
	}
	h.podman = &fakePodman{t: t, digest: h.release.PackageDigest, existsResult: 1}
	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).DoAndReturn(h.podman.execute).AnyTimes()
	h.client = client.NewPodman(logger, exec, h.rw, poll.Config{BaseDelay: time.Millisecond, Factor: 1, MaxSteps: 1})
	h.resolver = dependency.NewPullConfigResolver(logger, fileio.NewReadWriterFactory(root))
	h.resolver.BeforeUpdate(&v1beta1.DeviceSpec{})
	h.cfg.Firmware.UpdaterAccess = allowWorkspace()
	h.approve(h.release)
	h.setManifest(manifestFor(h.release))
	return h
}

func (h *fetchHarness) approve(r catalog.ReleaseBinding, tombstoned ...bool) {
	h.t.Helper()
	h.release = r
	h.binding = bindRelease(h.t, r)
	var tombstones []policy.Tombstone
	revision := int64(1)
	if len(tombstoned) > 0 && tombstoned[0] {
		revision = 2
		tombstones = []policy.Tombstone{{Name: r.Name, Version: r.Version, DescriptorDigest: h.binding.DescriptorDigest, Reason: "revoked", Revision: 2}}
	}
	view, err := policy.New(revision, []policy.Binding{h.binding}, tombstones)
	require.NoError(h.t, err)
	data, err := json.Marshal(view)
	require.NoError(h.t, err)
	require.NoError(h.t, h.rw.WriteFileDurable(filepath.Join(testDataDir, "firmware", "policy.json"), data, 0o700, 0o600))
	h.podman.digest = r.PackageDigest
}

func (h *fetchHarness) setManifest(m map[string]any) {
	h.t.Helper()
	data, err := json.Marshal(m)
	require.NoError(h.t, err)
	h.setManifestBytes(data)
}

func (h *fetchHarness) setManifestBytes(data []byte) {
	h.t.Helper()
	dir := filepath.Join(h.root, testMountPoint, "updater")
	require.NoError(h.t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "manifest.json")
	// A test may replace the manifest; WriteFile reports any replacement failure.
	_ = os.Remove(path)
	require.NoError(h.t, os.WriteFile(path, data, 0o600))
	h.manifest = data
}

func (h *fetchHarness) writeAuthFile(path string) {
	h.t.Helper()
	full := filepath.Join(h.root, path)
	require.NoError(h.t, os.MkdirAll(filepath.Dir(full), 0o700))
	require.NoError(h.t, os.WriteFile(full, []byte(`{"auths":{"quay.io":{"auth":"`+sentinelAuthValue+`"}}}`), 0o600))
}

func (h *fetchHarness) fetcher(opts ...firmware.FetcherOption) *firmware.Fetcher {
	h.t.Helper()
	recv, err := receiver.New("device", testDataDir, h.rw, client.NewMockManagement(gomock.NewController(h.t)), h.logger)
	require.NoError(h.t, err)
	f, err := firmware.NewFetcher(testDataDir, h.client, recv, h.resolver, h.rw, h.cfg, h.logger, opts...)
	require.NoError(h.t, err)
	return f
}

func (h *fetchHarness) descriptorHex() string {
	return strings.TrimPrefix(h.binding.DescriptorDigest, "sha256:")
}

func (h *fetchHarness) recordPath() string {
	return filepath.Join(h.root, testDataDir, "firmware", "packages", h.descriptorHex()+".json")
}

func (h *fetchHarness) policyPath() string {
	return filepath.Join(h.root, testDataDir, "firmware", "signature-policy", h.descriptorHex()+".json")
}

func (h *fetchHarness) reference() string {
	return h.release.Publisher.Policy.SignedIdentity.DockerRepository + "@" + h.release.PackageDigest
}

func (h *fetchHarness) requireNoRecord() {
	h.t.Helper()
	_, err := os.Stat(h.recordPath())
	require.ErrorIs(h.t, err, fs.ErrNotExist, "no verified record may exist")
}

func (h *fetchHarness) requirePolicyRemoved() {
	h.t.Helper()
	_, err := os.Stat(h.policyPath())
	require.ErrorIs(h.t, err, fs.ErrNotExist, "the per-call signature policy must be removed")
}

func (h *fetchHarness) requireAllowlisted() {
	h.t.Helper()
	for _, name := range h.podman.subcommands() {
		require.Contains(h.t, allowedSubcommands, name, "podman subcommand outside the allowlist")
	}
}

// requireSamePackage compares the exported identity of two packages.
func requireSamePackage(t *testing.T, want, got *firmware.VerifiedPackage) {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(gotJSON))
}

func requireFetchError(t *testing.T, err error, reason firmware.Reason, retryable bool) {
	t.Helper()
	var fetchErr *firmware.FetchError
	require.ErrorAs(t, err, &fetchErr)
	require.Equal(t, reason, fetchErr.Reason)
	require.Equal(t, retryable, fetchErr.Retryable)
}

func signatureRejection(r catalog.ReleaseBinding) string {
	repo := r.Publisher.Policy.SignedIdentity.DockerRepository
	return fmt.Sprintf("updater %s/%s: publisher signature verification failed for %s@%s; the image must be signed by the approved publisher key for this exact repository, and the host must enable sigstore attachment discovery (use-sigstore-attachments) for %s",
		r.Name, r.Version, repo, r.PackageDigest, repo)
}

func listFiles(t *testing.T, root string) map[string]bool {
	t.Helper()
	files := map[string]bool{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files[path] = true
		}
		return nil
	}))
	return files
}

func TestFetchVerifiesAndRecordsAnApprovedUncachedRelease(t *testing.T) {
	require := require.New(t)
	h := newFetchHarness(t)
	var policyAtPull []byte
	h.podman.onPull = func(args []string) {
		data, err := os.ReadFile(h.policyPath())
		require.NoError(err, "the signature policy must exist while podman pulls")
		policyAtPull = data
		info, err := os.Stat(h.policyPath())
		require.NoError(err)
		require.Equal(os.FileMode(0o600), info.Mode().Perm())
	}
	before := listFiles(t, h.root)

	pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)

	ref := h.reference()
	require.Equal([][]string{
		{"pull", ref, "--signature-policy", h.policyPath()},
		{"image", "inspect", "--format", "{{json .}}", ref},
		{"image", "mount", ref},
		{"image", "unmount", ref},
	}, h.podman.calls)

	d, err := policy.DecodeDescriptor(h.binding)
	require.NoError(err)
	expectedPolicy, err := firmware.SignaturePolicy(d)
	require.NoError(err)
	require.Equal(expectedPolicy, policyAtPull)

	parsed, err := firmware.ParseManifest(h.manifest)
	require.NoError(err)
	requireSamePackage(t, &firmware.VerifiedPackage{
		Name:                  h.release.Name,
		Version:               h.release.Version,
		DescriptorDigest:      h.binding.DescriptorDigest,
		PackageDigest:         h.release.PackageDigest,
		Reference:             ref,
		PublisherPolicyDigest: h.release.Publisher.PolicyDigest,
		ManifestDigest:        policy.Hash(h.manifest),
		PolicyRevision:        1,
		Manifest:              *parsed,
		Cached:                false,
	}, pkg)
	_, hasBudget := pkg.ExtractionLimits()
	require.False(hasBudget, "a release without an extraction object binds no budget")

	info, err := os.Stat(h.recordPath())
	require.NoError(err)
	require.Equal(os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(h.recordPath())
	require.NoError(err)
	var record map[string]any
	require.NoError(json.Unmarshal(data, &record))
	require.Equal(h.release.Name, record["name"])
	require.Equal(h.release.Version, record["version"])
	require.Equal(h.binding.DescriptorDigest, record["descriptorDigest"])
	require.Equal(h.release.PackageDigest, record["packageDigest"])
	require.Equal(h.release.Publisher.PolicyDigest, record["publisherPolicyDigest"])
	require.Equal(policy.Hash(h.manifest), record["manifestDigest"])
	require.EqualValues(1, record["policyRevision"])
	require.NotNil(record["manifest"])

	h.requirePolicyRemoved()
	h.requireAllowlisted()
	for path := range listFiles(t, h.root) {
		if !before[path] {
			require.True(strings.HasPrefix(path, filepath.Join(h.root, testDataDir, "firmware")+"/"), "unexpected write outside the firmware data directory: %s", path)
		}
	}
}

func TestFetchRefusesReleasesThatAreNotApproved(t *testing.T) {
	testCases := []struct {
		name    string
		setup   func(h *fetchHarness)
		release func(h *fetchHarness) (string, string)
	}{
		{
			name:    "When the release has no binding it should report the updater unavailable",
			release: func(h *fetchHarness) (string, string) { return "other-updater", "1.0.0" },
		},
		{
			name:  "When the release has been revoked it should report the updater unavailable",
			setup: func(h *fetchHarness) { h.approve(h.release, true) },
		},
		{
			name: "When no policy has been accepted it should report the updater unavailable",
			setup: func(h *fetchHarness) {
				require.NoError(h.t, h.rw.RemoveFile(filepath.Join(testDataDir, "firmware", "policy.json")))
			},
		},
		{
			name: "When the retained policy is corrupt it should report the updater unavailable",
			setup: func(h *fetchHarness) {
				require.NoError(h.t, h.rw.WriteFileDurable(filepath.Join(testDataDir, "firmware", "policy.json"), []byte(`{"revision":1}`), 0o700, 0o600))
			},
		},
		{
			name: "When the descriptor violates a catalog access rule it should report the updater unavailable",
			setup: func(h *fetchHarness) {
				r := h.release
				r.Access = &catalog.PhaseAccess{Prepare: &catalog.AccessGrant{}, Apply: &catalog.AccessGrant{NetworkRepositories: []string{"vendor-firmware"}}, Verify: &catalog.AccessGrant{}}
				h.approve(r)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			name, version := h.release.Name, h.release.Version
			if tc.release != nil {
				name, version = tc.release(h)
			}
			pkg, err := h.fetcher().Fetch(context.Background(), name, version)
			require.Nil(pkg)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			require.Equal(name+"/"+version, deviceerrors.GetElement(err))
			require.Empty(h.podman.calls, "no podman command may run")
			h.requireNoRecord()
		})
	}
}

func TestFetchRejectsUnsupportedReleasesBeforeAnyPull(t *testing.T) {
	testCases := []struct {
		name  string
		setup func(h *fetchHarness)
	}{
		{
			name: "When the release uses an unsupported protocol it should report firmware not supported",
			setup: func(h *fetchHarness) {
				r := h.release
				r.Protocol = 2
				h.approve(r)
			},
		},
		{
			name: "When the release declares a runtime dependency it should report firmware not supported",
			setup: func(h *fetchHarness) {
				r := h.release
				r.Dependencies = []catalog.RuntimeDependency{{Name: "fwupd", Constraint: ">=1.9"}}
				h.approve(r)
			},
		},
		{
			name:  "When the requested access exceeds the local policy it should report firmware not supported",
			setup: func(h *fetchHarness) { h.cfg.Firmware.UpdaterAccess = config.UpdaterAccessPolicy{} },
		},
		{
			name: "When invalid local configuration left a deny-all policy it should report firmware not supported",
			setup: func(h *fetchHarness) {
				t := h.t
				root := t.TempDir()
				t.Setenv(config.TestRootDirEnvKey, root)
				dir := filepath.Join(root, config.DefaultConfigDir)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				invalid := agentConfigYAML + "firmware:\n  updater-access:\n    prepare:\n      workspace-write: true\n      write-paths: [/opt/vendor]\n"
				require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(invalid), 0o600))
				cfg, err := config.Load(config.DefaultConfigFile)
				require.NoError(t, err)
				require.NotEmpty(t, cfg.Warnings)
				h.cfg = cfg
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			tc.setup(h)
			pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.Nil(pkg)
			requireFetchError(t, err, firmware.ReasonFirmwareNotSupported, false)
			require.Equal(h.release.Name+"/"+h.release.Version, deviceerrors.GetElement(err))
			require.Empty(h.podman.calls, "no podman command may run before the release is supported")
			h.requireNoRecord()
		})
	}
}

func TestFetchFailsClosedWhenTheSignaturePolicyRejectsThePull(t *testing.T) {
	require := require.New(t)
	h := newFetchHarness(t)
	h.podman.pullExit = 125
	h.podman.pullStderr = "Error: copying system image: Source image rejected: A signature was required, but no signature exists\n"

	pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.Nil(pkg)
	requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
	var fetchErr *firmware.FetchError
	require.ErrorAs(err, &fetchErr)
	require.Equal(signatureRejection(h.release), fetchErr.Error())
	require.NotContains(fetchErr.Error(), "Source image rejected")
	require.ErrorContains(errors.Unwrap(fetchErr), "Source image rejected")
	require.Equal(h.release.Name+"/"+h.release.Version, deviceerrors.GetElement(err))
	require.Equal([]string{"pull"}, h.podman.subcommands())
	require.Contains(h.logs.String(), "Source image rejected", "the podman reason must be logged")
	h.requireNoRecord()
	h.requirePolicyRemoved()
}

func TestFetchReportsRetryableRegistryFailures(t *testing.T) {
	testCases := []struct {
		name   string
		stderr string
	}{
		{"When the registry is unreachable it should report a retryable unavailable updater", "Error: initializing source docker://quay.io/example/updater: pinging container registry quay.io: dial tcp: connect: connection refused\n"},
		{"When registry authentication fails it should report a retryable unavailable updater", "Error: initializing source docker://quay.io/example/updater: reading manifest: unauthorized: authentication required\n"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			h.podman.pullExit = 125
			h.podman.pullStderr = tc.stderr
			pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.Nil(pkg)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
			require.NotContains(h.podman.subcommands(), "image mount")
			h.requireNoRecord()
			h.requirePolicyRemoved()
			h.requireAllowlisted()
		})
	}
}

func TestFetchRejectsADigestMismatch(t *testing.T) {
	require := require.New(t)
	h := newFetchHarness(t)
	h.podman.digest = policy.Hash([]byte("other image"))
	pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.Nil(pkg)
	requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
	require.Equal([]string{"pull", "image inspect"}, h.podman.subcommands())
	h.requireNoRecord()
	h.requirePolicyRemoved()
}

func TestFetchConfirmsThePinnedDigest(t *testing.T) {
	rewritten := policy.Hash([]byte("manifest rewritten by local storage"))
	testCases := []struct {
		name    string
		setup   func(h *fetchHarness)
		wantErr bool
	}{
		{"When podman reports the approved digest as the image digest it should verify the package", func(h *fetchHarness) {}, false},
		{"When podman stored a rewritten manifest but records the pinned repository digest it should verify the package", func(h *fetchHarness) {
			h.podman.digest = rewritten
			h.podman.repoDigests = []string{"registry.example.com/acme/other@" + rewritten, h.reference()}
		}, false},
		{"When the approved digest is recorded only for another repository it should refuse the image", func(h *fetchHarness) {
			h.podman.digest = rewritten
			h.podman.repoDigests = []string{"registry.example.com/acme/other@" + h.release.PackageDigest}
		}, true},
		{"When podman image metadata cannot be parsed it should refuse the image", func(h *fetchHarness) {
			h.podman.inspectOutput = "not image metadata"
		}, true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			tc.setup(h)
			pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			if tc.wantErr {
				require.Nil(pkg)
				requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
				require.Equal([]string{"pull", "image inspect"}, h.podman.subcommands())
				h.requireNoRecord()
				h.requirePolicyRemoved()
				return
			}
			require.NoError(err)
			require.NotNil(pkg)
			require.Equal([]string{"pull", "image inspect", "image mount", "image unmount"}, h.podman.subcommands())
			require.FileExists(h.recordPath())
		})
	}
}

func TestFetchRejectsInvalidManifestsAfterUnmounting(t *testing.T) {
	testCases := []struct {
		name  string
		setup func(h *fetchHarness)
	}{
		{"When the manifest is missing it should report the updater unavailable", func(h *fetchHarness) {
			require.NoError(h.t, os.Remove(filepath.Join(h.root, testMountPoint, firmware.ManifestPath)))
		}},
		{"When the manifest is a symbolic link it should report the updater unavailable", func(h *fetchHarness) {
			dir := filepath.Join(h.root, testMountPoint, "updater")
			require.NoError(h.t, os.Rename(filepath.Join(dir, "manifest.json"), filepath.Join(dir, "real.json")))
			require.NoError(h.t, os.Symlink("real.json", filepath.Join(dir, "manifest.json")))
		}},
		{"When the manifest exceeds the read bound it should report the updater unavailable", func(h *fetchHarness) {
			h.setManifestBytes(append(append([]byte(nil), h.manifest...), bytes.Repeat([]byte(" "), int(firmware.MaxManifestBytes))...))
		}},
		{"When the manifest violates the closed contract it should report the updater unavailable", func(h *fetchHarness) {
			m := manifestFor(h.release)
			m["extra"] = true
			h.setManifest(m)
		}},
		{"When the manifest declares a dependency it should report the updater unavailable", func(h *fetchHarness) {
			m := manifestFor(h.release)
			m["dependencies"] = []any{map[string]any{"name": "fwupd", "constraint": ">=1.9"}}
			h.setManifest(m)
		}},
		{"When the manifest binds another schema it should report the updater unavailable", func(h *fetchHarness) {
			m := manifestFor(h.release)
			m["schemas"].(map[string]any)["config"] = policy.Hash([]byte("other schema"))
			h.setManifest(m)
		}},
		{"When the manifest names another release it should report the updater unavailable", func(h *fetchHarness) {
			m := manifestFor(h.release)
			m["version"] = "9.9.9"
			h.setManifest(m)
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			tc.setup(h)
			pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.Nil(pkg)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			require.Equal([]string{"pull", "image inspect", "image mount", "image unmount"}, h.podman.subcommands())
			h.requireNoRecord()
			h.requirePolicyRemoved()
		})
	}
}

func TestFetchReturnsThePackageWhenUnmountFails(t *testing.T) {
	require := require.New(t)
	h := newFetchHarness(t)
	h.podman.unmountExit = 125
	h.podman.unmountStderr = "Error: device busy"
	pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)
	require.NotNil(pkg)
	require.Contains(h.logs.String(), "unmount")
	_, err = os.Stat(h.recordPath())
	require.NoError(err)
}

func TestFetchResolvesRegistryCredentialsWithoutReadingThem(t *testing.T) {
	inlineAuth := `{"auths":{"quay.io":{"auth":"` + sentinelAuthValue + `-inline"}}}`
	testCases := []struct {
		name     string
		setup    func(h *fetchHarness)
		opts     func(h *fetchHarness) []firmware.FetcherOption
		authfile func(h *fetchHarness, value string)
	}{
		{
			name:  "When the root auth file exists it should pass it to podman pull",
			setup: func(h *fetchHarness) { h.writeAuthFile(firmware.RegistryAuthFile) },
			authfile: func(h *fetchHarness, value string) {
				require.Equal(h.t, firmware.RegistryAuthFile, value)
			},
		},
		{
			name:     "When no auth file exists it should pull without one",
			authfile: func(h *fetchHarness, value string) { require.Empty(h.t, value) },
		},
		{
			name: "When the desired spec carries the auth file inline it should pass the resolver's copy",
			setup: func(h *fetchHarness) {
				var provider v1beta1.ConfigProviderSpec
				require.NoError(h.t, provider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{
					Name:   "registry-auth",
					Inline: []v1beta1.FileSpec{{Path: firmware.RegistryAuthFile, Content: inlineAuth}},
				}))
				h.resolver.BeforeUpdate(&v1beta1.DeviceSpec{Config: &[]v1beta1.ConfigProviderSpec{provider}})
			},
			authfile: func(h *fetchHarness, value string) {
				require.NotEmpty(h.t, value)
				require.NotEqual(h.t, firmware.RegistryAuthFile, value)
				content, err := os.ReadFile(filepath.Join(h.root, value))
				require.NoError(h.t, err)
				require.Equal(h.t, inlineAuth, string(content))
			},
		},
		{
			name: "When the resolver has not been initialized it should pull without an auth file",
			setup: func(h *fetchHarness) {
				h.writeAuthFile(firmware.RegistryAuthFile)
				h.resolver = dependency.NewPullConfigResolver(h.logger, fileio.NewReadWriterFactory(h.root))
			},
			authfile: func(h *fetchHarness, value string) { require.Empty(h.t, value) },
		},
		{
			name: "When another auth file is configured it should ask the resolver for that file",
			setup: func(h *fetchHarness) {
				h.writeAuthFile(firmware.RegistryAuthFile)
				h.writeAuthFile("/etc/flightctl/updater-auth.json")
			},
			opts: func(h *fetchHarness) []firmware.FetcherOption {
				return []firmware.FetcherOption{firmware.WithRegistryAuthFile("/etc/flightctl/updater-auth.json")}
			},
			authfile: func(h *fetchHarness, value string) {
				require.Equal(h.t, "/etc/flightctl/updater-auth.json", value)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			var opts []firmware.FetcherOption
			if tc.opts != nil {
				opts = tc.opts(h)
			}
			pkg, err := h.fetcher(opts...).Fetch(context.Background(), h.release.Name, h.release.Version)
			require.NoError(err)
			require.NotNil(pkg)
			pull := h.podman.calls[0]
			require.Equal("pull", pull[0])
			require.Equal([]string{"--signature-policy", h.policyPath()}, pull[len(pull)-2:])
			authfile := ""
			for i, arg := range pull {
				if arg == "--authfile" {
					authfile = pull[i+1]
				}
			}
			tc.authfile(h, authfile)
			for _, call := range h.podman.calls[1:] {
				require.NotContains(call, "--authfile", "only pull may receive the auth file")
			}
		})
	}
}

func TestFetchKeepsRegistryCredentialsOutOfRecordsLogsAndErrors(t *testing.T) {
	require := require.New(t)
	h := newFetchHarness(t)
	h.writeAuthFile(firmware.RegistryAuthFile)
	var policyAtPull []byte
	h.podman.onPull = func(args []string) {
		data, err := os.ReadFile(h.policyPath())
		require.NoError(err)
		policyAtPull = data
	}
	pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)
	record, err := os.ReadFile(h.recordPath())
	require.NoError(err)
	require.NotContains(string(record), sentinelAuthValue)
	require.NotContains(string(policyAtPull), sentinelAuthValue)
	require.NotContains(fmt.Sprintf("%+v", *pkg), sentinelAuthValue)

	h.podman.calls = nil
	h.podman.pullExit = 125
	h.podman.pullStderr = "Error: Source image rejected: Signature for identity quay.io/other is not accepted\n"
	require.NoError(os.Remove(h.recordPath()))
	_, err = h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.Error(err)
	require.NotContains(err.Error(), sentinelAuthValue)
	require.NotContains(errors.Unwrap(err).Error(), sentinelAuthValue)
	require.NotContains(h.logs.String(), sentinelAuthValue)
	for _, call := range h.podman.calls {
		if subcommand(call) != "pull" {
			require.NotContains(call, "--authfile")
		}
	}
}

func TestNewFetcherRequiresItsDependencies(t *testing.T) {
	h := newFetchHarness(t)
	recv, err := receiver.New("device", testDataDir, h.rw, client.NewMockManagement(gomock.NewController(t)), h.logger)
	require.NoError(t, err)
	testCases := []struct {
		name  string
		build func() (*firmware.Fetcher, error)
	}{
		{"When the data directory is relative it should fail", func() (*firmware.Fetcher, error) {
			return firmware.NewFetcher("var/lib/flightctl", h.client, recv, h.resolver, h.rw, h.cfg, h.logger)
		}},
		{"When the podman client is missing it should fail", func() (*firmware.Fetcher, error) {
			return firmware.NewFetcher(testDataDir, nil, recv, h.resolver, h.rw, h.cfg, h.logger)
		}},
		{"When the policy receiver is missing it should fail", func() (*firmware.Fetcher, error) {
			return firmware.NewFetcher(testDataDir, h.client, nil, h.resolver, h.rw, h.cfg, h.logger)
		}},
		{"When the pull config resolver is missing it should fail", func() (*firmware.Fetcher, error) {
			return firmware.NewFetcher(testDataDir, h.client, recv, nil, h.rw, h.cfg, h.logger)
		}},
		{"When fileio is missing it should fail", func() (*firmware.Fetcher, error) {
			return firmware.NewFetcher(testDataDir, h.client, recv, h.resolver, nil, h.cfg, h.logger)
		}},
		{"When the configuration is missing it should fail", func() (*firmware.Fetcher, error) {
			return firmware.NewFetcher(testDataDir, h.client, recv, h.resolver, h.rw, nil, h.logger)
		}},
		{"When the logger is missing it should fail", func() (*firmware.Fetcher, error) {
			return firmware.NewFetcher(testDataDir, h.client, recv, h.resolver, h.rw, h.cfg, nil)
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := tc.build()
			require.Error(t, err)
			require.Nil(t, f)
		})
	}
}

// TestFetchUsesTheAccessPolicyFromAReload drives the agent reload path: the
// reload manager loads the configuration files and hands the result to
// Fetcher.ReloadConfig, and the next fetch must use exactly that policy.
func TestFetchUsesTheAccessPolicyFromAReload(t *testing.T) {
	require := require.New(t)
	h := newFetchHarness(t)
	root := t.TempDir()
	t.Setenv(config.TestRootDirEnvKey, root)
	configDir := filepath.Join(root, config.DefaultConfigDir)
	grant := "firmware:\n  updater-access:\n    prepare:\n      workspace-write: true\n"

	writeLayers := func(main string, dropins ...string) {
		require.NoError(os.RemoveAll(configDir))
		require.NoError(os.MkdirAll(filepath.Join(configDir, "conf.d"), 0o755))
		require.NoError(os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(main), 0o600))
		for i, content := range dropins {
			require.NoError(os.WriteFile(filepath.Join(configDir, "conf.d", fmt.Sprintf("%02d.yaml", i+1)), []byte(content), 0o600))
		}
	}

	writeLayers(agentConfigYAML + grant)
	initial, err := config.Load(config.DefaultConfigFile)
	require.NoError(err)
	h.cfg = initial
	fetcher := h.fetcher()
	_, err = fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)

	var reloaded *config.Config
	manager := reload.NewManager(config.DefaultConfigFile, h.logger)
	manager.Register(func(ctx context.Context, cfg *config.Config) error {
		reloaded = cfg
		return nil
	})
	manager.Register(fetcher.ReloadConfig)

	denyingLayers := []struct {
		name    string
		main    string
		dropins []string
		warns   int
	}{
		{"an invalid main firmware section", agentConfigYAML + "log-level: debug\nfirmware: []\n", nil, 1},
		{"a drop-in setting firmware to null", agentConfigYAML + grant, []string{"firmware: null\n"}, 0},
		{"an invalid firmware drop-in followed by an unrelated drop-in", agentConfigYAML + grant, []string{"log-level: debug\nfirmware: []\n", "metrics-enabled: true\n"}, 1},
	}
	for _, layers := range denyingLayers {
		writeLayers(layers.main, layers.dropins...)
		manager.Reload(context.Background())
		require.NotNil(reloaded, layers.name)
		fresh, err := config.Load(config.DefaultConfigFile)
		require.NoError(err)
		require.Equal(fresh.Firmware.UpdaterAccess, reloaded.Firmware.UpdaterAccess, layers.name)
		require.Equal(fresh.Warnings, reloaded.Warnings, layers.name)
		require.Len(reloaded.Warnings, layers.warns, layers.name)

		h.podman.calls = nil
		_, err = fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
		requireFetchError(t, err, firmware.ReasonFirmwareNotSupported, false)
		require.Empty(h.podman.calls, layers.name)
	}

	writeLayers(agentConfigYAML + grant)
	manager.Reload(context.Background())
	_, err = fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)
}

func (h *fetchHarness) editRecord(edit func(record map[string]any)) {
	h.t.Helper()
	data, err := os.ReadFile(h.recordPath())
	require.NoError(h.t, err)
	var record map[string]any
	require.NoError(h.t, json.Unmarshal(data, &record))
	edit(record)
	data, err = json.Marshal(record)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(h.recordPath(), data, 0o600))
}

func TestFetchReusesAVerifiedRecordWhileStillValid(t *testing.T) {
	require := require.New(t)
	h := newFetchHarness(t)
	fetcher := h.fetcher()
	first, err := fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)
	require.False(first.Cached)

	expected := *first
	expected.Cached = true
	h.podman.existsResult = 0

	h.podman.calls = nil
	cached, err := fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)
	require.EqualExportedValues(&expected, cached)
	require.Equal([][]string{{"image", "exists", h.reference()}}, h.podman.calls)

	h.podman.calls = nil
	restarted, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err, "a new fetcher on the same data directory must reuse the record")
	require.EqualExportedValues(&expected, restarted)
	require.Equal([][]string{{"image", "exists", h.reference()}}, h.podman.calls)

	view, err := policy.New(2, []policy.Binding{h.binding}, nil)
	require.NoError(err)
	data, err := json.Marshal(view)
	require.NoError(err)
	require.NoError(h.rw.WriteFileDurable(filepath.Join(testDataDir, "firmware", "policy.json"), data, 0o700, 0o600))
	h.podman.calls = nil
	reaccepted, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
	require.NoError(err)
	require.True(reaccepted.Cached)
	require.EqualValues(2, reaccepted.PolicyRevision, "the package reports the revision that approved it now")
	require.Equal([][]string{{"image", "exists", h.reference()}}, h.podman.calls)
}

func TestFetchVerifiesAgainWhenTheRecordCannotBeUsed(t *testing.T) {
	testCases := []struct {
		name  string
		setup func(h *fetchHarness)
	}{
		{"When the image is no longer present it should fetch and verify again", func(h *fetchHarness) { h.podman.existsResult = 1 }},
		{"When the record is corrupt it should fetch and verify again", func(h *fetchHarness) {
			require.NoError(h.t, os.WriteFile(h.recordPath(), []byte(`{"name":`), 0o600))
		}},
		{"When the record has an unknown field it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) { r["trusted"] = true })
		}},
		{"When the record has another format version it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) { r["apiVersion"] = "updater-package-record/v0" })
		}},
		{"When the record names another descriptor it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) { r["descriptorDigest"] = policy.Hash([]byte("other descriptor")) })
		}},
		{"When the record names another package digest it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) { r["packageDigest"] = policy.Hash([]byte("other package")) })
		}},
		{"When the record names another publisher policy it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) { r["publisherPolicyDigest"] = policy.Hash([]byte("other policy")) })
		}},
		{"When the record names another release version it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) { r["version"] = "9.9.9" })
		}},
		{"When the record manifest binds another schema it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) {
				r["manifest"].(map[string]any)["schemas"].(map[string]any)["config"] = policy.Hash([]byte("other schema"))
			})
		}},
		{"When the record manifest digest is malformed it should fetch and verify again", func(h *fetchHarness) {
			h.editRecord(func(r map[string]any) { r["manifestDigest"] = "not-a-digest" })
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			first, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.NoError(err)
			h.podman.existsResult = 0
			tc.setup(h)

			h.podman.calls = nil
			pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.NoError(err)
			require.False(pkg.Cached)
			require.Equal(first, pkg)
			names := h.podman.subcommands()
			require.Equal([]string{"pull", "image inspect", "image mount", "image unmount"}, names[len(names)-4:])

			h.podman.calls = nil
			h.podman.existsResult = 0
			again, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.NoError(err)
			require.True(again.Cached, "the rewritten record must be reusable")
		})
	}
}

func TestFetchHonorsCurrentAuthorizationBeforeUsingARecord(t *testing.T) {
	testCases := []struct {
		name      string
		setup     func(h *fetchHarness)
		reason    firmware.Reason
		retryable bool
	}{
		{"When the release has since been revoked it should report the updater unavailable", func(h *fetchHarness) { h.approve(h.release, true) }, firmware.ReasonUpdaterUnavailable, false},
		{"When the local access policy no longer admits the release it should report firmware not supported", func(h *fetchHarness) {
			h.cfg.Firmware.UpdaterAccess = config.UpdaterAccessPolicy{}
		}, firmware.ReasonFirmwareNotSupported, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			_, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.NoError(err)
			h.podman.existsResult = 0
			tc.setup(h)

			h.podman.calls = nil
			pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.Nil(pkg)
			requireFetchError(t, err, tc.reason, tc.retryable)
			require.Empty(h.podman.calls, "no podman command may run")
			_, err = os.Stat(h.recordPath())
			require.NoError(err, "the record is kept as evidence")
		})
	}
}

func TestFetchBoundsThePullByTheAgentPullTimeout(t *testing.T) {
	requirePullBound := func(t *testing.T, h *fetchHarness, started time.Time, want time.Duration) {
		t.Helper()
		require.False(t, h.podman.pullDeadline.IsZero(), "the pull must run with a deadline")
		bound := h.podman.pullDeadline.Sub(started)
		require.True(t, bound > want-time.Minute && bound <= want+time.Second, "pull bounded by %s, want %s", bound, want)
	}

	t.Run("When the agent configures a pull timeout it should bound the pull by it", func(t *testing.T) {
		h := newFetchHarness(t)
		h.cfg.PullTimeout = util.Duration(25 * time.Minute)
		started := time.Now()
		_, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(t, err)
		requirePullBound(t, h, started, 25*time.Minute)
	})

	t.Run("When a reload changes the pull timeout it should bound the next pull by the new value", func(t *testing.T) {
		h := newFetchHarness(t)
		h.cfg.PullTimeout = util.Duration(25 * time.Minute)
		fetcher := h.fetcher()
		reloaded := config.NewDefault()
		reloaded.Firmware.UpdaterAccess = allowWorkspace()
		reloaded.PullTimeout = util.Duration(3 * time.Minute)
		require.NoError(t, fetcher.ReloadConfig(context.Background(), reloaded))
		started := time.Now()
		_, err := fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(t, err)
		requirePullBound(t, h, started, 3*time.Minute)
	})
}

func TestFetchErrorText(t *testing.T) {
	t.Run("When a fetch fails for a reason other than the signature policy it should name the release and the cause", func(t *testing.T) {
		require := require.New(t)
		h := newFetchHarness(t)
		h.podman.digest = policy.Hash([]byte("other image"))
		_, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.Error(err)
		require.True(strings.HasPrefix(err.Error(), "updater "+h.release.Name+"/"+h.release.Version+": "), err.Error())
		require.Contains(err.Error(), "differs from the approved package digest")
	})

	t.Run("When a fetch error carries no cause it should report its reason", func(t *testing.T) {
		require.Equal(t, "FirmwareNotSupported", (&firmware.FetchError{Reason: firmware.ReasonFirmwareNotSupported}).Error())
	})

	t.Run("When podman reports a long multi-line error it should keep one bounded line", func(t *testing.T) {
		require := require.New(t)
		h := newFetchHarness(t)
		h.podman.pullExit = 125
		h.podman.pullStderr = "Error: initializing source: dial tcp: connect: connection refused\n" + strings.Repeat("detail\t\x1b[31mline\r\n", 400)
		_, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		for _, text := range []string{err.Error(), errors.Unwrap(err).Error()} {
			require.NotContains(text, "\n")
			require.NotContains(text, "\r")
			require.NotContains(text, "\t")
			require.NotContains(text, "\x1b")
			require.Less(len([]rune(text)), 1400)
			require.True(strings.HasSuffix(text, "..."), "a truncated cause ends with an ellipsis")
		}
	})
}

func TestFetcherReloadConfigKeepsSettingsOnRefusal(t *testing.T) {
	testCases := []struct {
		name   string
		reload func(f *firmware.Fetcher) error
	}{
		{"When the reload context is canceled it should keep the previous settings", func(f *firmware.Fetcher) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return f.ReloadConfig(ctx, config.NewDefault())
		}},
		{"When no configuration is given it should keep the previous settings", func(f *firmware.Fetcher) error {
			return f.ReloadConfig(context.Background(), nil)
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			fetcher := h.fetcher()
			require.Error(tc.reload(fetcher))
			pkg, err := fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
			require.NoError(err, "the previous access policy still admits the release")
			require.NotNil(pkg)
		})
	}
}

func TestFetchFailsClosedWhenALocalStepFails(t *testing.T) {
	blockWith := func(dir string) func(h *fetchHarness) {
		return func(h *fetchHarness) {
			path := filepath.Join(h.root, testDataDir, "firmware", dir)
			require.NoError(h.t, os.WriteFile(path, []byte("not a directory"), 0o600))
		}
	}
	testCases := []struct {
		name        string
		setup       func(h *fetchHarness)
		subcommands []string
	}{
		{"When the pulled image cannot be inspected it should report the updater unavailable", func(h *fetchHarness) { h.podman.inspectExit = 125 },
			[]string{"pull", "image inspect"}},
		{"When the image cannot be mounted it should report the updater unavailable", func(h *fetchHarness) { h.podman.mountExit = 125 },
			[]string{"pull", "image inspect", "image mount"}},
		{"When the verified record cannot be written it should report the updater unavailable after unmounting", blockWith("packages"),
			[]string{"pull", "image inspect", "image mount", "image unmount"}},
		{"When the signature policy cannot be written it should report the updater unavailable before any pull", blockWith("signature-policy"),
			nil},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			h := newFetchHarness(t)
			tc.setup(h)
			pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.Nil(pkg)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			if tc.subcommands == nil {
				require.Empty(h.podman.calls)
			} else {
				require.Equal(tc.subcommands, h.podman.subcommands())
			}
			_, statErr := os.Stat(h.recordPath())
			require.Error(statErr, "no verified record may exist")
		})
	}
}

func TestFetchErrorsCarryTheAgentRetryClassification(t *testing.T) {
	testCases := []struct {
		name      string
		setup     func(h *fetchHarness)
		retryable bool
	}{
		{"When the registry is unreachable it should classify as retryable", func(h *fetchHarness) {
			h.podman.pullExit = 125
			h.podman.pullStderr = "Error: initializing source: dial tcp: connect: connection refused\n"
		}, true},
		{"When the signature policy rejects the pull it should classify as not retryable", func(h *fetchHarness) {
			h.podman.pullExit = 125
			h.podman.pullStderr = "Error: Source image rejected: A signature was required, but no signature exists\n"
		}, false},
		{"When the release is revoked it should classify as not retryable", func(h *fetchHarness) { h.approve(h.release, true) }, false},
		{"When the release is not supported it should classify as not retryable", func(h *fetchHarness) {
			h.cfg.Firmware.UpdaterAccess = config.UpdaterAccessPolicy{}
		}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFetchHarness(t)
			tc.setup(h)
			_, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
			require.Error(t, err)
			require.Equal(t, tc.retryable, deviceerrors.IsRetryable(err))
		})
	}
}
