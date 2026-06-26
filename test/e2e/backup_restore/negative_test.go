package backup_restore

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	nonexistentOutputPath  = "/nonexistent/path/that/does/not/exist"
	nonexistentArchivePath = "/nonexistent/archive.tar.gz"
	fakeChecksumLine       = "0000000000000000000000000000000000000000000000000000000000000000  fake.tar.gz\n"
	fakeVersion            = "v0.0.0-fake"
)

var _ = Describe("Backup negative cases", Label("backup-restore", "negative"), func() {
	var br *e2e.BackupRestore

	BeforeEach(func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When --output points to nonexistent parent it should fail with clear error", Label("89592"), func() {
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
			Skip("quadlet runs with sudo which auto-creates directories")
		}
		output, err := br.RunFlightCtlBackupRaw("--output", nonexistentOutputPath)
		Expect(err).To(HaveOccurred(), "backup should fail with nonexistent output path")
		Expect(output).To(ContainSubstring("nonexistent"), "error should reference the bad path")
	})

	It("When --output path has no write permissions it should fail", Label("89591"), func() {
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
			Skip("quadlet runs with sudo which bypasses file permissions")
		}
		readOnlyDir := GinkgoT().TempDir()
		Expect(os.Chmod(readOnlyDir, 0444)).To(Succeed())
		DeferCleanup(restoreDirPermissions, readOnlyDir)

		output, err := br.RunFlightCtlBackupRaw("--output", readOnlyDir)
		Expect(err).To(HaveOccurred(), "backup should fail with read-only output path")
		Expect(output).To(SatisfyAny(
			ContainSubstring("permission"),
			ContainSubstring("Permission"),
			ContainSubstring("read-only"),
		), "error should indicate permission issue")
	})
})

var _ = Describe("External database backup and restore", Label("backup-restore"), func() {
	var harness *e2e.Harness
	var br *e2e.BackupRestore

	BeforeEach(func() {
		p := setup.GetDefaultProviders()
		if p == nil || p.Infra == nil || p.Infra.BuiltinDatabaseWorkloadAvailable() {
			Skip("test only applies to external DB deployments (ACM, quadlets with db.type=external)")
		}
		harness = e2e.GetWorkerHarness()
		br = newBackupRestore(harness, p)
	})

	It("When backup runs on external DB it should produce archive without database dump", Label("89590"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, checksumPath, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred(), "backup should succeed on external DB (skipping database dump)")
		Expect(archivePath).To(BeAnExistingFile(), "archive file should exist")
		Expect(checksumPath).To(BeAnExistingFile(), "checksum file should exist")

		By("Verifying archive does not contain db/dump.sql")
		entries, err := extractTarGzEntries(archivePath)
		Expect(err).ToNot(HaveOccurred())
		Expect(hasEntryWithPrefix(entries, "db/")).To(BeFalse(), "archive should not contain db/ directory for external DB")

		By("Verifying archive contains PKI and config")
		Expect(hasEntryWithPrefix(entries, "pki/")).To(BeTrue(), "archive should contain pki/ directory")
		Expect(entries).To(ContainElement("metadata.json"), "archive should contain metadata.json")

		By("Verifying metadata shows databaseIncluded=false")
		extractDir := GinkgoT().TempDir()
		Expect(extractTarGzToDir(archivePath, extractDir)).To(Succeed())
		metadata, err := readBackupMetadata(filepath.Join(extractDir, "metadata.json"))
		Expect(err).ToNot(HaveOccurred())
		Expect(metadata.DatabaseIncluded).To(BeFalse(), "metadata should indicate database was not included")
	})

	It("When restoring external DB archive it should restore PKI and config without database", Label("89589"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred(), "backup should succeed on external DB")

		By("Running restore from external DB backup archive")
		defer func() {
			Expect(br.VerifyAllServicesRunning()).To(Succeed(), "all services must be running after restore cleanup")
		}()
		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).ToNot(HaveOccurred(), "restore should succeed for external DB archive")
		Expect(output).To(SatisfyAny(
			ContainSubstring("external database"),
			ContainSubstring("No database dump found"),
			ContainSubstring("database dump"),
		), "restore output should indicate external database was detected")

		By("Verifying all services were restarted after restore")
		Eventually(func() error {
			return br.VerifyAllServicesRunning()
		}, testutil.TIMEOUT_5M, testutil.POLLING).Should(Succeed(), "all services must be running after restore")

		By("Verifying API is responsive after restore")
		Eventually(func() error {
			_, loginErr := login.LoginToAPIWithToken(harness)
			return loginErr
		}, testutil.DURATION_TIMEOUT, testutil.POLLING).Should(Succeed(), "API should be responsive after restore")
	})
})

var _ = Describe("Restore negative cases", Label("backup-restore", "negative"), func() {
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When archive path does not exist it should fail with clear error", Label("89582"), func() {
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
			Skip("restore binary must run on quadlet host to detect deployment type")
		}
		output, err := br.RunFlightCtlRestoreRaw(nonexistentArchivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("no such file"),
			ContainSubstring("not found"),
			ContainSubstring("does not exist"),
		))
	})

	It("When archive is corrupted it should fail with checksum mismatch", Label("89588"), func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
			Skip("restore binary must run on quadlet host to detect deployment type")
		}
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(archivePath).To(BeAnExistingFile())

		Expect(corruptArchive(archivePath)).To(Succeed())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(ContainSubstring("checksum mismatch"), "should detect corruption via checksum verification")
	})

	It("When checksum mismatches it should fail before destructive operations", Label("89586"), func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
			Skip("restore binary must run on quadlet host to detect deployment type")
		}
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		checksumPath := archivePath + checksumSuffix
		Expect(os.WriteFile(checksumPath, []byte(fakeChecksumLine), 0644)).To(Succeed()) //nolint:gosec // G306: checksum is non-sensitive

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(ContainSubstring("checksum mismatch"), "should fail with checksum mismatch before any destructive operations")
	})

	It("When restoring archive from different deployment type it should fail with mismatch error", Label("89195"), func() {
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
			Skip("restore binary must run on quadlet host to detect deployment type")
		}
		wrongType := oppositeDeploymentType()

		archivePath := filepath.Join(GinkgoT().TempDir(), "wrong-type.tar.gz")
		Expect(createMinimalTarGz(archivePath, map[string]string{
			"metadata.json": fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:00Z","version":"test","deploymentType":"%s","databaseIncluded":false}`, wrongType),
			"pki/ca.yaml":   "apiVersion: v1\nkind: Secret\n",
		})).To(Succeed())
		Expect(writeMatchingChecksum(archivePath)).To(Succeed())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("deployment type"),
			ContainSubstring("mismatch"),
			ContainSubstring("type mismatch"),
		))
	})

	It("When restoring archive from different Flight Control version it should fail with version error", Label("89196"), func() {
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
			Skip("restore binary must run on quadlet host to detect deployment type")
		}
		deployType := currentDeploymentType()

		archivePath := filepath.Join(GinkgoT().TempDir(), "wrong-version.tar.gz")
		Expect(createMinimalTarGz(archivePath, map[string]string{
			"metadata.json": fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:00Z","version":"%s","deploymentType":"%s","databaseIncluded":false}`, fakeVersion, deployType),
			"pki/ca.yaml":   "apiVersion: v1\nkind: Secret\n",
		})).To(Succeed())
		Expect(writeMatchingChecksum(archivePath)).To(Succeed())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("version"),
			ContainSubstring("mismatch"),
			ContainSubstring("version mismatch"),
		))
	})
})
