package restore

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/backup"
)

// ExtractArchive validates that archivePath exists and is a readable regular
// file, then extracts the tar.gz to a new temporary directory.
//
// Returns the path to the extracted directory on success. Caller is
// responsible for cleanup via os.RemoveAll.
//
// On error, returns ("", err) — the empty string is guaranteed so that the
// caller's cleanup guard (if extractDir != "") is always safe.
func ExtractArchive(ctx context.Context, archivePath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("context cancelled before extraction: %w", err)
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return "", fmt.Errorf("cannot access archive %q: %w", archivePath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("archive path %q is not a regular file", archivePath)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("cannot open archive %q: %w", archivePath, err)
	}
	defer f.Close()

	extractDir, err := os.MkdirTemp("", "flightctl-restore-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary extraction directory: %w", err)
	}

	if err := extractTarGz(ctx, f, extractDir); err != nil {
		os.RemoveAll(extractDir)
		return "", fmt.Errorf("failed to extract archive %q: %w", archivePath, err)
	}

	return extractDir, nil
}

// extractTarGz reads from r (a gzip-compressed tar stream) and writes all
// entries into destDir. Paths are sanitized to prevent directory traversal.
func extractTarGz(ctx context.Context, r io.Reader, destDir string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("failed to open gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("extraction cancelled: %w", err)
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return fmt.Errorf("failed to create directory %q: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return fmt.Errorf("failed to create parent directory for %q: %w", target, err)
			}
			if err := writeFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		}
	}

	return nil
}

// safeJoin joins base and name, returning an error if the result would escape
// base (path traversal protection).
func safeJoin(base, name string) (string, error) {
	target := filepath.Join(base, filepath.Clean("/"+name))
	if !strings.HasPrefix(target, filepath.Clean(base)+string(os.PathSeparator)) &&
		target != filepath.Clean(base) {
		return "", fmt.Errorf("archive entry %q would escape extraction directory", name)
	}
	return target, nil
}

// writeFile creates or truncates target and copies content from r into it.
func writeFile(target string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("failed to create file %q: %w", target, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("failed to write file %q: %w", target, err)
	}
	return nil
}

// ReadMetadata reads and unmarshals metadata.json from the root of an
// extracted archive directory. Returns a non-nil pointer on success.
// Returns nil and an error if the file is missing or contains invalid JSON.
func ReadMetadata(extractDir string) (*backup.BackupMetadata, error) {
	metadataPath := filepath.Join(extractDir, "metadata.json")

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata.json from archive: %w", err)
	}

	var m backup.BackupMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse metadata.json: %w", err)
	}

	return &m, nil
}

// ValidateDeploymentType checks that the archive's recorded deployment type
// matches currentType. Returns a descriptive error on mismatch naming both
// types so the operator knows exactly what diverged.
func ValidateDeploymentType(metadata *backup.BackupMetadata, currentType backup.DeploymentType) error {
	if metadata.DeploymentType == currentType {
		return nil
	}
	return fmt.Errorf(
		"deployment type mismatch: archive was created on a %q deployment but the current environment is %q; cross-deployment restore is not supported",
		metadata.DeploymentType,
		currentType,
	)
}
