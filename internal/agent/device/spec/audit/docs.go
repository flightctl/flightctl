// Package audit provides structured audit logging for device spec transitions.
//
// The audit logger records all mutations to device spec files (current.json,
// desired.json, rollback.json) in JSON lines format at /var/log/flightctl/audit.log.
// Events capture version transitions with action-based types (bootstrap, sync,
// upgrade, rollback) to provide a comprehensive audit trail for troubleshooting
// and compliance.
//
// Log rotation is handled automatically using lumberjack with hardcoded defaults
// (2MB per file, 3 backups, 8MB total capacity).
package audit

//go:generate go run -modfile=../../../../../tools/go.mod go.uber.org/mock/mockgen -source=interfaces.go -destination=mock_audit.go -package=audit
