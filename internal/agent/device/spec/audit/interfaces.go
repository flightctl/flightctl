package audit

import (
	"context"
	"time"
)

// AuditReason represents why the state change is happening (lifecycle phase).
type AuditReason string

const (
	// AuditReasonBootstrap represents initial device enrollment
	AuditReasonBootstrap AuditReason = "bootstrap"
	// AuditReasonSync represents receiving a new spec from the management API
	AuditReasonSync AuditReason = "sync"
	// AuditReasonUpgrade represents applying a new desired spec
	AuditReasonUpgrade AuditReason = "upgrade"
	// AuditReasonRollback represents reverting to a previous working spec
	AuditReasonRollback AuditReason = "rollback"
)

// AuditType represents the type of spec file operation.
type AuditType string

const (
	// AuditTypeCurrent represents writing to current.json (effective spec)
	AuditTypeCurrent AuditType = "current"
	// AuditTypeDesired represents writing to desired.json (target spec from API)
	AuditTypeDesired AuditType = "desired"
	// AuditTypeRollback represents reverting to a previous version
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
	Reason               AuditReason `json:"reason"`                 // bootstrap/sync/upgrade/rollback (WHY)
	Type                 AuditType   `json:"type"`                   // current/desired/rollback (WHAT file operation)
	FleetTemplateVersion string      `json:"fleet_template_version"` // from metadata.annotations["fleet-controller/templateVersion"]
	AgentVersion         string      `json:"agent_version"`          // e.g., 0.10.0
}

// AuditEventInfo contains all the information needed to log an audit event.
// Note: AgentVersion is not included here as it's provided by the FileLogger at construction time.
type AuditEventInfo struct {
	Device               string
	OldVersion           string
	NewVersion           string
	Result               AuditResult
	Reason               AuditReason // WHY: bootstrap/sync/upgrade/rollback
	Type                 AuditType   // WHAT: current/desired/rollback
	FleetTemplateVersion string
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
