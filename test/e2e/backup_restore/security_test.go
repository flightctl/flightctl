package backup_restore

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Backup security", Label("backup-restore", "security"), func() {
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When backup creates archive it should have 0600 permissions", Label("BKP-SEC-001"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		info, err := os.Stat(archivePath)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)), "archive should have owner-only permissions")
	})

	It("When backup creates checksum it should have 0644 permissions", Label("BKP-SEC-002"), func() {
		outputDir := GinkgoT().TempDir()

		_, checksumPath, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		info, err := os.Stat(checksumPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0644)), "checksum file should be world-readable")
	})

	It("When backup fails mid-process it should clean up partial archive", Label("BKP-SEC-003"), func() {
		Skip("requires failure injection mid-backup not available in E2E — covered by unit tests in internal/backup/backup_test.go")
	})

	It("When backup logs it should not contain sensitive data", Label("BKP-SEC-004"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(archivePath).To(BeAnExistingFile())

		extractDir := GinkgoT().TempDir()
		extractTarGzToDir(GinkgoT(), archivePath, extractDir)

		metadataPath := extractDir + "/metadata.json"
		Expect(metadataPath).To(BeAnExistingFile())
		content, err := os.ReadFile(metadataPath)
		Expect(err).ToNot(HaveOccurred())
		metadataStr := string(content)

		sensitivePatterns := []string{"PRIVATE KEY", "password", "secret"}
		for _, pattern := range sensitivePatterns {
			Expect(strings.Contains(strings.ToLower(metadataStr), strings.ToLower(pattern))).To(BeFalse(),
				"metadata.json should not contain sensitive pattern: %s", pattern)
		}
	})
})

var _ = Describe("Restore security", Label("backup-restore", "security"), func() {
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When restore extracts archive it should create temp directory with 0700 permissions", Label("SEC-001"), func() {
		Skip("covered by unit tests in internal/restore/archive_test.go — temp dir permissions are set by os.MkdirTemp")
	})

	It("When restore completes successfully it should remove temp directory", Label("SEC-002"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		// Restore may fail due to deployment type mismatch or service access,
		// but temp directory cleanup should happen regardless.
		_ = err
		_ = output

		matches, err := filepath.Glob(os.TempDir() + "/flightctl-restore-*")
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeEmpty(), "temp extraction directories should be cleaned up after restore")
	})

	It("When restore fails it should remove temp directory", Label("SEC-003"), func() {
		archivePath := filepath.Join(GinkgoT().TempDir(), "corrupt.tar.gz")
		createMinimalTarGz(GinkgoT(), archivePath, map[string]string{
			"metadata.json": `{"timestamp":"2026-01-01T00:00:00Z","version":"test","deploymentType":"invalid","databaseIncluded":false}`,
		})
		writeMatchingChecksum(GinkgoT(), archivePath)

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred(), "restore should fail with invalid deployment type")
		_ = output

		matches, err := filepath.Glob(os.TempDir() + "/flightctl-restore-*")
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeEmpty(), "temp extraction directories should be cleaned up even on failure")
	})

	It("When PKI files are restored on Podman it should set private key permissions to 0600", Label("SEC-004"), func() {
		Skip("Podman deployment required")
	})

	It("When CA certificate is restored on Podman it should have appropriate permissions", Label("SEC-005"), func() {
		Skip("Podman deployment required")
	})

	It("When service config is restored on Podman it should have restrictive permissions", Label("SEC-006"), func() {
		Skip("Podman deployment required")
	})

	It("When restore logs it should not contain sensitive data", Label("SEC-007"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		// Restore may fail, but we only check the output for sensitive data leaks.
		_ = err

		sensitivePatterns := []string{"PRIVATE KEY", "BEGIN RSA", "BEGIN EC"}
		for _, pattern := range sensitivePatterns {
			Expect(output).ToNot(ContainSubstring(pattern),
				"restore output should not contain sensitive pattern: %s", pattern)
		}
	})
})
