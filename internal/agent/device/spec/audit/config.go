package audit

import (
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

const (
	// DefaultLogPath is the hardcoded path for audit logs - not configurable by users
	DefaultLogPath = "/var/log/flightctl/audit.log"
	// DefaultMaxSize is the default maximum size in megabytes of the audit log file before it gets rotated
	DefaultMaxSize = 100 // 100 MB
	// DefaultMaxBackups is the default maximum number of old audit log files to retain
	DefaultMaxBackups = 3
	// DefaultMaxAge is the default maximum number of days to retain old audit log files
	DefaultMaxAge = 28 // 28 days
	// DefaultEnabled is the default enabled state for audit logging
	DefaultEnabled = true
)

// AuditConfig holds audit logging configuration.
// Path is hardcoded, but rotation settings are configurable.
type AuditConfig struct {
	// Enabled enables or disables audit logging
	Enabled bool `json:"enabled,omitempty"`
	// MaxSize is the maximum size in megabytes of the audit log file before it gets rotated
	MaxSize int `json:"max-size,omitempty"`
	// MaxBackups is the maximum number of old audit log files to retain
	MaxBackups int `json:"max-backups,omitempty"`
	// MaxAge is the maximum number of days to retain old audit log files
	MaxAge int `json:"max-age,omitempty"`
}

// NewDefaultAuditConfig creates a new audit config with default values.
// Following the pattern from config.NewDefault().
func NewDefaultAuditConfig() *AuditConfig {
	return &AuditConfig{
		Enabled:    DefaultEnabled,
		MaxSize:    DefaultMaxSize,
		MaxBackups: DefaultMaxBackups,
		MaxAge:     DefaultMaxAge,
	}
}

// Complete fills in defaults for fields not set by the config.
// Following the pattern from agent config.Complete().
func (c *AuditConfig) Complete() error {
	if c.MaxSize <= 0 {
		c.MaxSize = DefaultMaxSize
	}
	if c.MaxBackups < 0 {
		c.MaxBackups = DefaultMaxBackups
	}
	if c.MaxAge <= 0 {
		c.MaxAge = DefaultMaxAge
	}
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

	if c.MaxSize <= 0 {
		return fmt.Errorf("audit log max size must be positive, got %d", c.MaxSize)
	}

	if c.MaxBackups < 0 {
		return fmt.Errorf("audit log max backups must be non-negative, got %d", c.MaxBackups)
	}

	if c.MaxAge <= 0 {
		return fmt.Errorf("audit log max age must be positive, got %d", c.MaxAge)
	}

	return nil
}
