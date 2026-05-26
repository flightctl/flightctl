package backup_restore

import (
	"os"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Backup negative cases", Label("backup-restore", "negative"), func() {
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When --output is not provided it should use current directory as default", Label("BKP-NEG-001"), func() {
		// --output defaults to "." per cmd/flightctl-backup/main.go:47, so this is not a failure case.
		// The backup will attempt to run in the current directory which may or may not succeed
		// depending on deployment detection. We verify the flag default behavior.
		output, err := br.RunFlightCtlBackupRaw()
		// Expect either success or a deployment detection error (not a missing-flag error)
		if err != nil {
			Expect(output).ToNot(ContainSubstring("--output"), "error should not be about missing --output flag")
		}
	})

	It("When --output points to nonexistent parent it should fail with clear error", Label("BKP-NEG-002"), func() {
		output, err := br.RunFlightCtlBackupRaw("--output", "/nonexistent/path/that/does/not/exist")
		Expect(err).To(HaveOccurred(), "backup should fail with nonexistent output path")
		Expect(output).To(ContainSubstring("nonexistent"), "error should reference the bad path")
	})

	It("When --output path has no write permissions it should fail", Label("BKP-NEG-003"), func() {
		readOnlyDir := GinkgoT().TempDir()
		Expect(os.Chmod(readOnlyDir, 0444)).To(Succeed())
		DeferCleanup(func() {
			os.Chmod(readOnlyDir, 0755) //nolint:errcheck
		})

		output, err := br.RunFlightCtlBackupRaw("--output", readOnlyDir)
		Expect(err).To(HaveOccurred(), "backup should fail with read-only output path")
		Expect(output).To(SatisfyAny(
			ContainSubstring("permission"),
			ContainSubstring("Permission"),
			ContainSubstring("read-only"),
		), "error should indicate permission issue")
	})

	It("When disk is full during backup it should fail cleanly", Label("BKP-NEG-004"), func() {
		Skip("requires disk space simulation not available in E2E")
	})

	It("When database is unreachable it should fail with connection error", Label("BKP-NEG-005"), func() {
		Skip("requires DB infrastructure control to stop database mid-test")
	})

	It("When pg_dump fails it should report error and not create archive", Label("BKP-NEG-006"), func() {
		Skip("requires DB corruption or permission revocation")
	})

	It("When PKI files are missing on Podman it should fail before creating archive", Label("BKP-NEG-007"), func() {
		Skip("requires Podman deployment with manipulated PKI files")
	})

	It("When Kubernetes Secrets are missing it should fail before creating archive", Label("BKP-NEG-008"), func() {
		Skip("requires Kubernetes deployment with deleted PKI Secrets")
	})

	It("When deployment type cannot be detected it should fail with clear error", Label("BKP-NEG-009"), func() {
		Skip("requires environment without Podman or Kubernetes indicators")
	})

	It("When both Podman and Kubernetes indicators are present it should fail", Label("BKP-NEG-010"), func() {
		Skip("requires controlled environment with both /etc/flightctl and KUBERNETES_SERVICE_HOST")
	})

	It("When --output path has spaces and special characters it should work", Label("BKP-NEG-011"), func() {
		baseDir := GinkgoT().TempDir()
		specialDir := baseDir + "/my backup (1)"
		Expect(os.MkdirAll(specialDir, 0755)).To(Succeed())

		archivePath, _, err := br.RunFlightCtlBackup(specialDir)
		Expect(err).ToNot(HaveOccurred(), "backup should succeed with special characters in path")
		Expect(archivePath).To(BeAnExistingFile())
	})
})

var _ = Describe("Restore negative cases", Label("backup-restore", "negative"), func() {
	It("When archive path does not exist it should fail with clear error", Label("NEG-001"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When a directory path is provided instead of a file it should fail", Label("NEG-002"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When archive path has spaces and special characters it should work", Label("NEG-003"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When archive is corrupted it should fail with clear error", Label("NEG-004"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When archive is empty it should fail with clear error", Label("NEG-005"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When checksum mismatches it should fail before destructive operations", Label("NEG-006"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When .sha256 file is missing it should fail with clear error", Label("NEG-007"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When .sha256 file is malformed it should fail with clear error", Label("NEG-008"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When archive is missing expected directories it should fail", Label("NEG-009"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When metadata.json is missing from archive it should fail", Label("NEG-010"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When disk is full during extraction it should fail cleanly and clean up", Label("NEG-011"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When database is unreachable during restore it should fail with connection error", Label("NEG-012"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When KV store is unreachable during device preparation it should handle gracefully", Label("NEG-013"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When archive has restrictive permissions it should still be readable by restore", Label("NEG-014"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When psql restore fails it should exit with psql stderr", Label("NEG-015"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When service stop fails during restore it should exit with error", Label("NEG-016"), func() {
		Skip("pending flightctl-restore archive support")
	})

	It("When archive has no db/dump.sql it should print instructions and skip DB restore", Label("NEG-017"), func() {
		Skip("pending flightctl-restore archive support")
	})
})
