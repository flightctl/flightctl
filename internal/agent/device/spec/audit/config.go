package audit

import (
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
	// Enabled enables or disables audit logging.
	// Using *bool to distinguish between unset (nil) and explicitly set (true/false).
	// This allows config overrides to explicitly disable audit logging with "enabled: false".
	Enabled *bool `json:"enabled,omitempty"`
}

// NewDefaultAuditConfig creates a new audit config with default values.
// Following the pattern from config.NewDefault().
func NewDefaultAuditConfig() *AuditConfig {
	enabled := DefaultEnabled
	return &AuditConfig{
		Enabled: &enabled,
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
	if c.Enabled == nil || !*c.Enabled {
		return nil
	}

	// Pure validation with no side effects.
	// Unlike required paths (DataDir, ConfigDir), audit logs are optional - failures
	// writing audit logs should not prevent agent startup.
	// Directory creation is deferred to first write:
	// - Production: lumberjack creates parent directories automatically
	// - Tests: test setup creates temporary directories
	// This allows the device simulator to start without /var/log write permissions.

	// Rotation settings are hardcoded and don't need validation
	return nil
}
