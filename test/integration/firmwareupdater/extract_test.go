package firmwareupdater_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/poll"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// extractEnv extracts updater payloads with the real podman image mount of a
// fetch environment. Its handles are rooted at the suite work directory, which
// holds every spec's data directory as well as the image storage, including
// storage that podman places under the suite's home directories. The trusted
// path to the data directory therefore starts at a directory the test owns: in
// a rootless user namespace the host root directory belongs to an unmapped user.
type extractEnv struct {
	*fetchEnv
	root      string
	rw        fileio.ReadWriter
	retention *firmware.Retention
	extractor *firmware.Extractor
}

func newExtractEnv(e *fetchEnv) *extractEnv {
	root := state.WorkDir
	x := &extractEnv{
		fetchEnv: e,
		root:     root,
		rw:       fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root))),
	}
	x.restart()
	return x
}

// rootedMounter reports image mount points relative to the root of the
// extraction handles.
type rootedMounter struct {
	firmware.ImageMounter
	root string
}

func (m rootedMounter) Mount(ctx context.Context, image string) (string, error) {
	point, err := m.ImageMounter.Mount(ctx, image)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(m.root, point)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("image mount point %s is outside %s", point, m.root)
	}
	return "/" + rel, nil
}

// fixedMounter reports a fixed mount point without mounting anything.
type fixedMounter struct{ point string }

func (m fixedMounter) Mount(context.Context, string) (string, error) { return m.point, nil }
func (fixedMounter) Unmount(context.Context, string) error           { return nil }

// restart replaces the retention and extractor as a new agent process would.
func (x *extractEnv) restart() {
	podman := client.NewPodman(x.logger, x.recorder, x.rw, poll.Config{BaseDelay: time.Second, Factor: 1, MaxSteps: 1})
	rel, err := filepath.Rel(x.root, x.dataDir)
	Expect(err).NotTo(HaveOccurred())
	Expect(rel).NotTo(Or(Equal(".."), HavePrefix("../")), "the data directory must lie beneath the suite root")
	dataDir := "/" + filepath.ToSlash(rel)
	x.retention = firmware.NewRetention(dataDir, x.rw, x.logger)
	x.extractor = firmware.NewExtractor(dataDir, rootedMounter{ImageMounter: podman, root: x.root}, x.receiver, x.rw, x.retention, x.logger)
}

func (x *extractEnv) versionDir(pkg *firmware.VerifiedPackage, rel ...string) string {
	return filepath.Join(append([]string{x.dataDir, "firmware", "updaters", strings.TrimPrefix(pkg.PackageDigest, "sha256:")}, rel...)...)
}

func (x *extractEnv) mounts() int {
	n := 0
	for _, name := range x.recorder.subcommands() {
		if name == "image mount" {
			n++
		}
	}
	return n
}

func requireNativeHost() {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		Skip("the extractable fixture has a native entrypoint for amd64 and arm64 only")
	}
}

func treeOf(dir string) map[string]string {
	out := map[string]string{}
	Expect(filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		out[p] = info.Mode().String()
		return nil
	})).To(Succeed())
	return out
}

var _ = Describe("Extracting verified updater payloads", func() {
	It("When an image is mounted beside the data directory under the suite root it should resolve the mount point and refuse one outside that root", func(ctx SpecContext) {
		x := newExtractEnv(newFetchEnv(state.DiscoveryHome, state.AuthFile))
		mounts, err := os.MkdirTemp(state.DiscoveryHome, "mounts-")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, mounts)
		merged := filepath.Join(mounts, "overlay", "merged")
		Expect(os.MkdirAll(filepath.Join(merged, "updater"), 0o700)).To(Succeed())

		point, err := rootedMounter{ImageMounter: fixedMounter{point: merged}, root: x.root}.Mount(ctx, "image")
		Expect(err).NotTo(HaveOccurred())
		src, err := x.rw.OpenAnchor(point, fileio.DirOptions{ReadOnly: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(src.Close()).To(Succeed())

		_, err = rootedMounter{ImageMounter: fixedMounter{point: filepath.Dir(state.WorkDir)}, root: x.root}.Mount(ctx, "image")
		Expect(err).To(MatchError(ContainSubstring("outside")))
	})

	It("When an approved signed release has an extraction budget it should publish only its listed files after unmounting the image", func(ctx SpecContext) {
		requireNativeHost()
		x := newExtractEnv(newFetchEnv(state.DiscoveryHome, state.AuthFile))
		sentinel := filepath.Join(x.dataDir, "sentinel")
		Expect(os.MkdirAll(sentinel, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(sentinel, "keep"), []byte("keep"), 0o600)).To(Succeed())
		before := treeOf(sentinel)

		pkg, err := x.fetch(ctx, roleExtractable)
		Expect(err).NotTo(HaveOccurred())
		containers := x.containers(ctx)
		payload, err := x.extractor.Extract(ctx, pkg)
		Expect(err).NotTo(HaveOccurred())
		Expect(payload.Reused).To(BeFalse())
		Expect(payload.PackageDigest).To(Equal(pkg.PackageDigest))
		Expect(payload.ManifestDigest).To(Equal(pkg.ManifestDigest))
		Expect(payload.Reference).To(Equal(fixtureReference(roleExtractable)))
		Expect(payload.Entrypoint).To(Equal("payload/bin/updater"))
		Expect(payload.Files).To(HaveLen(2))
		Expect(x.containers(ctx)).To(Equal(containers), "extraction must not create containers")
		Expect(x.recorder.subcommands()[len(x.recorder.subcommands())-2:]).To(Equal([]string{"image mount", "image unmount"}))
		x.requireNoMountLeft()

		manifest, err := os.ReadFile(x.versionDir(pkg, "manifest.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.Hash(manifest)).To(Equal(pkg.ManifestDigest))
		info, err := os.Stat(x.versionDir(pkg, "payload", "bin", "updater"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o500)))
		entries, err := os.ReadDir(x.versionDir(pkg))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(2), "only manifest.json and payload are published")
		Expect(treeOf(sentinel)).To(Equal(before))

		evidence, err := x.retention.Evidence(pkg.PackageDigest)
		Expect(err).NotTo(HaveOccurred())
		Expect(evidence.Published.Event).To(Equal("publish"))
		Expect(evidence.Published.Mode).To(BeElementOf(firmware.SELinuxEnforcing, firmware.SELinuxPermissive, firmware.SELinuxDisabled, firmware.SELinuxUnknown))

		mounts := x.mounts()
		x.restart()
		reused, err := x.extractor.Extract(ctx, pkg)
		Expect(err).NotTo(HaveOccurred())
		Expect(reused.Reused).To(BeTrue())
		Expect(x.mounts()).To(Equal(mounts), "reuse after a restart does not mount the image")
	})

	It("When a pinned payload is changed or deleted it should refuse it without mounting, also after a restart", func(ctx SpecContext) {
		requireNativeHost()
		x := newExtractEnv(newFetchEnv(state.DiscoveryHome, state.AuthFile))
		pkg, err := x.fetch(ctx, roleExtractable)
		Expect(err).NotTo(HaveOccurred())
		payload, err := x.extractor.Extract(ctx, pkg)
		Expect(err).NotTo(HaveOccurred())
		Expect(x.retention.Pin(payload.Pin("attempt-1", firmware.PinRoleActive))).To(Succeed())
		refs, err := x.retention.RetainedImageReferences(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(ConsistOf(pkg.Reference))
		mounts := x.mounts()

		notes := x.versionDir(pkg, "payload", "share", "notes.txt")
		Expect(os.Chmod(notes, 0o600)).To(Succeed())
		Expect(os.WriteFile(notes, []byte("tampered notes\n"), 0o600)).To(Succeed())
		Expect(os.Chmod(notes, 0o400)).To(Succeed())
		_, err = x.extractor.Extract(ctx, pkg)
		Expect(errors.Is(err, firmware.ErrRetainedPayloadChanged)).To(BeTrue(), "%v", err)

		Expect(os.RemoveAll(x.versionDir(pkg))).To(Succeed())
		x.restart()
		_, err = x.extractor.Extract(ctx, pkg)
		Expect(errors.Is(err, firmware.ErrRetainedPayloadMissing)).To(BeTrue(), "%v", err)
		Expect(x.mounts()).To(Equal(mounts), "missing or changed pinned code is never extracted again")
		_, err = os.Stat(x.versionDir(pkg))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("When the release declares no extraction budget it should refuse it before mounting", func(ctx SpecContext) {
		x := newExtractEnv(newFetchEnv(state.DiscoveryHome, state.AuthFile))
		pkg, err := x.fetch(ctx, roleValid)
		Expect(err).NotTo(HaveOccurred())
		mounts := x.mounts()
		_, err = x.extractor.Extract(ctx, pkg)
		Expect(errors.Is(err, firmware.ErrNoExtractionBudget)).To(BeTrue(), "%v", err)
		Expect(x.mounts()).To(Equal(mounts))
	})

	It("When the updater cache directory is a symlink it should refuse it and leave the target untouched", func(ctx SpecContext) {
		requireNativeHost()
		x := newExtractEnv(newFetchEnv(state.DiscoveryHome, state.AuthFile))
		pkg, err := x.fetch(ctx, roleExtractable)
		Expect(err).NotTo(HaveOccurred())
		target := filepath.Join(x.dataDir, "elsewhere")
		Expect(os.MkdirAll(target, 0o700)).To(Succeed())
		Expect(os.Symlink(target, filepath.Join(x.dataDir, "firmware", "updaters"))).To(Succeed())
		mounts := x.mounts()
		_, err = x.extractor.Extract(ctx, pkg)
		Expect(errors.Is(err, deviceerrors.ErrUntrustedDirectory)).To(BeTrue(), "%v", err)
		Expect(x.mounts()).To(Equal(mounts))
		entries, err := os.ReadDir(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})

	It("When a maximal-depth staging directory is left behind cleanup should remove it", func(ctx context.Context) {
		x := newExtractEnv(newFetchEnv(state.DiscoveryHome, state.AuthFile))
		staging := filepath.Join(x.dataDir, "firmware", "updaters", ".staging-0123456789abcdef")
		deep := filepath.Join(append([]string{staging, "payload"}, strings.Split(strings.Repeat("d/", 14), "/")...)...)
		Expect(os.MkdirAll(deep, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(deep, "f"), []byte("x"), 0o400)).To(Succeed())
		removed, err := x.retention.CleanupUnreferenced(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(ContainElement(".staging-0123456789abcdef"))
		_, err = os.Stat(staging)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})
