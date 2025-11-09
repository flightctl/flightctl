package audit

import (
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

const (
	// DefaultLogPath is the hardcoded path for audit logs - not configurable by users
	DefaultLogPath = "/var/log/flightctl/audit.log"
	// DefaultEnabled is the default enabled state for audit logging
	DefaultEnabled = true

	// Hardcoded rotation defaults - non-configurable by users
	// 2 MB per file, 3 backups = 8 MB total (≈27,960 records)
	DefaultMaxSizeKB  = 2048 // 2MB per file (approximately 6,990 records)
	DefaultMaxBackups = 3    // Keep 3 backup files (1 active + 3 backups = 8 MB total)
	DefaultMaxAge     = 0    // No time-based pruning
)

// AuditConfig holds audit logging configuration.
// Flat enable/disable configuration only - rotation settings are hardcoded.
type AuditConfig struct {
	// Enabled enables or disables audit logging
	Enabled bool `json:"enabled,omitempty"`
}

// NewDefaultAuditConfig creates a new audit config with default values.
// Following the pattern from config.NewDefault().
func NewDefaultAuditConfig() *AuditConfig {
	return &AuditConfig{
		Enabled: DefaultEnabled,
	}
}

// Complete fills in defaults for fields not set by the config.
// Following the pattern from agent config.Complete().
func (c *AuditConfig) Complete() error {
	// No configurable fields to complete - rotation settings are hardcoded
	return nil
}

// Validate checks that the configuration is valid.
// Following the pattern from agent config.Validate().
func (c *AuditConfig) Validate(readWriter fileio.ReadWriter) error {
	if !c.Enabled {
		return nil
	}

	// Validate that the hardcoded log directory exists or can be created
	logDir := filepath.Dir(DefaultLogPath)
	exists, err := readWriter.PathExists(logDir)
	if err != nil {
		return fmt.Errorf("checking audit log directory %q: %w", logDir, err)
	}
	if !exists {
		if err := readWriter.MkdirAll(logDir, fileio.DefaultDirectoryPermissions); err != nil {
			return fmt.Errorf("creating audit log directory %q: %w", logDir, err)
		}
	}

	// Rotation settings are hardcoded and don't need validation
	return nil
}
