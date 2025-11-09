package audit

import (
	"context"
	"time"
)

// AuditType represents the type of audit event (action-based).
type AuditType string

const (
	// AuditTypeBootstrap represents initial device enrollment
	AuditTypeBootstrap AuditType = "bootstrap"
	// AuditTypeSync represents receiving a new spec from the management API (desired.json write)
	AuditTypeSync AuditType = "sync"
	// AuditTypeUpgrade represents applying a new desired spec (current.json write)
	AuditTypeUpgrade AuditType = "upgrade"
	// AuditTypeRollback represents reverting to a previous working spec
	AuditTypeRollback AuditType = "rollback"
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
	Ts                   string      `json:"ts"`                     // RFC3339 UTC format - when the attempt started
	Device               string      `json:"device"`                 // device name
	OldVersion           string      `json:"old_version"`            // current effective version before the attempt
	NewVersion           string      `json:"new_version"`            // target version
	Result               AuditResult `json:"result"`                 // success | failure
	Type                 AuditType   `json:"type"`                   // bootstrap/sync/upgrade/rollback
	FleetTemplateVersion string      `json:"fleet_template_version"` // from metadata.annotations["fleet-controller/templateVersion"]
	AgentVersion         string      `json:"agent_version"`          // e.g., 0.10.0
}

// AuditEventInfo contains all the information needed to log an audit event.
type AuditEventInfo struct {
	Device               string
	OldVersion           string
	NewVersion           string
	Result               AuditResult
	Type                 AuditType
	FleetTemplateVersion string
	AgentVersion         string
	StartTime            time.Time // When the operation started
}

// Logger defines the interface for audit logging operations.
// Following the pattern from status.Manager interface.
type Logger interface {
	// LogEvent logs a complete audit event with all required fields.
	LogEvent(ctx context.Context, info *AuditEventInfo) error

	// Close closes the audit logger and flushes any pending writes.
	Close() error
}
