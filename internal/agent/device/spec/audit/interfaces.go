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
)

// AuditEvent represents a single audit log entry.
type AuditEvent struct {
	Timestamp    time.Time   `json:"timestamp"`
	DeviceID     string      `json:"device_id"`
	OldVersion   string      `json:"old_version"`
	NewVersion   string      `json:"new_version"`
	AuditType    AuditType   `json:"audit_type"`
	AuditResult  AuditResult `json:"audit_result"`
	ErrorMessage string      `json:"error_message,omitempty"`
}

// Logger defines the interface for audit logging operations.
// Following the pattern from status.Manager interface.
type Logger interface {
	// LogApply logs a successful spec application.
	LogApply(ctx context.Context, oldVersion, newVersion string) error

	// LogRollback logs a rollback operation.
	LogRollback(ctx context.Context, oldVersion, newVersion string) error

	// LogFailure logs a failed spec application.
	LogFailure(ctx context.Context, oldVersion, newVersion string, err error) error

	// Close closes the audit logger and flushes any pending writes.
	Close() error
}

