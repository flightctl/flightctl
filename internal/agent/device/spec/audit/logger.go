package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

var _ Logger = (*FileLogger)(nil)

// FileLogger implements the Logger interface using file-based logging with rotation.
// Following the pattern from config.Controller struct.
type FileLogger struct {
	config       *AuditConfig
	readWriter   fileio.ReadWriter
	deviceID     string
	agentVersion string
	log          *log.PrefixLogger
	rotatingLog  *lumberjack.Logger
}

// NewFileLogger creates a new file-based audit logger.
// Following the dependency injection pattern from status.NewManager and config.NewController.
func NewFileLogger(
	config *AuditConfig,
	readWriter fileio.ReadWriter,
	deviceID string,
	agentVersion string,
	log *log.PrefixLogger,
) (*FileLogger, error) {
	if config == nil {
		return nil, fmt.Errorf("audit config is required")
	}
	if readWriter == nil {
		return nil, fmt.Errorf("readWriter is required")
	}
	if deviceID == "" {
		return nil, fmt.Errorf("deviceID is required")
	}
	if agentVersion == "" {
		return nil, fmt.Errorf("agentVersion is required")
	}
	if log == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Validate config using the readWriter
	if err := config.Validate(readWriter); err != nil {
		return nil, fmt.Errorf("invalid audit config: %w", err)
	}

	// Initialize lumberjack logger with hardcoded rotation settings
	rotatingLog := &lumberjack.Logger{
		Filename:   DefaultLogPath,
		MaxSize:    DefaultMaxSizeKB / 1024, // Convert KB to MB for lumberjack
		MaxBackups: DefaultMaxBackups,
		MaxAge:     DefaultMaxAge,
		Compress:   false, // Keep uncompressed for easier debugging
	}

	return &FileLogger{
		config:       config,
		readWriter:   readWriter,
		deviceID:     deviceID,
		agentVersion: agentVersion,
		log:          log,
		rotatingLog:  rotatingLog,
	}, nil
}

// LogEvent logs a complete audit event with all required fields.
// The ctx parameter is currently unused but reserved for future extensibility
// (e.g., context-aware logging, distributed tracing, cancellation support).
func (f *FileLogger) LogEvent(ctx context.Context, info *AuditEventInfo) error {
	if f.config.Enabled == nil || !*f.config.Enabled {
		return nil
	}

	event := AuditEvent{
		Ts:                   info.StartTime.UTC().Format(time.RFC3339),
		Device:               info.Device,
		OldVersion:           info.OldVersion,
		NewVersion:           info.NewVersion,
		Result:               info.Result,
		Type:                 info.Type,
		FleetTemplateVersion: info.FleetTemplateVersion,
		AgentVersion:         f.agentVersion,
	}

	return f.writeEvent(event)
}

// Close closes the audit logger and flushes any pending writes.
func (f *FileLogger) Close() error {
	if f.rotatingLog != nil {
		return f.rotatingLog.Close()
	}
	return nil
}

// writeEvent writes an audit event to the log file with rotation.
// Uses lumberjack for rotation in production, fileio for testing.
func (f *FileLogger) writeEvent(event AuditEvent) error {
	// Marshal event to JSON
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling audit event: %w", err)
	}

	// Add newline for JSON lines format
	eventBytes = append(eventBytes, '\n')

	// Check if we're in test mode (fileio has PathFor method that indicates testing)
	// In tests, use fileio AppendFile for mockability. In production, use lumberjack for rotation.
	if testPath := f.readWriter.PathFor(""); testPath != "" {
		// Test mode: use fileio AppendFile (allows mocking)
		if err := f.readWriter.AppendFile(DefaultLogPath, eventBytes, fileio.DefaultFilePermissions); err != nil {
			return fmt.Errorf("appending audit event to %q: %w", DefaultLogPath, err)
		}
	} else {
		// Production mode: use lumberjack directly for log rotation.
		// ARCHITECTURAL NOTE: This bypasses the FileIO abstraction, which is an accepted
		// tradeoff because:
		// 1. lumberjack provides battle-tested rotation logic
		// 2. audit logs are append-only with low risk surface
		// 3. tests still use FileIO mock via PathFor() detection
		// 4. extending FileIO for rotation would add complexity for a single use case
		// This pattern should be reconsidered if rotation becomes needed elsewhere.
		if _, err := f.rotatingLog.Write(eventBytes); err != nil {
			return fmt.Errorf("writing audit event to rotating log: %w", err)
		}
	}

	f.log.Debugf("Wrote audit event: %s %s->%s %s", event.Type, event.OldVersion, event.NewVersion, event.Result)

	return nil
}
