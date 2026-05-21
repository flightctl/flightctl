package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

// PodmanDeployer implements Deployer for Podman/quadlet deployments
type PodmanDeployer struct {
	cfg     *config.Config
	log     logrus.FieldLogger
	pkiPath string // Optional: if empty, defaults to "/etc/flightctl/pki"
}

// NewPodmanDeployer creates a new Podman deployer.
// If pkiPath is empty, defaults to "/etc/flightctl/pki" (production path).
// Pass explicit pkiPath for testing with temporary directories.
func NewPodmanDeployer(cfg *config.Config, log logrus.FieldLogger, pkiPath string) *PodmanDeployer {
	// Set default PKI path at construction time
	if pkiPath == "" {
		pkiPath = "/etc/flightctl/pki"
	}
	return &PodmanDeployer{
		cfg:     cfg,
		log:     log,
		pkiPath: pkiPath,
	}
}

// Type returns the deployment type
func (p *PodmanDeployer) Type() DeploymentType {
	return DeploymentTypePodman
}

// BackupDatabase backs up the PostgreSQL database by executing pg_dump inside the database container.
// For internal databases, executes pg_dump via podman exec and writes dump to <outputDir>/db/dump.sql.
// For external databases, returns ErrExternalDatabase without creating a backup.
func (p *PodmanDeployer) BackupDatabase(ctx context.Context, outputDir string) error {
	// Check if database is external
	if !isInternalDB(p.cfg) {
		return ErrExternalDatabase
	}

	// Create db directory
	dbDir := filepath.Join(outputDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	// Hardcoded container name for database.
	// This matches the default Podman/quadlet deployment.
	containerName := "flightctl-db"

	p.log.Infof("Starting database backup from container %s...", containerName)

	// Build password from config
	password := string(p.cfg.Database.Password)

	// Create dump file to stream output directly (avoids holding entire dump in memory)
	dumpFile := filepath.Join(dbDir, "dump.sql")
	outFile, err := os.OpenFile(dumpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create dump file: %w", err)
	}
	defer outFile.Close()

	// Execute pg_dump inside the container with password from stdin and safely escaped parameters.
	// Output streams directly to dump file.
	// Use shell escaping to prevent injection attacks from user/database names.
	pgDumpCmd := fmt.Sprintf("PGPASSWORD=$(cat -) pg_dump -h 127.0.0.1 -p %s -U %s -d %s",
		shellEscape(strconv.Itoa(int(p.cfg.Database.Port))),
		shellEscape(p.cfg.Database.User),
		shellEscape(p.cfg.Database.Name))

	cmd := exec.CommandContext(ctx, "podman", "exec", "-i", containerName, "sh", "-c", pgDumpCmd)

	// Pass password via stdin to avoid exposing it in process argv
	cmd.Stdin = bytes.NewReader([]byte(password))

	// Stream stdout (SQL dump) directly to file and capture stderr
	cmd.Stdout = outFile
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Execute command
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump in container failed: %w (stderr: %s)", err, stderr.String())
	}

	// Get file size for logging
	fileInfo, err := outFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat dump file: %w", err)
	}

	p.log.Infof("Database backup completed. Dump file size: %d bytes", fileInfo.Size())

	return nil
}

// copyDirPreservePerms recursively copies a directory tree, preserving file permissions.
// It walks the source directory and recreates the structure in the destination,
// copying file contents and preserving the original file mode (permissions).
// Respects context cancellation during the directory walk.
func copyDirPreservePerms(src, dst string, ctx context.Context, log logrus.FieldLogger) (int, error) {
	fileCount := 0

	err := filepath.Walk(src, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walking source: %w", err)
		}

		// Check for context cancellation
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Compute destination path
		relPath, err := filepath.Rel(src, srcPath)
		if err != nil {
			return fmt.Errorf("computing relative path: %w", err)
		}
		dstPath := filepath.Join(dst, relPath)

		// Reject symlinks to prevent path traversal attacks and undefined behavior
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks not supported in PKI directory: %s", relPath)
		}

		// Handle directories
		if info.IsDir() {
			if err := os.MkdirAll(dstPath, info.Mode()); err != nil {
				return fmt.Errorf("creating directory %s: %w", dstPath, err)
			}
			log.Debugf("Created directory: %s (mode: %04o)", relPath, info.Mode())
			return nil
		}

		// Copy file preserving permissions
		if err := copyFilePreserveMode(srcPath, dstPath, info.Mode(), log); err != nil {
			return fmt.Errorf("copying file %s: %w", relPath, err)
		}

		fileCount++
		log.Debugf("Copied file: %s (mode: %04o)", relPath, info.Mode())
		return nil
	})

	return fileCount, err
}

// copyFilePreserveMode copies a single file preserving its mode (permissions)
func copyFilePreserveMode(src, dst string, mode os.FileMode, log logrus.FieldLogger) error {
	// Read source file
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading source file %s: %w", src, err)
	}

	// Write to destination with preserved mode
	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("writing destination file %s: %w", dst, err)
	}

	return nil
}

// BackupPKI backs up PKI materials by copying /etc/flightctl/pki/ directory tree.
// Preserves file permissions (typically 0600 for private keys, 0644 for certificates).
// Writes output to <outputDir>/pki/.
// The operation respects context cancellation during the directory walk.
func (p *PodmanDeployer) BackupPKI(ctx context.Context, outputDir string) error {
	// PKI path is set at construction time (defaults to /etc/flightctl/pki)
	pkiSrcDir := p.pkiPath
	pkiDstDir := filepath.Join(outputDir, "pki")

	// Verify source exists
	if _, err := os.Stat(pkiSrcDir); os.IsNotExist(err) {
		return fmt.Errorf("PKI directory not found: %s", pkiSrcDir)
	} else if err != nil {
		return fmt.Errorf("failed to stat PKI directory: %w", err)
	}

	p.log.Infof("Starting PKI backup from %s...", pkiSrcDir)

	// Create destination directory
	if err := os.MkdirAll(pkiDstDir, pkiDirMode); err != nil {
		return fmt.Errorf("failed to create PKI output directory: %w", err)
	}

	// Clean up PKI directory on error (ensures all-or-nothing semantics)
	success := false
	defer func() {
		if !success {
			os.RemoveAll(pkiDstDir)
		}
	}()

	// Recursive copy with permission preservation
	fileCount, err := copyDirPreservePerms(pkiSrcDir, pkiDstDir, ctx, p.log)
	if err != nil {
		return fmt.Errorf("failed to copy PKI directory: %w", err)
	}

	p.log.Infof("PKI backup completed. Backed up %d files from %s", fileCount, pkiSrcDir)

	success = true
	return nil
}

// BackupConfig is a stub (implemented in EDM-3892)
func (p *PodmanDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	p.log.Debug("BackupConfig called (stub implementation)")
	return nil
}
