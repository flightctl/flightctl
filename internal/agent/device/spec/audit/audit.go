package audit

import (
	"context"
	"time"
)

// Reason represents why a spec file was written to disk.
// All disk writes are audited - the reason field documents the purpose of each write.
type Reason string

const (
	// ReasonBootstrap represents initial device enrollment and first file creation
	ReasonBootstrap Reason = "bootstrap"
	// ReasonSync represents receiving a new spec from the management API
	ReasonSync Reason = "sync"
	// ReasonUpgrade represents applying a new desired spec
	ReasonUpgrade Reason = "upgrade"
	// ReasonRollback represents reverting to a previous working spec
	ReasonRollback Reason = "rollback"
	// ReasonOSRollback represents an OS-level rollback where the booted image does not match the current spec
	ReasonOSRollback Reason = "os_rollback"
	// ReasonRecovery represents recovery operations (recreating corrupted/deleted files)
	ReasonRecovery Reason = "recovery"
	// ReasonInitialization represents internal initialization operations (creating rollback snapshots)
	ReasonInitialization Reason = "initialization"
)

// Type represents the type of spec file operation.
type Type string

const (
	// TypeCurrent represents writing to current.json (effective spec)
	TypeCurrent Type = "current"
	// TypeDesired represents writing to desired.json (target spec from API)
	TypeDesired Type = "desired"
	// TypeRollback represents reverting to a previous version
	TypeRollback Type = "rollback"
)

// Result represents the result of an audit event.
type Result string

const (
	// ResultSuccess represents a successful operation
	ResultSuccess Result = "success"
	// ResultFailure represents a failed operation
	ResultFailure Result = "failure"
)

// Event represents a single audit log entry.
type Event struct {
	Ts                   string `json:"ts"`                     // RFC3339 UTC format - when the attempt started
	Device               string `json:"device"`                 // device name
	OldVersion           string `json:"old_version"`            // current effective version before the attempt
	NewVersion           string `json:"new_version"`            // target version
	Result               Result `json:"result"`                 // success | failure
	Reason               Reason `json:"reason"`                 // why the write happened (bootstrap/sync/upgrade/rollback/recovery/initialization)
	Type                 Type   `json:"type"`                   // current/desired/rollback (WHAT file operation)
	FleetTemplateVersion string `json:"fleet_template_version"` // from metadata.annotations["fleet-controller/templateVersion"]
	AgentVersion         string `json:"agent_version"`          // e.g., 0.10.0
}

// EventInfo contains all the information needed to log an audit event.
// Note: AgentVersion is not included here as it's provided by the FileLogger at construction time.
type EventInfo struct {
	Device               string
	OldVersion           string
	NewVersion           string
	Result               Result
	Reason               Reason // WHY: bootstrap/sync/upgrade/rollback/recovery/initialization
	Type                 Type   // WHAT: current/desired/rollback
	FleetTemplateVersion string
	StartTime            time.Time // When the operation started
}

// Logger defines the interface for audit logging operations.
// Following the pattern from status.Manager interface.
type Logger interface {
	// LogEvent logs a complete audit event with all required fields.
	LogEvent(ctx context.Context, info *EventInfo) error

	// Close closes the audit logger and flushes any pending writes.
	Close() error
}
