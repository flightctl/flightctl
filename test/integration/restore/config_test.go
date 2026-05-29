package restore_test

import (
	"archive/tar"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/restore"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/harness/containers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

// buildTestExtractDirWithServiceConfig writes config/service-config.yaml with the
// given content into extractDir.
func buildTestExtractDirWithServiceConfig(extractDir, cfgContent string) {
	cfgDir := filepath.Join(extractDir, "config")
	Expect(os.MkdirAll(cfgDir, 0700)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(cfgDir, "service-config.yaml"), []byte(cfgContent), 0600)).To(Succeed())
}

// buildTestExtractDirWithSyntheticPAMVolume creates a minimal valid tar file at
// <extractDir>/volumes/pam-issuer-etc.tar containing a single file named filename.
// The tar is crafted entirely in Go so no running Podman volume is needed during setup.
func buildTestExtractDirWithSyntheticPAMVolume(extractDir, filename string) {
	volumesDir := filepath.Join(extractDir, "volumes")
	Expect(os.MkdirAll(volumesDir, 0700)).To(Succeed())

	tarPath := filepath.Join(volumesDir, "pam-issuer-etc.tar")
	f, err := os.Create(tarPath)
	Expect(err).NotTo(HaveOccurred())
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	content := []byte("test content for " + filename)
	hdr := &tar.Header{
		Name: filename,
		Mode: 0600,
		Size: int64(len(content)),
	}
	Expect(tw.WriteHeader(hdr)).To(Succeed())
	_, err = tw.Write(content)
	Expect(err).NotTo(HaveOccurred())
}

// podmanVolumeMountpoint returns the storage path for a named Podman volume.
func podmanVolumeMountpoint(ctx context.Context, cli, volumeName string) (string, error) {
	out, err := exec.CommandContext(ctx, cli, "volume", "inspect", volumeName, "--format", "{{.Mountpoint}}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// cleanupVolume removes a Podman volume created during the test.
func cleanupVolume(cli, volumeName string) {
	_ = exec.Command(cli, "volume", "rm", "--force", volumeName).Run() //nolint:gosec
}

// skipIfNotPodman skips the current spec when the container runtime is not Podman.
// These tests use podman volume create/import/rm/inspect which have no Docker equivalent.
func skipIfNotPodman(cli string) {
	if cli != "podman" {
		Skip(fmt.Sprintf("skipping Podman-only tests: runtime is %q", cli))
	}
}

var _ = Describe("PodmanRestoreDeployer RestoreConfig", func() {
	const minimalCfg = "database:\n  hostname: testhost\n"

	var (
		ctx        context.Context
		log        *logrus.Logger
		extractDir string
		cli        string
	)

	BeforeEach(func() {
		ctx = context.Background()
		log = flightlog.InitLogs()
		cli = containers.RuntimeCLIName()
		skipIfNotPodman(cli)

		var err error
		extractDir, err = os.MkdirTemp("", "restore-config-test-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(extractDir)
	})

	It("When a service-config.yaml is in the archive it should write it to the destination", func() {
		buildTestExtractDirWithServiceConfig(extractDir, minimalCfg)

		destDir, err := os.MkdirTemp("", "restore-config-dest-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(destDir) })
		destPath := filepath.Join(destDir, "service-config.yaml")

		d := restore.NewPodmanRestoreDeployer(log,
			restore.WithServiceHandler(restore.NewSystemctlServiceHandler([]string{})),
			restore.WithServiceConfigPath(destPath),
			restore.WithContainerCLI(cli),
		)
		Expect(d.RestoreConfig(ctx, extractDir)).To(Succeed())
		Expect(destPath).To(BeAnExistingFile())

		content, err := os.ReadFile(destPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("testhost"))
	})

	It("When a PAM Issuer volume archive is in the archive it should import it into the named volume", func() {
		volumeName := fmt.Sprintf("flightctl-test-pam-issuer-%d", GinkgoRandomSeed())
		DeferCleanup(func() { cleanupVolume(cli, volumeName) })

		buildTestExtractDirWithServiceConfig(extractDir, minimalCfg)
		buildTestExtractDirWithSyntheticPAMVolume(extractDir, "marker-a.txt")

		destDir, err := os.MkdirTemp("", "restore-config-dest-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(destDir) })
		destPath := filepath.Join(destDir, "service-config.yaml")

		d := restore.NewPodmanRestoreDeployer(log,
			restore.WithServiceHandler(restore.NewSystemctlServiceHandler([]string{})),
			restore.WithServiceConfigPath(destPath),
			restore.WithContainerCLI(cli),
			restore.WithPAMIssuerVolumeName(volumeName),
		)
		Expect(d.RestoreConfig(ctx, extractDir)).To(Succeed())

		// Volume must have been created and populated by the import.
		mountpoint, err := podmanVolumeMountpoint(ctx, cli, volumeName)
		Expect(err).NotTo(HaveOccurred(), "volume should exist after import")
		Expect(filepath.Join(mountpoint, "marker-a.txt")).To(BeAnExistingFile())
	})

	It("When the PAM Issuer volume already exists it should overwrite it with the archive content", func() {
		volumeName := fmt.Sprintf("flightctl-test-pam-issuer-exists-%d", GinkgoRandomSeed())
		DeferCleanup(func() { cleanupVolume(cli, volumeName) })

		// First restore — import old content into a fresh volume.
		buildTestExtractDirWithServiceConfig(extractDir, minimalCfg)
		buildTestExtractDirWithSyntheticPAMVolume(extractDir, "old-marker.txt")

		firstDestDir, err := os.MkdirTemp("", "restore-config-dest-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(firstDestDir) })

		d := restore.NewPodmanRestoreDeployer(log,
			restore.WithServiceHandler(restore.NewSystemctlServiceHandler([]string{})),
			restore.WithServiceConfigPath(filepath.Join(firstDestDir, "service-config.yaml")),
			restore.WithContainerCLI(cli),
			restore.WithPAMIssuerVolumeName(volumeName),
		)
		Expect(d.RestoreConfig(ctx, extractDir)).To(Succeed())

		// Second restore — overwrite the volume with new content.
		secondExtractDir, err := os.MkdirTemp("", "restore-config-overwrite-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(secondExtractDir) })

		buildTestExtractDirWithServiceConfig(secondExtractDir, minimalCfg)
		buildTestExtractDirWithSyntheticPAMVolume(secondExtractDir, "new-marker.txt")

		secondDestDir, err := os.MkdirTemp("", "restore-config-dest2-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(secondDestDir) })

		d2 := restore.NewPodmanRestoreDeployer(log,
			restore.WithServiceHandler(restore.NewSystemctlServiceHandler([]string{})),
			restore.WithServiceConfigPath(filepath.Join(secondDestDir, "service-config.yaml")),
			restore.WithContainerCLI(cli),
			restore.WithPAMIssuerVolumeName(volumeName),
		)
		Expect(d2.RestoreConfig(ctx, secondExtractDir)).To(Succeed())

		mountpoint, err := podmanVolumeMountpoint(ctx, cli, volumeName)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(mountpoint, "new-marker.txt")).To(BeAnExistingFile(),
			"new content must be present after second restore")
		Expect(filepath.Join(mountpoint, "old-marker.txt")).NotTo(BeAnExistingFile(),
			"old content must be absent after second restore — import must replace, not overlay")
	})

	It("When the PAM volume archive is absent it should succeed", func() {
		buildTestExtractDirWithServiceConfig(extractDir, minimalCfg)

		destDir, err := os.MkdirTemp("", "restore-config-dest-*")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(destDir) })

		d := restore.NewPodmanRestoreDeployer(log,
			restore.WithServiceHandler(restore.NewSystemctlServiceHandler([]string{})),
			restore.WithServiceConfigPath(filepath.Join(destDir, "service-config.yaml")),
			restore.WithContainerCLI(cli),
		)
		Expect(d.RestoreConfig(ctx, extractDir)).To(Succeed())
	})
})
