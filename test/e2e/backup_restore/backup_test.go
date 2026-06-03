package backup_restore

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Backup command", Label("backup-restore", "backup"), func() {
	var harness *e2e.Harness
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When backup runs on healthy deployment it should produce valid archive with checksum and expected structure", Label("OCP-89120", "OCP-89121", "OCP-89122"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, checksumPath, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred(), "backup should succeed on healthy deployment")

		By("Verifying archive filename matches timestamp naming convention")
		basename := filepath.Base(archivePath)
		Expect(basename).To(MatchRegexp(`^flightctl-backup-\d{8}T\d{6}Z\.tar\.gz$`))

		By("Verifying SHA256 checksum file exists")
		Expect(checksumPath).To(BeAnExistingFile())

		By("Verifying checksum matches archive content")
		archiveData, err := os.ReadFile(archivePath)
		Expect(err).ToNot(HaveOccurred())
		hash := sha256.Sum256(archiveData)
		computedHash := hex.EncodeToString(hash[:])

		checksumContent, err := os.ReadFile(checksumPath)
		Expect(err).ToNot(HaveOccurred())
		parts := strings.SplitN(string(checksumContent), "  ", 2)
		Expect(parts).To(HaveLen(2), "checksum format should be '<hash>  <filename>'")
		Expect(parts[0]).To(Equal(computedHash), "SHA256 checksum should match archive content")

		By("Verifying archive contains expected directory structure")
		entries := extractTarGzEntries(GinkgoT(), archivePath)
		Expect(entries).To(ContainElement("metadata.json"), "archive should contain metadata.json")
		Expect(hasEntryWithPrefix(entries, "pki/")).To(BeTrue(), "archive should contain pki/ directory")

		extractDir := GinkgoT().TempDir()
		extractTarGzToDir(GinkgoT(), archivePath, extractDir)

		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.BuiltinDatabaseWorkloadAvailable() {
			By("Verifying db/dump.sql exists with valid PostgreSQL content (internal DB)")
			Expect(hasEntryWithPrefix(entries, "db/")).To(BeTrue(), "archive should contain db/ directory")
			dumpPath := filepath.Join(extractDir, "db", "dump.sql")
			Expect(dumpPath).To(BeAnExistingFile())
			dumpContent, err := os.ReadFile(dumpPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(dumpContent)).To(BeNumerically(">", 0), "dump.sql should have content")
			dumpStr := string(dumpContent)
			Expect(dumpStr).To(ContainSubstring("PostgreSQL database dump"), "dump should have PostgreSQL header")
		} else {
			By("Verifying db/ directory is absent (external DB)")
			Expect(hasEntryWithPrefix(entries, "db/")).To(BeFalse(), "archive should not contain db/ directory with external DB")
		}

		By("Verifying pki/ directory contains files with content")
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

	It("When backup runs it should include correct version and deployment type in metadata", Label("OCP-89126"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		extractDir := GinkgoT().TempDir()
		extractTarGzToDir(GinkgoT(), archivePath, extractDir)

		metadataPath := filepath.Join(extractDir, "metadata.json")
		Expect(metadataPath).To(BeAnExistingFile())

		metadataBytes, err := os.ReadFile(metadataPath)
		Expect(err).ToNot(HaveOccurred())

		var metadata backup.BackupMetadata
		Expect(json.Unmarshal(metadataBytes, &metadata)).To(Succeed())

		Expect(metadata.Version).ToNot(BeEmpty(), "version should be non-empty")
		Expect(string(metadata.DeploymentType)).To(BeElementOf("kubernetes", "podman"), "deployment type should be kubernetes or podman")
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.BuiltinDatabaseWorkloadAvailable() {
			Expect(metadata.DatabaseIncluded).To(BeTrue(), "database should be included for internal DB")
		} else {
			Expect(metadata.DatabaseIncluded).To(BeFalse(), "database should not be included for external DB")
		}
		Expect(metadata.Timestamp.IsZero()).To(BeFalse(), "timestamp should be set")
	})

	It("When backup runs it should collect service configuration", Label("OCP-89123"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		entries := extractTarGzEntries(GinkgoT(), archivePath)
		Expect(hasEntryWithPrefix(entries, "config/")).To(BeTrue(), "archive should contain config/ directory")

		extractDir := GinkgoT().TempDir()
		extractTarGzToDir(GinkgoT(), archivePath, extractDir)
		configDir := filepath.Join(extractDir, "config")
		Expect(configDir).To(BeADirectory())
		configFiles, err := os.ReadDir(configDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(configFiles)).To(BeNumerically(">", 0), "config/ should contain files")
	})

	It("When backup runs without TTY it should complete non-interactively", Label("OCP-89125"), func() {
		outputDir := GinkgoT().TempDir()

		archivePath, checksumPath, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred(), "backup should complete without interactive prompts")
		Expect(archivePath).To(BeAnExistingFile())
		Expect(checksumPath).To(BeAnExistingFile())
	})

	It("When backup binary exists it should be executable", Label("OCP-89127"), func() {
		binaryPath := harness.GetFlightctlBackupPath()
		info, err := os.Stat(binaryPath)
		Expect(err).ToNot(HaveOccurred(), "flightctl-backup binary should exist at %s", binaryPath)
		Expect(info.Mode().Perm()&0o111).ToNot(BeZero(), "binary should have execute permission")
	})

	It("When external DB is configured backup should produce archive without db/dump.sql", Label("external-db"), func() {
		p := setup.GetDefaultProviders()
		if p != nil && p.Infra != nil && p.Infra.BuiltinDatabaseWorkloadAvailable() {
			Skip("test only runs with external DB (db.type=external)")
		}

		outputDir := GinkgoT().TempDir()
		archivePath, checksumPath, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred(), "backup should succeed with external DB")
		Expect(archivePath).To(BeAnExistingFile())
		Expect(checksumPath).To(BeAnExistingFile())

		entries := extractTarGzEntries(GinkgoT(), archivePath)
		Expect(entries).To(ContainElement("metadata.json"))
		Expect(hasEntryWithPrefix(entries, "pki/")).To(BeTrue(), "PKI should be backed up even with external DB")
		Expect(hasEntryWithPrefix(entries, "db/")).To(BeFalse(), "db/ should not be present with external DB")

		extractDir := GinkgoT().TempDir()
		extractTarGzToDir(GinkgoT(), archivePath, extractDir)
		metadataBytes, err := os.ReadFile(filepath.Join(extractDir, "metadata.json"))
		Expect(err).ToNot(HaveOccurred())
		var metadata backup.BackupMetadata
		Expect(json.Unmarshal(metadataBytes, &metadata)).To(Succeed())
		Expect(metadata.DatabaseIncluded).To(BeFalse(), "metadata should indicate database not included")
	})
})

func extractTarGzEntries(t GinkgoTInterface, archivePath string) []string {
	t.Helper()

	file, err := os.Open(archivePath)
	Expect(err).ToNot(HaveOccurred())
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	Expect(err).ToNot(HaveOccurred())
	defer gzr.Close()

	var entries []string
	tarReader := tar.NewReader(gzr)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		Expect(err).ToNot(HaveOccurred())
		entries = append(entries, header.Name)
	}
	return entries
}

func extractTarGzToDir(t GinkgoTInterface, archivePath string, destDir string) {
	t.Helper()

	file, err := os.Open(archivePath)
	Expect(err).ToNot(HaveOccurred())
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	Expect(err).ToNot(HaveOccurred())
	defer gzr.Close()

	tarReader := tar.NewReader(gzr)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		Expect(err).ToNot(HaveOccurred())

		target := filepath.Join(destDir, header.Name) //nolint:gosec
		switch header.Typeflag {
		case tar.TypeDir:
			Expect(os.MkdirAll(target, os.FileMode(header.Mode))).To(Succeed())
		case tar.TypeReg:
			Expect(os.MkdirAll(filepath.Dir(target), 0o755)).To(Succeed())
			outFile, err := os.Create(target)
			Expect(err).ToNot(HaveOccurred())
			_, err = io.Copy(outFile, tarReader) //nolint:gosec
			outFile.Close()
			Expect(err).ToNot(HaveOccurred())
		}
	}
}

func hasEntryWithPrefix(entries []string, prefix string) bool {
	for _, e := range entries {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
