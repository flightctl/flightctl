package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// PodmanDeployer implements Deployer for Podman/quadlet deployments
type PodmanDeployer struct {
	log               logrus.FieldLogger
	pkiPath           string // Optional: if empty, defaults to "/etc/flightctl/pki"
	encryptionPath    string // Optional: if empty, defaults to "/etc/flightctl/encryption"
	serviceConfigPath string // Optional: if empty, defaults to "/etc/flightctl/service-config.yaml"
	dbContainerName   string
	dbName            string
	dbUser            string
	dbPassword        string
	containerCLI      string
	kvContainerName   string
}

// PodmanDeployerOption configures a PodmanDeployer.
type PodmanDeployerOption func(*PodmanDeployer)

// WithPKIPath sets the PKI source directory path.
func WithPKIPath(path string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.pkiPath = path
	}
}

// WithEncryptionPath sets the encryption key source directory path.
func WithEncryptionPath(path string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.encryptionPath = path
	}
}

// WithServiceConfigPath sets the service configuration file path.
func WithServiceConfigPath(path string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.serviceConfigPath = path
	}
}

// WithDBContainerName sets the database container name for pg_dump exec.
func WithDBContainerName(name string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.dbContainerName = name
	}
}

// WithDBName overrides the database name used for pg_dump.
// When empty, cfg.Database.Name from the service config is used.
func WithDBName(name string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.dbName = name
	}
}

// WithContainerCLI sets the container CLI command (e.g. "podman" or "docker").
func WithContainerCLI(cli string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.containerCLI = cli
	}
}

// WithDBUser overrides the database user for pg_dump.
// When empty, the container's $POSTGRESQL_USER env var is used.
func WithDBUser(user string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.dbUser = user
	}
}

// WithDBPassword overrides the database password for pg_dump.
// When empty, the container's $PGPASSWORD env var is used.
func WithDBPassword(password string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.dbPassword = password
	}
}

// WithKVContainerName sets the KV container name (reserved for future use).
func WithKVContainerName(name string) PodmanDeployerOption {
	return func(d *PodmanDeployer) {
		d.kvContainerName = name
	}
}

// NewPodmanDeployer creates a new Podman deployer.
// Defaults: pkiPath "/etc/flightctl/pki", serviceConfigPath "/etc/flightctl/service-config.yaml",
// dbContainerName "flightctl-db", containerCLI "podman", kvContainerName "flightctl-kv".
func NewPodmanDeployer(log logrus.FieldLogger, opts ...PodmanDeployerOption) *PodmanDeployer {
	d := &PodmanDeployer{
		log: log,
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.pkiPath == "" {
		d.pkiPath = "/etc/flightctl/pki"
	}
	if d.encryptionPath == "" {
		d.encryptionPath = "/etc/flightctl/encryption"
	}
	if d.serviceConfigPath == "" {
		d.serviceConfigPath = "/etc/flightctl/service-config.yaml"
	}
	if d.dbContainerName == "" {
		d.dbContainerName = "flightctl-db"
	}
	if d.containerCLI == "" {
		d.containerCLI = "podman"
	}
	if d.kvContainerName == "" {
		d.kvContainerName = "flightctl-kv"
	}
	if d.dbUser == "" {
		d.dbUser = "flightctl_app"
	}
	return d
}

// Type returns the deployment type
func (p *PodmanDeployer) Type() DeploymentType {
	return DeploymentTypePodman
}

// BackupDatabase backs up the PostgreSQL database by executing pg_dump inside the database container.
// Credentials are loaded from the service configuration file at serviceConfigPath.
// For internal databases, executes pg_dump via podman exec and writes dump to <outputDir>/db/dump.sql.
// For external databases, returns ErrExternalDatabase without creating a backup.
func (p *PodmanDeployer) BackupDatabase(ctx context.Context, outputDir string) error {
	rawCfg, err := os.ReadFile(p.serviceConfigPath)
	if err != nil {
		return fmt.Errorf("failed to read service configuration from %s: %w", p.serviceConfigPath, err)
	}

	dbCfg := parseServiceConfigDB(rawCfg, p.log)
	if dbCfg.Type == "external" {
		return ErrExternalDatabase
	}

	// Create db directory
	dbDir := filepath.Join(outputDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	p.log.Infof("Starting database backup from container %s...", p.dbContainerName)

	dbName := dbCfg.Name
	if p.dbName != "" {
		dbName = p.dbName
	}

	// Create dump file to stream output directly (avoids holding entire dump in memory)
	dumpFile := filepath.Join(dbDir, "dump.sql")
	outFile, err := os.OpenFile(dumpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create dump file: %w", err)
	}
	defer outFile.Close()

	// Execute pg_dump inside the container with password from stdin and safely escaped parameters.
	// Output streams directly to dump file.
	pgDumpCmd := fmt.Sprintf("PGPASSWORD=$(cat -) pg_dump --clean --if-exists -h 127.0.0.1 -p 5432 -U %s -d %s",
		ShellEscape(p.dbUser),
		ShellEscape(dbName))

	cmd := exec.CommandContext(ctx, p.containerCLI, "exec", "-i", p.dbContainerName, "sh", "-c", pgDumpCmd)

	// Pass password via stdin to avoid exposing it in process argv
	cmd.Stdin = bytes.NewReader([]byte(p.dbPassword))

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
			return fmt.Errorf("symlinks not supported in backup directory: %s", relPath)
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
	if err := os.MkdirAll(pkiDstDir, sensitiveDataDirMode); err != nil {
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

// BackupEncryptionKeys backs up the data-at-rest encryption key directory.
// Copies <encryptionPath>/ to <outputDir>/encryption/, preserving file permissions.
// Never returns an error for a missing encryption directory — a warning is logged
// so the operator knows encrypted fields will be unrecoverable from this backup.
func (p *PodmanDeployer) BackupEncryptionKeys(ctx context.Context, outputDir string) (retErr error) {
	encSrcDir := p.encryptionPath

	if _, err := os.Stat(encSrcDir); os.IsNotExist(err) {
		p.log.Warnf("Encryption key directory not found at %s — skipping. If this deployment uses data-at-rest encryption, encrypted database fields will be unrecoverable from this backup.", encSrcDir)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to access encryption key directory %s: %w", encSrcDir, err)
	}

	p.log.Infof("Starting encryption key backup from %s...", encSrcDir)

	encDstDir := filepath.Join(outputDir, "encryption")
	if err := os.MkdirAll(encDstDir, sensitiveDataDirMode); err != nil {
		return fmt.Errorf("failed to create encryption output directory: %w", err)
	}

	// Clean up encryption directory on error to avoid leaving partial sensitive data on disk.
	success := false
	defer func() {
		if !success {
			if cleanupErr := os.RemoveAll(encDstDir); cleanupErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("failed to clean up partial encryption backup at %s: %w", encDstDir, cleanupErr))
			}
		}
	}()

	fileCount, err := copyDirPreservePerms(encSrcDir, encDstDir, ctx, p.log)
	if err != nil {
		return fmt.Errorf("failed to copy encryption directory: %w", err)
	}

	p.log.Infof("Encryption key backup completed. Backed up %d files from %s", fileCount, encSrcDir)
	success = true
	return nil
}

// BackupConfig backs up service configuration files for Podman deployments.
// Copies /etc/flightctl/service-config.yaml to <outputDir>/config/service-config.yaml.
// Exports PAM Issuer volume to <outputDir>/volumes/pam-issuer-etc.tar.
// Returns error if service-config.yaml is missing (required file).
// Logs warning if PAM Issuer volume export fails (optional component).
func (p *PodmanDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	// Create config directory
	configDir := filepath.Join(outputDir, "config")
	if err := os.MkdirAll(configDir, sensitiveDataDirMode); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Copy service-config.yaml (required file)
	serviceConfigSrc := p.serviceConfigPath
	serviceConfigDst := filepath.Join(configDir, "service-config.yaml")

	// Check if source file exists
	srcInfo, err := os.Stat(serviceConfigSrc)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("required service configuration file does not exist: %s", serviceConfigSrc)
		}
		return fmt.Errorf("failed to access service configuration file %s: %w", serviceConfigSrc, err)
	}

	// Copy file preserving permissions
	if err := copyFilePreserveMode(serviceConfigSrc, serviceConfigDst, srcInfo.Mode().Perm(), p.log); err != nil {
		return fmt.Errorf("failed to copy service configuration: %w", err)
	}

	p.log.Info("Service configuration backed up")

	// Backup PAM Issuer volume (optional component)
	volumesDir := filepath.Join(outputDir, "volumes")
	if err := os.MkdirAll(volumesDir, sensitiveDataDirMode); err != nil {
		return fmt.Errorf("failed to create volumes directory: %w", err)
	}

	// Check for context cancellation before volume export
	select {
	case <-ctx.Done():
		return fmt.Errorf("config backup cancelled: %w", ctx.Err())
	default:
	}

	// PAM Issuer volume name (must match Podman/quadlet deployment)
	// Defined in: packaging/systemd/flightctl-pam-issuer.quadlet
	volumeName := "flightctl-pam-issuer-etc"
	volumeArchive := filepath.Join(volumesDir, "pam-issuer-etc.tar")

	// Execute podman volume export
	cmd := exec.CommandContext(ctx, "podman", "volume", "export", volumeName, "-o", volumeArchive)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Clean up any partial archive before returning
		if removeErr := os.Remove(volumeArchive); removeErr != nil && !os.IsNotExist(removeErr) {
			p.log.Warnf("Failed to remove partial PAM Issuer archive %s: %v - manual cleanup may be required", volumeArchive, removeErr)
		}
		// Check if failure was due to context cancellation
		if ctx.Err() != nil {
			return fmt.Errorf("config backup cancelled during PAM volume export: %w", ctx.Err())
		}
		// Log warning but don't fail - PAM Issuer volume is optional
		p.log.Warnf("PAM Issuer volume export failed (volume may not exist or podman unavailable): %v (output: %s)", err, string(output))
		p.log.Warn("Continuing backup without PAM Issuer volume")
		return nil
	}

	p.log.Info("PAM Issuer volume backed up")
	return nil
}
