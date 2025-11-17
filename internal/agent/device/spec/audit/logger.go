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
	// Convert KB to MB with proper rounding up and enforce minimum of 1 MB
	maxSizeMB := (DefaultMaxSizeKB + 1023) / 1024
	if maxSizeMB < 1 {
		maxSizeMB = 1
	}

	// Use readWriter to get the correct log path (handles test vs production)
	logPath := readWriter.PathFor(DefaultLogPath)

	rotatingLog := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    maxSizeMB, // MB (300KB rounds up to 1MB)
		MaxBackups: DefaultMaxBackups,
		MaxAge:     DefaultMaxAge,
		Compress:   true, // Compress rotated files to minimize footprint
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
// The ctx parameter is used to check for cancellation before performing work.
func (f *FileLogger) LogEvent(ctx context.Context, info *EventInfo) error {
	// Check context before doing work
	if err := ctx.Err(); err != nil {
		return err
	}

	if f.config.Enabled == nil || !*f.config.Enabled {
		f.log.Debug("Audit logging is disabled, skipping event")
		return nil
	}

	event := Event{
		Ts:                   info.StartTime.UTC().Format(time.RFC3339),
		Device:               info.Device,
		OldVersion:           info.OldVersion,
		NewVersion:           info.NewVersion,
		Result:               info.Result,
		Reason:               info.Reason,
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

// writeEvent writes an audit event to the log file with rotation using lumberjack.
// Lumberjack handles rotation automatically based on the configured size/age limits.
func (f *FileLogger) writeEvent(event Event) error {
	// Marshal event to JSON
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling audit event: %w", err)
	}

	// Add newline for JSON lines format
	eventBytes = append(eventBytes, '\n')

	// Write using lumberjack for automatic rotation (works in both test and production)
	if _, err := f.rotatingLog.Write(eventBytes); err != nil {
		return fmt.Errorf("writing audit event to rotating log: %w", err)
	}

	f.log.Debugf("Wrote audit event: reason=%s type=%s %s->%s %s", event.Reason, event.Type, event.OldVersion, event.NewVersion, event.Result)

	return nil
}
