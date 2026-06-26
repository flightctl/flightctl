package backup_restore

import (
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	restoreTempDirPattern = "/flightctl-restore-*"
)

var sensitivePatterns = []string{"PRIVATE KEY", "BEGIN RSA", "BEGIN EC", "password"}

var _ = Describe("Backup security", Label("backup-restore", "security"), func() {
	var br *e2e.BackupRestore

	BeforeEach(func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When backup creates archive it should have 0600 permissions", Label("89572"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(archivePath).To(BeAnExistingFile())

		info, err := os.Stat(archivePath)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)), "archive should have owner-only permissions")
	})

	It("When backup creates checksum it should have 0644 permissions", Label("89571"), func() {
		outputDir := GinkgoT().TempDir()

		_, checksumPath, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(checksumPath).To(BeAnExistingFile())

		info, err := os.Stat(checksumPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0644)), "checksum file should be world-readable")
	})

	It("When backup logs it should not contain sensitive data", Label("89576"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(archivePath).To(BeAnExistingFile())

		extractDir := GinkgoT().TempDir()
		Expect(extractTarGzToDir(archivePath, extractDir)).To(Succeed())

		metadataPath := filepath.Join(extractDir, "metadata.json")
		Expect(metadataPath).To(BeAnExistingFile())
		content, err := os.ReadFile(metadataPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(content)).To(BeNumerically(">", 0), "metadata.json should not be empty")

		assertNoSensitiveData(string(content), "metadata.json")
	})
})

var _ = Describe("Restore security", Label("backup-restore", "security"), func() {
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When restore completes it should remove temp directory", Label("89574"), func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		Expect(br.RunFlightCtlRestoreFromArchive(archivePath)).To(Succeed(), "restore should succeed")

		assertNoRestoreTempDirs()
	})

	It("When restore fails it should remove temp directory", Label("89573"), func() {
		archivePath := filepath.Join(GinkgoT().TempDir(), "corrupt.tar.gz")
		Expect(createMinimalTarGz(archivePath, map[string]string{
			"metadata.json": `{"timestamp":"2026-01-01T00:00:00Z","version":"test","deploymentType":"invalid","databaseIncluded":false}`,
		})).To(Succeed())
		Expect(writeMatchingChecksum(archivePath)).To(Succeed())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred(), "restore should fail with invalid deployment type")
		Expect(output).ToNot(BeEmpty(), "error output should not be empty")

		assertNoRestoreTempDirs()
	})

	It("When restore logs it should not contain sensitive data", Label("89579"), func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).ToNot(HaveOccurred(), "restore should succeed before checking output for sensitive data")

		assertNoSensitiveData(output, "restore output")
	})
})
