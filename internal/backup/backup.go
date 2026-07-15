package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/pkg/version"
	"github.com/sirupsen/logrus"
)

// BackupMetadata contains metadata about a backup archive.
// This struct is serialized to metadata.json in the archive root.
type BackupMetadata struct {
	// Timestamp is when the backup was created (UTC).
	Timestamp time.Time `json:"timestamp"`

	// Version is the FlightCtl version that created the backup.
	Version string `json:"version"`

	// DeploymentType is the deployment environment (podman or kubernetes).
	DeploymentType DeploymentType `json:"deploymentType"`

	// DatabaseIncluded indicates whether database backup is included.
	// False when external database is configured.
	DatabaseIncluded bool `json:"databaseIncluded"`
}

// generateArchiveFilename creates a timestamped archive filename.
// Format: flightctl-backup-YYYYMMDDTHHMMSSZ.tar.gz
// Example: flightctl-backup-20260521T143022Z.tar.gz
func generateArchiveFilename(timestamp time.Time) string {
	return fmt.Sprintf("flightctl-backup-%s.tar.gz", timestamp.Format("20060102T150405Z"))
}

// writeMetadataFile writes BackupMetadata to metadata.json in the staging directory.
func writeMetadataFile(stagingDir string, metadata BackupMetadata) error {
	// Marshal metadata to JSON with 2-space indentation
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Write to metadata.json with 0600 permissions (owner read/write only)
	metadataPath := filepath.Join(stagingDir, "metadata.json")
	if err := os.WriteFile(metadataPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write metadata file: %w", err)
	}

	return nil
}

// createTarGz creates a gzip-compressed tar archive from the staging directory.
// Archive file is created with 0600 permissions (owner read/write only).
func createTarGz(ctx context.Context, stagingDir string, archivePath string, log logrus.FieldLogger) (err error) {
	// Create output file with restrictive permissions
	outFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create archive file: %w", err)
	}
	defer func() {
		if closeErr := outFile.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close archive file: %w", closeErr)
		}
	}()

	// Create gzip writer
	gzWriter := gzip.NewWriter(outFile)
	defer func() {
		if closeErr := gzWriter.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close gzip writer: %w", closeErr)
		}
	}()

	// Create tar writer
	tarWriter := tar.NewWriter(gzWriter)
	defer func() {
		if closeErr := tarWriter.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close tar writer: %w", closeErr)
		}
	}()

	// Walk staging directory and add files to archive
	err = filepath.Walk(stagingDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Check for context cancellation
		select {
		case <-ctx.Done():
			return fmt.Errorf("archive creation cancelled: %w", ctx.Err())
		default:
		}

		// Create tar header from file info
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header for %s: %w", path, err)
		}

		// Set name to relative path (remove staging directory prefix)
		relPath, err := filepath.Rel(stagingDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}

		// Normalize path separators for portability (use forward slashes in tar)
		header.Name = filepath.ToSlash(relPath)

		// Skip the root directory itself (empty name)
		if header.Name == "." {
			return nil
		}

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header for %s: %w", path, err)
		}

		// Write file content for regular files
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("failed to open file %s: %w", path, err)
			}

			if _, err := io.Copy(tarWriter, file); err != nil {
				file.Close()
				return fmt.Errorf("failed to write file content for %s: %w", path, err)
			}
			file.Close()
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create tar archive: %w", err)
	}

	log.Debugf("Created tar.gz archive: %s", archivePath)
	return nil
}

// generateChecksum computes SHA256 hash of the archive and writes checksum file.
// Checksum format: <hash>  <filename> (two spaces, compatible with sha256sum -c)
func generateChecksum(archivePath string, checksumPath string) error {
	// Open archive file for reading
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive for checksum: %w", err)
	}
	defer file.Close()

	// Create SHA256 hasher and stream file through it
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to compute checksum: %w", err)
	}

	// Get hash as hex string
	hashBytes := hasher.Sum(nil)
	hashStr := hex.EncodeToString(hashBytes)

	// Format checksum file content: <hash>  <filename> (two spaces per sha256sum convention)
	filename := filepath.Base(archivePath)
	checksumContent := fmt.Sprintf("%s  %s\n", hashStr, filename)

	// Write checksum file with 0644 permissions (readable by all - no secrets, just hash)
	if err := os.WriteFile(checksumPath, []byte(checksumContent), 0644); err != nil { //nolint:gosec
		return fmt.Errorf("failed to write checksum file: %w", err)
	}

	return nil
}

// CreateArchive packages the staging directory into a timestamped tar.gz archive
// with SHA256 checksum. Writes metadata.json to staging directory before archiving.
//
// Returns:
//   - archivePath: path to created .tar.gz file
//   - checksumPath: path to created .sha256 file
//   - error: nil on success, error describing failure otherwise
//
// Archive filename format: flightctl-backup-YYYYMMDDTHHMMSSZ.tar.gz
// Checksum format: <sha256-hash>  <archive-filename> (compatible with sha256sum -c)
//
// Error handling: All-or-nothing semantics. On failure, cleans up partial artifacts
// (archive file, checksum file). Staging directory cleanup is caller's responsibility.
func CreateArchive(ctx context.Context, stagingDir string, outputDir string, metadata BackupMetadata, log logrus.FieldLogger) (archivePath string, checksumPath string, err error) {
	log.Infof("Starting archive creation from staging directory: %s", stagingDir)

	// Validate staging directory exists
	if _, err := os.Stat(stagingDir); err != nil {
		return "", "", fmt.Errorf("staging directory not accessible: %w", err)
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate archive filename from metadata timestamp
	filename := generateArchiveFilename(metadata.Timestamp)
	archivePath = filepath.Join(outputDir, filename)
	checksumPath = archivePath + ".sha256"

	// All-or-nothing cleanup: remove archive and checksum on error
	success := false
	defer func() {
		if !success {
			// Clean up partial artifacts
			if archivePath != "" {
				os.Remove(archivePath)
			}
			if checksumPath != "" {
				os.Remove(checksumPath)
			}
		}
	}()

	// Write metadata.json to staging directory
	if err := writeMetadataFile(stagingDir, metadata); err != nil {
		return "", "", fmt.Errorf("failed to write metadata file: %w", err)
	}

	log.Debugf("Metadata written to staging directory")

	// Create tar.gz archive
	if err := createTarGz(ctx, stagingDir, archivePath, log); err != nil {
		return "", "", fmt.Errorf("failed to create archive: %w", err)
	}

	log.Infof("Archive created: %s", archivePath)

	// Generate SHA256 checksum
	if err := generateChecksum(archivePath, checksumPath); err != nil {
		return "", "", fmt.Errorf("failed to generate checksum: %w", err)
	}

	log.Infof("Checksum generated: %s", checksumPath)

	success = true
	return archivePath, checksumPath, nil
}

// PerformBackup executes the complete backup workflow:
//  1. Detect deployment type
//  2. Create staging directory
//  3. Backup database (if internal)
//  4. Backup PKI materials
//  5. Backup encryption keys (if present)
//  6. Backup config (stub)
//  7. Create timestamped archive with checksum
//  8. Clean up staging directory
//
// Returns path to created archive on success.
func PerformBackup(ctx context.Context, deployer Deployer, outputDir string, log logrus.FieldLogger) (archivePath string, err error) {
	log.Infof("Starting backup workflow (deployment type: %s)", deployer.Type())

	// Create staging directory for temporary backup files
	stagingDir, err := os.MkdirTemp(outputDir, "flightctl-backup-staging-*")
	if err != nil {
		return "", fmt.Errorf("failed to create staging directory: %w", err)
	}

	// Ensure staging directory is cleaned up after successful archive creation
	stagingCleaned := false
	defer func() {
		if !stagingCleaned {
			if err := os.RemoveAll(stagingDir); err != nil {
				log.Warnf("Failed to clean up staging directory %s: %v", stagingDir, err)
			}
		}
	}()

	log.Debugf("Created staging directory: %s", stagingDir)

	// Backup database
	databaseIncluded := true
	if err := deployer.BackupDatabase(ctx, stagingDir); err != nil {
		// Handle external database case (not an error)
		if errors.Is(err, ErrExternalDatabase) {
			log.Infof("External database detected - database backup skipped")
			databaseIncluded = false
		} else {
			return "", fmt.Errorf("database backup failed: %w", err)
		}
	} else {
		log.Infof("Database backup completed")
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("backup cancelled: %w", ctx.Err())
	default:
	}

	// Backup PKI materials
	if err := deployer.BackupPKI(ctx, stagingDir); err != nil {
		return "", fmt.Errorf("PKI backup failed: %w", err)
	}
	log.Infof("PKI backup completed")

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("backup cancelled: %w", ctx.Err())
	default:
	}

	// Backup encryption keys (optional — may not exist in pre-encryption deployments)
	if err := deployer.BackupEncryptionKeys(ctx, stagingDir); err != nil {
		return "", fmt.Errorf("encryption keys backup failed: %w", err)
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("backup cancelled: %w", ctx.Err())
	default:
	}

	// Backup config (stub implementation in EDM-3891)
	if err := deployer.BackupConfig(ctx, stagingDir); err != nil {
		return "", fmt.Errorf("config backup failed: %w", err)
	}
	log.Debugf("Config backup completed")

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("backup cancelled: %w", ctx.Err())
	default:
	}

	// Build backup metadata
	metadata := BackupMetadata{
		Timestamp:        time.Now().UTC(),
		Version:          version.Get().String(),
		DeploymentType:   deployer.Type(),
		DatabaseIncluded: databaseIncluded,
	}

	// Create archive with checksum
	archivePath, checksumPath, err := CreateArchive(ctx, stagingDir, outputDir, metadata, log)
	if err != nil {
		return "", fmt.Errorf("archive creation failed: %w", err)
	}

	// Clean up staging directory (contains sensitive data - must succeed)
	if err := os.RemoveAll(stagingDir); err != nil {
		// Cleanup failed - attempt rollback by removing archive and checksum
		var rollbackErrs []error
		if errArchive := os.Remove(archivePath); errArchive != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to remove archive during rollback: %w", errArchive))
		}
		if errChecksum := os.Remove(checksumPath); errChecksum != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to remove checksum during rollback: %w", errChecksum))
		}

		// Return comprehensive error including staging cleanup failure and any rollback failures
		errMsg := fmt.Sprintf("backup failed: unable to clean up staging directory %s (contains sensitive data - MANUAL CLEANUP REQUIRED): %v", stagingDir, err)
		if len(rollbackErrs) > 0 {
			errMsg += "; rollback also failed: "
			for i, rollbackErr := range rollbackErrs {
				if i > 0 {
					errMsg += ", "
				}
				errMsg += rollbackErr.Error()
			}
		}
		return "", fmt.Errorf("%s", errMsg)
	}
	stagingCleaned = true
	log.Debugf("Staging directory cleaned up: %s", stagingDir)

	// Log success only after cleanup succeeds
	log.Infof("Backup completed successfully")
	log.Infof("Archive: %s", archivePath)
	log.Infof("Checksum: %s", checksumPath)

	return archivePath, nil
}
