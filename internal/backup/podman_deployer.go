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
	cfg *config.Config
	log logrus.FieldLogger
}

// NewPodmanDeployer creates a new Podman deployer
func NewPodmanDeployer(cfg *config.Config, log logrus.FieldLogger) *PodmanDeployer {
	return &PodmanDeployer{cfg: cfg, log: log}
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

// BackupPKI is a stub (implemented in EDM-3891)
func (p *PodmanDeployer) BackupPKI(ctx context.Context, outputDir string) error {
	p.log.Debug("BackupPKI called (stub implementation)")
	return nil
}

// BackupConfig is a stub (implemented in EDM-3892)
func (p *PodmanDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	p.log.Debug("BackupConfig called (stub implementation)")
	return nil
}
