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

const (
	archiveNamePattern = `^flightctl-backup-\d{8}T\d{6}Z\.tar\.gz$`
	checksumSuffix     = ".sha256"
)

var _ = Describe("Backup command", Label("backup-restore", "backup"), func() {
	var harness *e2e.Harness
	var br *e2e.BackupRestore
	var archivePath, checksumPath, extractDir string
	var entries []string

	BeforeEach(func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		harness = e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())

		outputDir := GinkgoT().TempDir()
		var err error
		archivePath, checksumPath, err = br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred(), "backup should succeed on healthy deployment")
		Expect(archivePath).To(BeAnExistingFile(), "archive file should exist")
		Expect(checksumPath).To(BeAnExistingFile(), "checksum file should exist")

		entries, err = extractTarGzEntries(archivePath)
		Expect(err).ToNot(HaveOccurred())

		extractDir = GinkgoT().TempDir()
		Expect(extractTarGzToDir(archivePath, extractDir)).To(Succeed())
	})

	It("When backup runs it should produce valid archive with checksum and expected structure", Label("89120"), func() {
		By("Verifying archive filename matches timestamp naming convention")
		Expect(filepath.Base(archivePath)).To(MatchRegexp(archiveNamePattern))

		By("Verifying checksum matches archive content")
		computedHash, err := computeSHA256(archivePath)
		Expect(err).ToNot(HaveOccurred())
		fileHash, err := readChecksumHash(checksumPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(fileHash).To(Equal(computedHash), "SHA256 checksum should match archive content")

		By("Verifying archive contains expected directory structure")
		Expect(entries).To(ContainElement("metadata.json"), "archive should contain metadata.json")
		Expect(hasEntryWithPrefix(entries, "pki/")).To(BeTrue(), "archive should contain pki/ directory")
	})

	It("When backup runs on deployment with internal DB it should include db/dump.sql", Label("89121"), func() {
		p := setup.GetDefaultProviders()
		if p == nil || p.Infra == nil || !p.Infra.BuiltinDatabaseWorkloadAvailable() {
			Skip("requires internal DB (db.type=builtin)")
		}

		Expect(hasEntryWithPrefix(entries, "db/")).To(BeTrue(), "archive should contain db/ directory")
		dumpPath := filepath.Join(extractDir, "db", "dump.sql")
		Expect(dumpPath).To(BeAnExistingFile())
		dumpContent, err := os.ReadFile(dumpPath)
		Expect(err).ToNot(HaveOccurred())
		dump := string(dumpContent)
		Expect(dump).To(ContainSubstring("PostgreSQL database dump"), "dump should have PostgreSQL header")
		for _, table := range []string{"devices", "fleets", "enrollment_requests", "repositories"} {
			Expect(dump).To(ContainSubstring(table), "dump should contain table: %s", table)
		}
	})

	It("When backup runs it should collect PKI materials", Label("89122"), func() {
		Expect(hasEntryWithPrefix(entries, "pki/")).To(BeTrue(), "archive should contain pki/ directory")
		pkiDir := filepath.Join(extractDir, "pki")
		Expect(pkiDir).To(BeADirectory())
		pkiFiles, err := os.ReadDir(pkiDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(pkiFiles)).To(BeNumerically(">", 0), "pki/ should contain files")
		for _, f := range pkiFiles {
			if f.IsDir() {
				continue
			}
			info, err := f.Info()
			Expect(err).ToNot(HaveOccurred())
			Expect(info.Size()).To(BeNumerically(">", 0), "PKI file %s should have content", f.Name())
		}
	})

	It("When backup runs it should include correct version and deployment type in metadata", Label("89126"), func() {
		metadata, err := readBackupMetadata(filepath.Join(extractDir, "metadata.json"))
		Expect(err).ToNot(HaveOccurred())

		versionOutput, err := br.RunFlightCtlBackupRaw("version")
		Expect(err).ToNot(HaveOccurred(), "flightctl-backup version should succeed")
		expectedVersion := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(versionOutput), "Flight Control Backup Version:"))
		Expect(metadata.Version).To(Equal(expectedVersion), "metadata version should match the backup binary version")
		Expect(string(metadata.DeploymentType)).To(BeElementOf("kubernetes", "podman"), "deployment type should be kubernetes or podman")
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.BuiltinDatabaseWorkloadAvailable() {
			Expect(metadata.DatabaseIncluded).To(BeTrue(), "database should be included for internal DB")
		} else {
			Expect(metadata.DatabaseIncluded).To(BeFalse(), "database should not be included for external DB")
		}
		Expect(metadata.Timestamp.IsZero()).To(BeFalse(), "timestamp should be set")
	})

	It("When backup runs it should collect service configuration", Label("89123"), func() {
		Expect(hasEntryWithPrefix(entries, "config/")).To(BeTrue(), "archive should contain config/ directory")
		configDir := filepath.Join(extractDir, "config")
		Expect(configDir).To(BeADirectory())
		configFiles, err := os.ReadDir(configDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(configFiles)).To(BeNumerically(">", 0), "config/ should contain files")
	})

	It("When backup runs without TTY it should complete non-interactively", Label("89125"), func() {
		Expect(archivePath).To(BeAnExistingFile(), "backup should complete without interactive prompts")
		Expect(checksumPath).To(BeAnExistingFile())
	})

	It("When backup runs without --output it should default to current directory and succeed", Label("89578"), func() {
		output, err := br.RunFlightCtlBackupRaw()
		Expect(err).ToNot(HaveOccurred(), "backup without --output should default to current directory")
		Expect(output).To(ContainSubstring("Backup completed"), "backup should complete successfully using default output directory")
	})

	It("When backup binary exists it should be executable", Label("89127"), func() {
		binaryPath := harness.GetFlightctlBackupPath()
		info, err := os.Stat(binaryPath)
		Expect(err).ToNot(HaveOccurred(), "flightctl-backup binary should exist at %s", binaryPath)
		Expect(info.Mode().Perm()&0o111).ToNot(BeZero(), "binary should have execute permission")
	})
})
