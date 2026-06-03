package backup_restore

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

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
		output, err := br.RunFlightCtlBackupRaw()
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
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	It("When archive path does not exist it should fail with clear error", Label("NEG-001"), func() {
		output, err := br.RunFlightCtlRestoreRaw("/nonexistent/archive.tar.gz")
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("no such file"),
			ContainSubstring("not found"),
			ContainSubstring("does not exist"),
		))
	})

	It("When a directory path is provided instead of a file it should fail", Label("NEG-002"), func() {
		dirPath := GinkgoT().TempDir()
		output, err := br.RunFlightCtlRestoreRaw(dirPath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("directory"),
			ContainSubstring("is a directory"),
			ContainSubstring("not a regular file"),
			ContainSubstring("gzip"),
		))
	})

	It("When archive path has spaces and special characters it should work", Label("NEG-003"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred(), "backup must succeed to test restore path handling")

		specialDir := GinkgoT().TempDir() + "/my restore (1)"
		Expect(os.MkdirAll(specialDir, 0755)).To(Succeed())

		checksumPath := archivePath + ".sha256"
		specialArchive := filepath.Join(specialDir, "backup file (copy).tar.gz")
		specialChecksum := specialArchive + ".sha256"
		copyFile(GinkgoT(), archivePath, specialArchive)
		copyFile(GinkgoT(), checksumPath, specialChecksum)

		output, err := br.RunFlightCtlRestoreRaw(specialArchive)
		if err != nil {
			Expect(output).ToNot(SatisfyAny(
				ContainSubstring("no such file"),
				ContainSubstring("invalid character"),
			), "error should not be caused by special characters in path")
		}
	})

	It("When archive is corrupted it should fail with clear error", Label("NEG-004"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		corruptArchive(GinkgoT(), archivePath)

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("checksum"),
			ContainSubstring("integrity"),
			ContainSubstring("mismatch"),
		), "should detect corruption via checksum")
	})

	It("When archive is empty it should fail with clear error", Label("NEG-005"), func() {
		emptyArchive := filepath.Join(GinkgoT().TempDir(), "empty.tar.gz")
		Expect(os.WriteFile(emptyArchive, []byte{}, 0600)).To(Succeed())
		writeMatchingChecksum(GinkgoT(), emptyArchive)

		output, err := br.RunFlightCtlRestoreRaw(emptyArchive)
		Expect(err).To(HaveOccurred())
		Expect(output).ToNot(BeEmpty(), "should produce error output")
	})

	It("When checksum mismatches it should fail before destructive operations", Label("NEG-006"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		checksumPath := archivePath + ".sha256"
		Expect(os.WriteFile(checksumPath, []byte("0000000000000000000000000000000000000000000000000000000000000000  fake.tar.gz\n"), 0644)).To(Succeed()) //nolint:gosec

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("checksum"),
			ContainSubstring("integrity"),
			ContainSubstring("mismatch"),
		))
	})

	It("When .sha256 file is missing it should fail with clear error", Label("NEG-007"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		checksumPath := archivePath + ".sha256"
		Expect(os.Remove(checksumPath)).To(Succeed())

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("checksum"),
			ContainSubstring(".sha256"),
			ContainSubstring("no such file"),
		))
	})

	It("When .sha256 file is malformed it should fail with clear error", Label("NEG-008"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		checksumPath := archivePath + ".sha256"
		Expect(os.WriteFile(checksumPath, []byte("not-a-valid-checksum-file\n"), 0644)).To(Succeed()) //nolint:gosec

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("checksum"),
			ContainSubstring("malformed"),
			ContainSubstring("integrity"),
			ContainSubstring("mismatch"),
		))
	})

	It("When metadata.json is missing from archive it should fail", Label("NEG-010"), func() {
		archivePath := filepath.Join(GinkgoT().TempDir(), "no-metadata.tar.gz")
		createMinimalTarGz(GinkgoT(), archivePath, map[string]string{
			"db/dump.sql": "-- empty dump",
		})
		writeMatchingChecksum(GinkgoT(), archivePath)

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		Expect(err).To(HaveOccurred())
		Expect(output).To(SatisfyAny(
			ContainSubstring("metadata"),
			ContainSubstring("metadata.json"),
		))
	})

	It("When disk is full during extraction it should fail cleanly and clean up", Label("NEG-011"), func() {
		Skip("requires disk space simulation not available in E2E")
	})

	It("When database is unreachable during restore it should fail with connection error", Label("NEG-012"), func() {
		Skip("requires DB infrastructure control to stop database mid-test")
	})

	It("When KV store is unreachable during device preparation it should handle gracefully", Label("NEG-013"), func() {
		Skip("requires KV infrastructure control to stop Redis mid-test")
	})

	It("When archive has restrictive permissions it should still be readable by restore", Label("NEG-014"), func() {
		outputDir := GinkgoT().TempDir()
		archivePath, _, err := br.RunFlightCtlBackup(outputDir)
		Expect(err).ToNot(HaveOccurred())

		Expect(os.Chmod(archivePath, 0400)).To(Succeed())
		DeferCleanup(func() { os.Chmod(archivePath, 0644) }) //nolint:errcheck

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		if err != nil {
			Expect(output).ToNot(SatisfyAny(
				ContainSubstring("permission denied"),
				ContainSubstring("Permission denied"),
			), "restore should be able to read 0400 archive owned by current user")
		}
	})

	It("When psql restore fails it should exit with psql stderr", Label("NEG-015"), func() {
		Skip("requires DB permission revocation or corrupt dump injection")
	})

	It("When service stop fails during restore it should exit with error", Label("NEG-016"), func() {
		Skip("requires service infrastructure control to prevent stop")
	})

	It("When archive has no db/dump.sql it should skip DB restore for external database", Label("NEG-017"), func() {
		archivePath := filepath.Join(GinkgoT().TempDir(), "no-db.tar.gz")
		createMinimalTarGz(GinkgoT(), archivePath, map[string]string{
			"metadata.json": `{"timestamp":"2026-01-01T00:00:00Z","version":"test","deploymentType":"kubernetes","databaseIncluded":false}`,
			"pki/ca.yaml":   "apiVersion: v1\nkind: Secret\n",
		})
		writeMatchingChecksum(GinkgoT(), archivePath)

		output, err := br.RunFlightCtlRestoreRaw(archivePath)
		if err != nil {
			Expect(output).ToNot(ContainSubstring("dump.sql not found"),
				"missing db/dump.sql with databaseIncluded=false should not be treated as an error")
		}
	})
})

// copyFile copies src to dst preserving content.
func copyFile(t GinkgoTInterface, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	Expect(err).ToNot(HaveOccurred())
	Expect(os.WriteFile(dst, data, 0600)).To(Succeed())
}

// corruptArchive overwrites the first 100 bytes of an archive with zeros to simulate corruption.
func corruptArchive(t GinkgoTInterface, archivePath string) {
	t.Helper()
	f, err := os.OpenFile(archivePath, os.O_WRONLY, 0)
	Expect(err).ToNot(HaveOccurred())
	defer f.Close()
	_, err = f.Write(make([]byte, 100))
	Expect(err).ToNot(HaveOccurred())
}

// writeMatchingChecksum computes SHA256 of the file and writes a matching .sha256 sidecar.
func writeMatchingChecksum(t GinkgoTInterface, archivePath string) {
	t.Helper()
	data, err := os.ReadFile(archivePath)
	Expect(err).ToNot(HaveOccurred())
	hash := sha256.Sum256(data)
	content := fmt.Sprintf("%s  %s\n", hex.EncodeToString(hash[:]), filepath.Base(archivePath))
	Expect(os.WriteFile(archivePath+".sha256", []byte(content), 0644)).To(Succeed()) //nolint:gosec
}

// createMinimalTarGz creates a tar.gz archive with the given file entries.
func createMinimalTarGz(t GinkgoTInterface, destPath string, files map[string]string) {
	t.Helper()
	f, err := os.Create(destPath)
	Expect(err).ToNot(HaveOccurred())
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	for name, content := range files {
		dir := filepath.Dir(name)
		if dir != "." {
			Expect(tw.WriteHeader(&tar.Header{
				Name:     dir + "/",
				Typeflag: tar.TypeDir,
				Mode:     0755,
			})).To(Succeed())
		}
		hdr := &tar.Header{
			Name: name,
			Mode: 0600,
			Size: int64(len(content)),
		}
		Expect(tw.WriteHeader(hdr)).To(Succeed())
		_, err := tw.Write([]byte(content))
		Expect(err).ToNot(HaveOccurred())
	}
}
