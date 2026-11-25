package firmwareupdater_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	receiver "github.com/flightctl/flightctl/internal/agent/device/firmwarepolicy"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

var allowedSubcommands = []string{"pull", "image inspect", "image exists", "image mount", "image unmount"}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fetchEnv is one device: fresh image storage, a fresh data directory holding
// the accepted policy, and a fetcher wired with real agent components.
type fetchEnv struct {
	dataDir  string
	env      []string
	recorder *recordingExecuter
	logs     *syncBuffer
	fetcher  *firmware.Fetcher
	receiver *receiver.Manager
	logger   *log.PrefixLogger
}

func newFetchEnv(home, authFile string) *fetchEnv {
	dir := tempDir()
	e := &fetchEnv{dataDir: filepath.Join(dir, "data"), logs: &syncBuffer{}}
	e.env = podmanEnv(newStorage(filepath.Join(dir, "storage")), home)
	e.recorder = newRecordingExecuter(e.env)
	logger := log.NewPrefixLogger("firmware-it")
	logger.SetOutput(e.logs)
	logger.SetLevel(logrus.DebugLevel)

	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	bindings := make([]policy.Binding, 0, len(state.Fixtures))
	for _, f := range state.Fixtures {
		bindings = append(bindings, f.Binding)
	}
	view, err := policy.New(1, bindings, nil)
	Expect(err).NotTo(HaveOccurred())
	data, err := json.Marshal(view)
	Expect(err).NotTo(HaveOccurred())
	Expect(rw.WriteFileDurable(filepath.Join(e.dataDir, "firmware", "policy.json"), data, 0o700, 0o600)).To(Succeed())
	recv, err := receiver.New("device", e.dataDir, rw, client.NewMockManagement(gomock.NewController(GinkgoT())), logger)
	Expect(err).NotTo(HaveOccurred())
	e.receiver, e.logger = recv, logger

	resolver := dependency.NewPullConfigResolver(logger, fileio.NewReadWriterFactory(""))
	resolver.BeforeUpdate(&v1beta1.DeviceSpec{})
	cfg := config.NewDefault()
	cfg.Firmware.UpdaterAccess = config.UpdaterAccessPolicy{Prepare: config.UpdaterAccessGrant{WorkspaceWrite: true}}
	podman := client.NewPodman(logger, e.recorder, rw, poll.Config{BaseDelay: time.Second, Factor: 1, MaxSteps: 1})
	e.fetcher, err = firmware.NewFetcher(e.dataDir, podman, recv, resolver, rw, cfg, logger, firmware.WithRegistryAuthFile(authFile))
	Expect(err).NotTo(HaveOccurred())
	return e
}

// fetch runs Fetch for the release of role and checks that the fetch ran no
// updater: only allowlisted podman subcommands, and no container created in
// the device's image storage. The registry and fixture-build containers live
// in other storage and are not part of this check.
func (e *fetchEnv) fetch(ctx SpecContext, role string) (*firmware.VerifiedPackage, error) {
	before := e.containers(ctx)
	pkg, err := e.fetcher.Fetch(ctx, releaseName, state.Fixtures[role].Version)
	for _, name := range e.recorder.subcommands() {
		Expect(allowedSubcommands).To(ContainElement(name), "podman subcommand outside the allowlist")
	}
	Expect(e.containers(ctx)).To(Equal(before), "the fetch must not create containers")
	if e.imageExists(ctx, fixtureReference(role)) {
		ancestors, err := runPodman(ctx, e.env, "ps", "--all", "--external", "--filter", "ancestor="+fixtureReference(role), "--format", "{{.ID}}")
		Expect(err).NotTo(HaveOccurred())
		Expect(ancestors).To(BeEmpty(), "no container may run the updater image")
	}
	return pkg, err
}

func (e *fetchEnv) containers(ctx SpecContext) string {
	out, err := runPodman(ctx, e.env, "ps", "--all", "--external", "--format", "{{.ID}}")
	Expect(err).NotTo(HaveOccurred())
	return out
}

func (e *fetchEnv) imageExists(ctx SpecContext, ref string) bool {
	_, err := runPodman(ctx, e.env, "image", "exists", ref)
	return err == nil
}

func (e *fetchEnv) recordPath(role string) string {
	digest := state.Fixtures[role].Binding.DescriptorDigest
	return filepath.Join(e.dataDir, "firmware", "packages", strings.TrimPrefix(digest, "sha256:")+".json")
}

func (e *fetchEnv) requireNoRecord(role string) {
	_, err := os.Stat(e.recordPath(role))
	Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue(), "no verified record may exist")
}

func (e *fetchEnv) requireNoSignaturePolicy() {
	entries, err := os.ReadDir(filepath.Join(e.dataDir, "firmware", "signature-policy"))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	Expect(entries).To(BeEmpty(), "the per-call signature policy must be removed")
}

func (e *fetchEnv) requireNoMountLeft() {
	for mountPoint := range e.recorder.mountPoints {
		_, mounted := mountOptions(mountPoint)
		Expect(mounted).To(BeFalse(), "the image must be unmounted after the fetch")
	}
}

func requireFetchError(err error, reason firmware.Reason, retryable bool) {
	var fetchErr *firmware.FetchError
	Expect(errors.As(err, &fetchErr)).To(BeTrue(), "expected a FetchError, got %v", err)
	Expect(fetchErr.Reason).To(Equal(reason))
	Expect(fetchErr.Retryable).To(Equal(retryable))
}

func signatureRejection(role string) string {
	repo := state.Registry + "/" + repository
	return fmt.Sprintf("updater %s/%s: publisher signature verification failed for %s; the image must be signed by the approved publisher key for this exact repository, and the host must enable sigstore attachment discovery (use-sigstore-attachments) for %s",
		releaseName, state.Fixtures[role].Version, fixtureReference(role), repo)
}

func registriesDFile(home string) string {
	return filepath.Join(home, ".config", "containers", "registries.d", "firmware-updater-test.yaml")
}

var _ = Describe("Fetching approved updater code", func() {
	It("When an approved signed release is not cached it should pull, verify, mount, record and unmount it", func(ctx SpecContext) {
		e := newFetchEnv(state.DiscoveryHome, state.AuthFile)
		f := state.Fixtures[roleValid]
		pkg, err := e.fetch(ctx, roleValid)
		Expect(err).NotTo(HaveOccurred())

		Expect(e.recorder.subcommands()).To(Equal([]string{"pull", "image inspect", "image mount", "image unmount"}))
		pull := e.recorder.pullArgs()
		Expect(pull).To(ContainElement("--signature-policy"))
		Expect(strings.Join(pull, " ")).To(ContainSubstring("--authfile " + state.AuthFile))

		Expect(pkg.Cached).To(BeFalse())
		Expect(pkg.Name).To(Equal(releaseName))
		Expect(pkg.Version).To(Equal(f.Version))
		Expect(pkg.DescriptorDigest).To(Equal(f.Binding.DescriptorDigest))
		Expect(pkg.PackageDigest).To(Equal(f.Digest))
		Expect(pkg.Reference).To(Equal(fixtureReference(roleValid)))
		Expect(pkg.ManifestDigest).To(Equal(policy.Hash(f.Manifest)))
		Expect(pkg.PolicyRevision).To(Equal(int64(1)))

		info, err := os.Stat(e.recordPath(roleValid))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		data, err := os.ReadFile(e.recordPath(roleValid))
		Expect(err).NotTo(HaveOccurred())
		var record map[string]any
		Expect(json.Unmarshal(data, &record)).To(Succeed())
		Expect(record).To(HaveKeyWithValue("descriptorDigest", f.Binding.DescriptorDigest))
		Expect(record).To(HaveKeyWithValue("packageDigest", f.Digest))
		Expect(record).To(HaveKeyWithValue("manifestDigest", policy.Hash(f.Manifest)))
		e.requireNoSignaturePolicy()

		Expect(e.recorder.mountPoints).To(HaveLen(1))
		for _, options := range e.recorder.mountPoints {
			Expect(strings.Split(options, ",")).To(ContainElement("ro"), "the image must be mounted read-only")
		}
		e.requireNoMountLeft()
	})

	It("When the approved package digest names a signed manifest list it should verify and record the platform image", func(ctx SpecContext) {
		e := newFetchEnv(state.DiscoveryHome, state.AuthFile)
		f := state.Fixtures[roleManifestList]
		pkg, err := e.fetch(ctx, roleManifestList)
		Expect(err).NotTo(HaveOccurred())
		Expect(e.recorder.subcommands()).To(Equal([]string{"pull", "image inspect", "image mount", "image unmount"}))
		Expect(pkg.PackageDigest).To(Equal(f.Digest))
		Expect(pkg.Reference).To(Equal(fixtureReference(roleManifestList)))
		Expect(pkg.ManifestDigest).To(Equal(policy.Hash(f.Manifest)))
		_, err = os.Stat(e.recordPath(roleManifestList))
		Expect(err).NotTo(HaveOccurred())
		e.requireNoSignaturePolicy()
		e.requireNoMountLeft()
	})

	It("When no registry credentials are available it should report a retryable unavailable updater", func(ctx SpecContext) {
		e := newFetchEnv(state.DiscoveryHome, filepath.Join(state.WorkDir, "missing-auth.json"))
		pkg, err := e.fetch(ctx, roleValid)
		Expect(pkg).To(BeNil())
		requireFetchError(err, firmware.ReasonUpdaterUnavailable, true)
		Expect(e.recorder.subcommands()).NotTo(ContainElement("image mount"))
		Expect(e.recorder.pullArgs()).NotTo(ContainElement("--authfile"))
		e.requireNoRecord(roleValid)
		e.requireNoSignaturePolicy()
		Expect(err.Error()).NotTo(ContainSubstring(state.RegistryPassword))
		Expect(e.logs.String()).NotTo(ContainSubstring(state.RegistryPassword))
	})

	It("When the image is signed by an unknown key it should fail closed with the actionable message", func(ctx SpecContext) {
		e := newFetchEnv(state.DiscoveryHome, state.AuthFile)
		pkg, err := e.fetch(ctx, roleUnknownKey)
		Expect(pkg).To(BeNil())
		requireFetchError(err, firmware.ReasonUpdaterUnavailable, false)
		Expect(err.Error()).To(Equal(signatureRejection(roleUnknownKey)))
		Expect(errors.Unwrap(err).Error()).To(ContainSubstring("Source image rejected"))
		Expect(e.recorder.subcommands()).To(Equal([]string{"pull"}))
		e.requireNoRecord(roleUnknownKey)
		e.requireNoSignaturePolicy()
	})

	It("When a correctly signed image binds another schema it should reject it, unmount and keep the image", func(ctx SpecContext) {
		e := newFetchEnv(state.DiscoveryHome, state.AuthFile)
		pkg, err := e.fetch(ctx, roleSchemaMismatch)
		Expect(pkg).To(BeNil())
		requireFetchError(err, firmware.ReasonUpdaterUnavailable, false)
		Expect(e.recorder.subcommands()).To(Equal([]string{"pull", "image inspect", "image mount", "image unmount"}))
		e.requireNoMountLeft()
		e.requireNoRecord(roleSchemaMismatch)
		e.requireNoSignaturePolicy()
		Expect(e.imageExists(ctx, fixtureReference(roleSchemaMismatch))).To(BeTrue(), "the rejected image is kept as evidence")
	})

	It("When a correctly signed image has a symlinked manifest it should refuse to follow it", func(ctx SpecContext) {
		e := newFetchEnv(state.DiscoveryHome, state.AuthFile)
		pkg, err := e.fetch(ctx, roleSymlink)
		Expect(pkg).To(BeNil())
		requireFetchError(err, firmware.ReasonUpdaterUnavailable, false)
		Expect(err.Error()).To(ContainSubstring(firmware.ManifestPath))
		Expect(e.recorder.subcommands()).To(Equal([]string{"pull", "image inspect", "image mount", "image unmount"}))
		e.requireNoMountLeft()
		e.requireNoRecord(roleSymlink)
	})

	It("When host signature discovery is disabled it should fail closed even for a correctly signed approved image", func(ctx SpecContext) {
		enabled, err := os.ReadFile(registriesDFile(state.DiscoveryHome))
		Expect(err).NotTo(HaveOccurred())
		disabled, err := os.ReadFile(registriesDFile(state.NoDiscoveryHome))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(disabled)).NotTo(Equal(string(enabled)))
		Expect(strings.ReplaceAll(string(disabled), "false", "true")).To(Equal(string(enabled)), "only the discovery setting may differ")

		control := newFetchEnv(state.DiscoveryHome, state.AuthFile)
		pkg, err := control.fetch(ctx, roleValid)
		Expect(err).NotTo(HaveOccurred(), "the positive control must succeed with discovery enabled")
		Expect(pkg.Cached).To(BeFalse())

		negative := newFetchEnv(state.NoDiscoveryHome, state.AuthFile)
		pkg, err = negative.fetch(ctx, roleValid)
		Expect(pkg).To(BeNil())
		requireFetchError(err, firmware.ReasonUpdaterUnavailable, false)
		Expect(err.Error()).To(Equal(signatureRejection(roleValid)))
		Expect(negative.recorder.subcommands()).To(Equal([]string{"pull"}))
		negative.requireNoRecord(roleValid)
		negative.requireNoSignaturePolicy()
		Expect(negative.imageExists(ctx, fixtureReference(roleValid))).To(BeFalse())
	})
})
