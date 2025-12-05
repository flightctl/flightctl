package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewFileLogger(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name          string
		config        *AuditConfig
		readWriter    fileio.ReadWriter
		deviceID      string
		log           *log.PrefixLogger
		setupMocks    func(*fileio.MockReadWriter)
		expectedError string
	}{
		{
			name:          "nil config",
			config:        nil,
			readWriter:    fileio.NewMockReadWriter(ctrl),
			deviceID:      "test-device",
			log:           log.NewPrefixLogger("test"),
			expectedError: "audit config is required",
		},
		{
			name:          "nil readWriter",
			config:        NewDefaultAuditConfig(),
			readWriter:    nil,
			deviceID:      "test-device",
			log:           log.NewPrefixLogger("test"),
			expectedError: "readWriter is required",
		},
		{
			name:          "empty deviceID",
			config:        NewDefaultAuditConfig(),
			readWriter:    fileio.NewMockReadWriter(ctrl),
			deviceID:      "",
			log:           log.NewPrefixLogger("test"),
			expectedError: "deviceID is required",
		},
		{
			name:          "nil logger",
			config:        NewDefaultAuditConfig(),
			readWriter:    fileio.NewMockReadWriter(ctrl),
			deviceID:      "test-device",
			log:           nil,
			expectedError: "logger is required",
		},
		{
			name:       "success case",
			config:     NewDefaultAuditConfig(),
			readWriter: fileio.NewMockReadWriter(ctrl),
			deviceID:   "test-device",
			log:        log.NewPrefixLogger("test"),
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				// Mock PathFor call for lumberjack initialization
				mockRW.EXPECT().PathFor(DefaultLogPath).Return("/tmp/test/var/log/flightctl/audit.log")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRW, ok := tt.readWriter.(*fileio.MockReadWriter)
			if ok && tt.setupMocks != nil {
				tt.setupMocks(mockRW)
			}

			logger, err := NewFileLogger(tt.config, tt.readWriter, tt.deviceID, "test-agent-version", tt.log)

			if tt.expectedError != "" {
				require.Error(err)
				require.Contains(err.Error(), tt.expectedError)
				require.Nil(logger)
			} else {
				require.NoError(err)
				require.NotNil(logger)
				require.Equal(tt.config, logger.config)
				require.Equal(tt.deviceID, logger.deviceID)
			}
		})
	}
}

func TestNewFileLogger_EmptyAgentVersionUsesUnknown(t *testing.T) {
	require := require.New(t)

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	config := NewDefaultAuditConfig()
	logger := log.NewPrefixLogger("test")

	// Empty agentVersion should use "unknown" fallback instead of failing
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "", logger)
	require.NoError(err)
	require.NotNil(auditLogger)
	require.Equal("unknown", auditLogger.agentVersion)

	defer func() {
		_ = auditLogger.Close()
	}()
}

func TestFileLogger_LogEventApply(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	config := NewDefaultAuditConfig()
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test successful log entry
	auditInfo := &EventInfo{
		Device:               "test-device",
		OldVersion:           "1",
		NewVersion:           "2",
		Result:               ResultSuccess,
		Reason:               ReasonUpgrade,
		Type:                 TypeCurrent,
		FleetTemplateVersion: "template-v1",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)

	// Verify the log file was created and contains correct data
	data, err := readWriter.ReadFile(DefaultLogPath)
	require.NoError(err)

	lines := strings.Split(string(data), "\n")
	require.Len(lines, 2) // Event line + empty line from newline
	require.NotEmpty(lines[0])

	var event Event
	err = json.Unmarshal([]byte(lines[0]), &event)
	require.NoError(err)

	require.Equal("test-device", event.Device)
	require.Equal("1", event.OldVersion)
	require.Equal("2", event.NewVersion)
	require.Equal(ReasonUpgrade, event.Reason)
	require.Equal(TypeCurrent, event.Type)
	require.Equal(ResultSuccess, event.Result)
	require.NotEmpty(event.Ts)
	require.Equal("template-v1", event.FleetTemplateVersion)
	require.Equal("test-agent-version", event.AgentVersion)
}

func TestFileLogger_LogEventFailure(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	config := NewDefaultAuditConfig()
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test failure logging
	auditInfo := &EventInfo{
		Device:               "test-device",
		OldVersion:           "2",
		NewVersion:           "3",
		Result:               ResultFailure,
		Reason:               ReasonUpgrade,
		Type:                 TypeCurrent, // Failed upgrade attempt
		FleetTemplateVersion: "template-v2",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)

	// Verify the log file contains correct data
	data, err := readWriter.ReadFile(DefaultLogPath)
	require.NoError(err)

	lines := strings.Split(string(data), "\n")
	require.NotEmpty(lines[0])

	var event Event
	err = json.Unmarshal([]byte(lines[0]), &event)
	require.NoError(err)

	require.Equal("test-device", event.Device)
	require.Equal("2", event.OldVersion)
	require.Equal("3", event.NewVersion)
	require.Equal(ReasonUpgrade, event.Reason) // Failed upgrade attempt
	require.Equal(TypeCurrent, event.Type)
	require.Equal(ResultFailure, event.Result)
	require.NotEmpty(event.Ts)
	require.Equal("template-v2", event.FleetTemplateVersion)
	require.Equal("test-agent-version", event.AgentVersion)
}

func TestFileLogger_LogEventRollback(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	config := NewDefaultAuditConfig()
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test successful log rollback
	auditInfo := &EventInfo{
		Device:               "test-device",
		OldVersion:           "3",
		NewVersion:           "2",
		Result:               ResultSuccess,
		Reason:               ReasonRollback,
		Type:                 TypeRollback,
		FleetTemplateVersion: "template-rollback",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)

	// Verify the log file contains correct data
	data, err := readWriter.ReadFile(DefaultLogPath)
	require.NoError(err)

	lines := strings.Split(string(data), "\n")
	require.Len(lines, 2) // Event line + empty line from newline
	require.NotEmpty(lines[0])

	var event Event
	err = json.Unmarshal([]byte(lines[0]), &event)
	require.NoError(err)

	require.Equal("test-device", event.Device)
	require.Equal("3", event.OldVersion)
	require.Equal("2", event.NewVersion)
	require.Equal(ReasonRollback, event.Reason)
	require.Equal(TypeRollback, event.Type)
	require.Equal(ResultSuccess, event.Result)
	require.NotEmpty(event.Ts)
	require.Equal("template-rollback", event.FleetTemplateVersion)
	require.Equal("test-agent-version", event.AgentVersion)
}

func TestFileLogger_LogEventBootstrap(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	config := NewDefaultAuditConfig()
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test bootstrap event logging
	auditInfo := &EventInfo{
		Device:               "test-device",
		OldVersion:           "", // No previous version for bootstrap
		NewVersion:           "0",
		Result:               ResultSuccess,
		Reason:               ReasonBootstrap,
		Type:                 TypeCurrent,
		FleetTemplateVersion: "", // No fleet template on bootstrap
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)

	// Verify the log file contains correct data
	data, err := readWriter.ReadFile(DefaultLogPath)
	require.NoError(err)

	lines := strings.Split(string(data), "\n")
	require.Len(lines, 2) // Event line + empty line from newline
	require.NotEmpty(lines[0])

	var event Event
	err = json.Unmarshal([]byte(lines[0]), &event)
	require.NoError(err)

	require.Equal("test-device", event.Device)
	require.Equal("", event.OldVersion)  // Bootstrap has no old version
	require.Equal("0", event.NewVersion) // Bootstrap starts at version 0
	require.Equal(ReasonBootstrap, event.Reason)
	require.Equal(TypeCurrent, event.Type)
	require.Equal(ResultSuccess, event.Result)
	require.NotEmpty(event.Ts)
	require.Equal("", event.FleetTemplateVersion) // No fleet template on bootstrap
	require.Equal("test-agent-version", event.AgentVersion)
}

func TestFileLogger_LogEventSync(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	config := NewDefaultAuditConfig()
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test sync event logging
	auditInfo := &EventInfo{
		Device:               "test-device",
		OldVersion:           "1",
		NewVersion:           "2",
		Result:               ResultSuccess,
		Reason:               ReasonSync,
		Type:                 TypeDesired,
		FleetTemplateVersion: "template-sync",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)

	// Verify the log file contains correct data
	data, err := readWriter.ReadFile(DefaultLogPath)
	require.NoError(err)

	lines := strings.Split(string(data), "\n")
	require.Len(lines, 2) // Event line + empty line from newline
	require.NotEmpty(lines[0])

	var event Event
	err = json.Unmarshal([]byte(lines[0]), &event)
	require.NoError(err)

	require.Equal("test-device", event.Device)
	require.Equal("1", event.OldVersion)
	require.Equal("2", event.NewVersion)
	require.Equal(ReasonSync, event.Reason)
	require.Equal(TypeDesired, event.Type)
	require.Equal(ResultSuccess, event.Result)
	require.NotEmpty(event.Ts)
	require.Equal("template-sync", event.FleetTemplateVersion)
	require.Equal("test-agent-version", event.AgentVersion)
}

func TestFileLogger_DifferentEventTypes(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	config := NewDefaultAuditConfig()
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test different event types
	events := []struct {
		info           *EventInfo
		expectedReason Reason
		expectedType   Type
	}{
		{
			info: &EventInfo{
				Device:               "test-device",
				OldVersion:           "",
				NewVersion:           "0",
				Result:               ResultSuccess,
				Reason:               ReasonBootstrap,
				Type:                 TypeCurrent,
				FleetTemplateVersion: "",
				StartTime:            time.Now(),
			},
			expectedReason: ReasonBootstrap,
			expectedType:   TypeCurrent,
		},
		{
			info: &EventInfo{
				Device:               "test-device",
				OldVersion:           "0",
				NewVersion:           "1",
				Result:               ResultSuccess,
				Reason:               ReasonSync,
				Type:                 TypeDesired,
				FleetTemplateVersion: "template-v1",
				StartTime:            time.Now(),
			},
			expectedReason: ReasonSync,
			expectedType:   TypeDesired,
		},
		{
			info: &EventInfo{
				Device:               "test-device",
				OldVersion:           "0",
				NewVersion:           "1",
				Result:               ResultSuccess,
				Reason:               ReasonUpgrade,
				Type:                 TypeCurrent,
				FleetTemplateVersion: "template-v1",
				StartTime:            time.Now(),
			},
			expectedReason: ReasonUpgrade,
			expectedType:   TypeCurrent,
		},
		{
			info: &EventInfo{
				Device:               "test-device",
				OldVersion:           "1",
				NewVersion:           "0",
				Result:               ResultSuccess,
				Reason:               ReasonRollback,
				Type:                 TypeRollback,
				FleetTemplateVersion: "template-v0",
				StartTime:            time.Now(),
			},
			expectedReason: ReasonRollback,
			expectedType:   TypeRollback,
		},
	}

	for _, e := range events {
		err := auditLogger.LogEvent(ctx, e.info)
		require.NoError(err)
	}

	// Verify all events were logged correctly
	data, err := readWriter.ReadFile(DefaultLogPath)
	require.NoError(err)

	lines := strings.Split(string(data), "\n")
	require.Len(lines, len(events)+1) // Events + final newline

	for i, e := range events {
		var event Event
		err := json.Unmarshal([]byte(lines[i]), &event)
		require.NoError(err)
		require.Equal(e.expectedReason, event.Reason)
		require.Equal(e.expectedType, event.Type)
	}
}

func TestFileLogger_RotationBehavior(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	config := NewDefaultAuditConfig()
	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Write multiple events to verify append behavior
	for i := 0; i < 5; i++ {
		auditInfo := &EventInfo{
			Device:               "test-device",
			OldVersion:           "1",
			NewVersion:           "2",
			Result:               ResultSuccess,
			Reason:               ReasonUpgrade,
			Type:                 TypeCurrent,
			FleetTemplateVersion: "template-v1",
			StartTime:            time.Now(),
		}
		err := auditLogger.LogEvent(ctx, auditInfo)
		require.NoError(err)
	}

	// Verify all events were appended
	data, err := readWriter.ReadFile(DefaultLogPath)
	require.NoError(err)

	lines := strings.Split(string(data), "\n")
	require.Len(lines, 6) // 5 events + final newline
}

func TestFileLogger_DisabledLogging(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// Create temp directory for test
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
	logger := log.NewPrefixLogger("test")

	// Create config with logging disabled
	disabled := false
	config := &AuditConfig{
		Enabled: &disabled,
	}

	auditLogger, err := NewFileLogger(config, readWriter, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Try to log an event
	auditInfo := &EventInfo{
		Device:               "test-device",
		OldVersion:           "1",
		NewVersion:           "2",
		Result:               ResultSuccess,
		Reason:               ReasonUpgrade,
		Type:                 TypeCurrent,
		FleetTemplateVersion: "template-v1",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)

	// Verify no log file was created
	_, err = readWriter.ReadFile(DefaultLogPath)
	require.Error(err)
	require.True(fileio.IsNotExist(err))
}
