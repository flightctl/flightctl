package audit

import (
	"context"
	"time"
)

// AuditType represents the type of audit event.
type AuditType string

const (
	// AuditTypeApply represents a successful spec application
	AuditTypeApply AuditType = "apply"
	// AuditTypeRollback represents a rollback operation
	AuditTypeRollback AuditType = "rollback"
	// AuditTypeFailure represents a failed spec application
	AuditTypeFailure AuditType = "failure"
)

// AuditResult represents the result of an audit event.
type AuditResult string

const (
	// AuditResultSuccess represents a successful operation
	AuditResultSuccess AuditResult = "success"
	// AuditResultFailure represents a failed operation
	AuditResultFailure AuditResult = "failure"
	// AuditResultNoop represents no change was needed
	AuditResultNoop AuditResult = "noop"
)

// AuditEvent represents a single audit log entry.
type AuditEvent struct {
	Ts         string      `json:"ts"`          // RFC3339 UTC format - when the attempt started
	Device     string      `json:"device"`      // device name
	OldVersion string      `json:"old_version"` // current effective version before the attempt
	NewVersion string      `json:"new_version"` // target version
	OldHash    string      `json:"old_hash"`    // SHA256 of the current rendered spec
	NewHash    string      `json:"new_hash"`    // target rendered spec hash
	Result     AuditResult `json:"result"`      // success | failure | noop
	DurationMs int64       `json:"duration_ms"` // total apply time in milliseconds
	Type       AuditType   `json:"type"`        // apply/rollback/failure
}

// AuditEventInfo contains all the information needed to log an audit event.
type AuditEventInfo struct {
	Device     string
	OldVersion string
	NewVersion string
	OldHash    string
	NewHash    string
	Result     AuditResult
	DurationMs int64
	Type       AuditType
	StartTime  time.Time // When the operation started
}

// Logger defines the interface for audit logging operations.
// Following the pattern from status.Manager interface.
type Logger interface {
	// LogEvent logs a complete audit event with all required fields.
	LogEvent(ctx context.Context, info *AuditEventInfo) error

	// Close closes the audit logger and flushes any pending writes.
	Close() error
}
