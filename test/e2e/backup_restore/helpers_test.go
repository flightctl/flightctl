package backup_restore

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/gomega"
)

// --- Backup/restore shared helpers ---

// newBackupRestore returns a BackupRestore using the production backup/restore binaries.
func newBackupRestore(harness *e2e.Harness, p *infra.Providers) *e2e.BackupRestore {
	return harness.NewBackupRestore(p)
}

// motdInlineConfigProviderSpec returns a ConfigProviderSpec that writes content to /etc/motd (per test plan 4.1).
func motdInlineConfigProviderSpec() v1beta1.ConfigProviderSpec {
	mode := 0644
	inline := v1beta1.InlineConfigProviderSpec{
		Inline: []v1beta1.FileSpec{{
			Path:    "/etc/motd",
			Mode:    &mode,
			Content: "backup-restore-e2e\n",
		}},
		Name: "motd-inline",
	}
	var spec v1beta1.ConfigProviderSpec
	if err := spec.FromInlineConfigProviderSpec(inline); err != nil {
		panic(err)
	}
	return spec
}

// --- Archive helpers ---

// computeSHA256 returns the hex-encoded SHA256 hash of the file at the given path.
func computeSHA256(filePath string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("filePath must not be empty")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file for checksum: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// readChecksumHash reads a .sha256 sidecar file and returns the hex hash string.
// Expects the standard format: "<hex-hash>  <filename>\n".
func readChecksumHash(checksumPath string) (string, error) {
	if checksumPath == "" {
		return "", fmt.Errorf("checksumPath must not be empty")
	}
	content, err := os.ReadFile(checksumPath)
	if err != nil {
		return "", fmt.Errorf("read checksum file: %w", err)
	}
	if len(content) == 0 {
		return "", fmt.Errorf("checksum file is empty")
	}
	parts := strings.SplitN(string(content), "  ", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed checksum file: expected '<hash>  <filename>', got %q", string(content))
	}
	return parts[0], nil
}

// readBackupMetadata reads and parses metadata.json from an extracted backup directory.
func readBackupMetadata(metadataPath string) (*backup.BackupMetadata, error) {
	if metadataPath == "" {
		return nil, fmt.Errorf("metadataPath must not be empty")
	}
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("read metadata.json: %w", err)
	}
	if len(metadataBytes) == 0 {
		return nil, fmt.Errorf("metadata.json is empty")
	}
	var metadata backup.BackupMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, fmt.Errorf("parse metadata.json: %w", err)
	}
	return &metadata, nil
}

// extractTarGzEntries returns the list of entry names (files and directories) in a .tar.gz archive.
func extractTarGzEntries(archivePath string) ([]string, error) {
	if archivePath == "" {
		return nil, fmt.Errorf("archivePath must not be empty")
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer gzr.Close()

	var entries []string
	tarReader := tar.NewReader(gzr)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}
		entries = append(entries, header.Name)
	}
	return entries, nil
}

// extractTarGzToDir extracts a .tar.gz archive into the given destination directory.
func extractTarGzToDir(archivePath string, destDir string) error {
	if archivePath == "" || destDir == "" {
		return fmt.Errorf("archivePath and destDir must not be empty")
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gzr.Close()

	tarReader := tar.NewReader(gzr)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destDir, header.Name) //nolint:gosec
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("create directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent directory for %s: %w", target, err)
			}
			outFile, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			_, err = io.Copy(outFile, tarReader) //nolint:gosec
			outFile.Close()
			if err != nil {
				return fmt.Errorf("write file %s: %w", target, err)
			}
		}
	}
	return nil
}

// hasEntryWithPrefix returns true if any entry in the list starts with the given prefix.
func hasEntryWithPrefix(entries []string, prefix string) bool {
	for _, e := range entries {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// writeMatchingChecksum computes SHA256 of the file and writes a matching .sha256 sidecar.
func writeMatchingChecksum(archivePath string) error {
	if archivePath == "" {
		return fmt.Errorf("archivePath must not be empty")
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("read archive for checksum: %w", err)
	}
	hash := sha256.Sum256(data)
	content := fmt.Sprintf("%s  %s\n", hex.EncodeToString(hash[:]), filepath.Base(archivePath))
	return os.WriteFile(archivePath+checksumSuffix, []byte(content), 0644) //nolint:gosec // G306: checksum is non-sensitive
}

// createMinimalTarGz creates a .tar.gz archive with the given file entries.
func createMinimalTarGz(destPath string, files map[string]string) error {
	if destPath == "" {
		return fmt.Errorf("destPath must not be empty")
	}
	if len(files) == 0 {
		return fmt.Errorf("files must not be empty")
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	for name, content := range files {
		dir := filepath.Dir(name)
		if dir != "." {
			if err := tw.WriteHeader(&tar.Header{
				Name:     dir + "/",
				Typeflag: tar.TypeDir,
				Mode:     0755,
			}); err != nil {
				return fmt.Errorf("write dir header %s: %w", dir, err)
			}
		}
		hdr := &tar.Header{
			Name: name,
			Mode: 0600,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write file header %s: %w", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return fmt.Errorf("write file content %s: %w", name, err)
		}
	}
	return nil
}

// corruptArchive overwrites the first 100 bytes of an archive with zeros to simulate corruption.
func corruptArchive(archivePath string) error {
	if archivePath == "" {
		return fmt.Errorf("archivePath must not be empty")
	}
	f, err := os.OpenFile(archivePath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open archive for corruption: %w", err)
	}
	defer f.Close()
	_, err = f.Write(make([]byte, 100))
	if err != nil {
		return fmt.Errorf("write corruption bytes: %w", err)
	}
	return nil
}

// --- Deployment type helpers ---

// oppositeDeploymentType returns "podman" for K8s environments and "kubernetes" for quadlet.
func oppositeDeploymentType() string {
	p := setup.GetDefaultProviders()
	if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		return "kubernetes"
	}
	return "podman"
}

// currentDeploymentType returns the deployment type string matching the current environment.
func currentDeploymentType() string {
	p := setup.GetDefaultProviders()
	if p != nil && p.Infra != nil && p.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		return "podman"
	}
	return "kubernetes"
}

// --- Security helpers ---

// assertNoSensitiveData checks that the given content does not contain any known sensitive patterns
// (private key material, PEM headers, passwords). The word "secret" is intentionally excluded
// because the restore binary legitimately logs Kubernetes Secret resource names.
func assertNoSensitiveData(content, source string) {
	lower := strings.ToLower(content)
	for _, pattern := range sensitivePatterns {
		Expect(strings.Contains(lower, strings.ToLower(pattern))).To(BeFalse(),
			"%s should not contain sensitive pattern: %s", source, pattern)
	}
}

// assertNoRestoreTempDirs verifies that no flightctl-restore temp directories remain in os.TempDir().
func assertNoRestoreTempDirs() {
	matches, err := filepath.Glob(os.TempDir() + restoreTempDirPattern)
	Expect(err).ToNot(HaveOccurred())
	Expect(matches).To(BeEmpty(), "temp extraction directories should be cleaned up")
}

// restoreDirPermissions resets directory permissions to 0755 for cleanup.
func restoreDirPermissions(dir string) {
	os.Chmod(dir, 0755) //nolint:errcheck
}
