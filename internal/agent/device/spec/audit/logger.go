package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Logger = (*FileLogger)(nil)

// FileLogger implements the Logger interface using file-based logging.
// Following the pattern from config.Controller struct.
type FileLogger struct {
	config     *AuditConfig
	readWriter fileio.ReadWriter
	deviceID   string
	log        *log.PrefixLogger
}

// NewFileLogger creates a new file-based audit logger.
// Following the dependency injection pattern from status.NewManager and config.NewController.
func NewFileLogger(
	config *AuditConfig,
	readWriter fileio.ReadWriter,
	deviceID string,
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
	if log == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Validate config using the readWriter
	if err := config.Validate(readWriter); err != nil {
		return nil, fmt.Errorf("invalid audit config: %w", err)
	}

	return &FileLogger{
		config:     config,
		readWriter: readWriter,
		deviceID:   deviceID,
		log:        log,
	}, nil
}

// LogEvent logs a complete audit event with all required fields.
func (f *FileLogger) LogEvent(ctx context.Context, info *AuditEventInfo) error {
	if !f.config.Enabled {
		return nil
	}

	event := AuditEvent{
		Ts:         info.StartTime.UTC().Format(time.RFC3339),
		Device:     info.Device,
		OldVersion: info.OldVersion,
		NewVersion: info.NewVersion,
		OldHash:    info.OldHash,
		NewHash:    info.NewHash,
		Result:     info.Result,
		DurationMs: info.DurationMs,
		Type:       info.Type,
	}

	return f.writeEvent(event)
}

// Close closes the audit logger and flushes any pending writes.
func (f *FileLogger) Close() error {
	// No special cleanup needed for simple append-only file logging
	return nil
}

// writeEvent writes an audit event to the log file.
// Following the pattern from config controller file writing.
func (f *FileLogger) writeEvent(event AuditEvent) error {
	// Marshal event to JSON
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling audit event: %w", err)
	}

	// Add newline for JSON lines format
	eventBytes = append(eventBytes, '\n')

	// Read existing content if file exists
	var existingContent []byte
	if exists, _ := f.readWriter.PathExists(DefaultLogPath); exists {
		existingContent, err = f.readWriter.ReadFile(DefaultLogPath)
		if err != nil {
			f.log.Warnf("Failed to read existing audit log: %v", err)
			// Continue with empty content
			existingContent = []byte{}
		}
	}

	// Append new event
	newContent := append(existingContent, eventBytes...)

	// Write back to file using fileio abstraction
	if err := f.readWriter.WriteFile(DefaultLogPath, newContent, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing audit event to %q: %w", DefaultLogPath, err)
	}

	f.log.Debugf("Wrote audit event: %s %s->%s %s (%dms)", event.Type, event.OldVersion, event.NewVersion, event.Result, event.DurationMs)

	return nil
}
