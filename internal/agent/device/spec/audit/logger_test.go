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
				// No mocks needed - validation is pure with no side effects
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

func TestFileLogger_LogEventApply(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test successful log apply
	mockRW.EXPECT().PathFor("").Return("/test/root")
	mockRW.EXPECT().AppendFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
		func(path string, data []byte, perm interface{}, opts ...interface{}) error {
			// Verify the JSON structure
			lines := strings.Split(string(data), "\n")
			require.Len(lines, 2) // Event line + empty line from newline
			require.NotEmpty(lines[0])

			var event AuditEvent
			err := json.Unmarshal([]byte(lines[0]), &event)
			require.NoError(err)

			require.Equal("test-device", event.Device)
			require.Equal("1", event.OldVersion)
			require.Equal("2", event.NewVersion)
			require.Equal(AuditTypeUpgrade, event.Type)
			require.Equal(AuditResultSuccess, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal("template-v1", event.FleetTemplateVersion)
			require.Equal("test-agent-version", event.AgentVersion)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:               "test-device",
		OldVersion:           "1",
		NewVersion:           "2",
		Result:               AuditResultSuccess,
		Type:                 AuditTypeUpgrade,
		FleetTemplateVersion: "template-v1",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)
}

func TestFileLogger_LogEventFailure(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test failure logging
	mockRW.EXPECT().PathFor("").Return("/test/root")
	mockRW.EXPECT().AppendFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
		func(path string, data []byte, perm interface{}, opts ...interface{}) error {
			// Verify the JSON structure
			lines := strings.Split(string(data), "\n")
			require.NotEmpty(lines[0])

			var event AuditEvent
			err := json.Unmarshal([]byte(lines[0]), &event)
			require.NoError(err)

			require.Equal("test-device", event.Device)
			require.Equal("2", event.OldVersion)
			require.Equal("3", event.NewVersion)
			require.Equal(AuditTypeUpgrade, event.Type) // Failed upgrade attempt
			require.Equal(AuditResultFailure, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal("template-v2", event.FleetTemplateVersion)
			require.Equal("test-agent-version", event.AgentVersion)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:               "test-device",
		OldVersion:           "2",
		NewVersion:           "3",
		Result:               AuditResultFailure,
		Type:                 AuditTypeUpgrade, // Failed upgrade attempt
		FleetTemplateVersion: "template-v2",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)
}

func TestFileLogger_LogEventRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test successful log rollback
	mockRW.EXPECT().PathFor("").Return("/test/root")
	mockRW.EXPECT().AppendFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
		func(path string, data []byte, perm interface{}, opts ...interface{}) error {
			// Verify the JSON structure
			lines := strings.Split(string(data), "\n")
			require.Len(lines, 2) // Event line + empty line from newline
			require.NotEmpty(lines[0])

			var event AuditEvent
			err := json.Unmarshal([]byte(lines[0]), &event)
			require.NoError(err)

			require.Equal("test-device", event.Device)
			require.Equal("3", event.OldVersion)
			require.Equal("2", event.NewVersion)
			require.Equal(AuditTypeRollback, event.Type)
			require.Equal(AuditResultSuccess, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal("template-rollback", event.FleetTemplateVersion)
			require.Equal("test-agent-version", event.AgentVersion)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:               "test-device",
		OldVersion:           "3",
		NewVersion:           "2",
		Result:               AuditResultSuccess,
		Type:                 AuditTypeRollback,
		FleetTemplateVersion: "template-rollback",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)
}

func TestFileLogger_LogEventBootstrap(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test bootstrap event logging
	mockRW.EXPECT().PathFor("").Return("/test/root")
	mockRW.EXPECT().AppendFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
		func(path string, data []byte, perm interface{}, opts ...interface{}) error {
			// Verify the JSON structure
			lines := strings.Split(string(data), "\n")
			require.Len(lines, 2) // Event line + empty line from newline
			require.NotEmpty(lines[0])

			var event AuditEvent
			err := json.Unmarshal([]byte(lines[0]), &event)
			require.NoError(err)

			require.Equal("test-device", event.Device)
			require.Equal("", event.OldVersion)  // Bootstrap has no old version
			require.Equal("0", event.NewVersion) // Bootstrap starts at version 0
			require.Equal(AuditTypeBootstrap, event.Type)
			require.Equal(AuditResultSuccess, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal("", event.FleetTemplateVersion) // No fleet template on bootstrap
			require.Equal("test-agent-version", event.AgentVersion)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:               "test-device",
		OldVersion:           "", // No previous version for bootstrap
		NewVersion:           "0",
		Result:               AuditResultSuccess,
		Type:                 AuditTypeBootstrap,
		FleetTemplateVersion: "", // No fleet template on bootstrap
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)
}

func TestFileLogger_LogEventSync(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test sync event logging
	mockRW.EXPECT().PathFor("").Return("/test/root")
	mockRW.EXPECT().AppendFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
		func(path string, data []byte, perm interface{}, opts ...interface{}) error {
			// Verify the JSON structure
			lines := strings.Split(string(data), "\n")
			require.Len(lines, 2) // Event line + empty line from newline
			require.NotEmpty(lines[0])

			var event AuditEvent
			err := json.Unmarshal([]byte(lines[0]), &event)
			require.NoError(err)

			require.Equal("test-device", event.Device)
			require.Equal("1", event.OldVersion) // Previous desired version
			require.Equal("2", event.NewVersion) // New desired version from API
			require.Equal(AuditTypeSync, event.Type)
			require.Equal(AuditResultSuccess, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal("template-v2", event.FleetTemplateVersion)
			require.Equal("test-agent-version", event.AgentVersion)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:               "test-device",
		OldVersion:           "1", // Old desired version
		NewVersion:           "2", // New desired version from management API
		Result:               AuditResultSuccess,
		Type:                 AuditTypeSync,
		FleetTemplateVersion: "template-v2",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)
}

func TestFileLogger_DisabledLogging(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	config := NewDefaultAuditConfig()
	disabled := false
	config.Enabled = &disabled // Disable logging
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Should not make any file operations when disabled
	auditInfo := &AuditEventInfo{
		Device:               "test-device",
		OldVersion:           "1",
		NewVersion:           "2",
		Result:               AuditResultSuccess,
		Type:                 AuditTypeUpgrade,
		FleetTemplateVersion: "template-disabled",
		StartTime:            time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)
}

func TestFileLogger_RotationConfiguration(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)
	require.NotNil(auditLogger.rotatingLog)

	// Verify lumberjack configuration matches hardcoded defaults
	require.Equal(DefaultLogPath, auditLogger.rotatingLog.Filename)
	require.Equal(DefaultMaxSizeKB/1024, auditLogger.rotatingLog.MaxSize, "MaxSize should be 2MB")
	require.Equal(DefaultMaxBackups, auditLogger.rotatingLog.MaxBackups, "Should keep 3 backup files")
	require.Equal(DefaultMaxAge, auditLogger.rotatingLog.MaxAge, "No time-based pruning")
	require.False(auditLogger.rotatingLog.Compress, "Files should not be compressed")
}

func TestFileLogger_RotationBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping rotation test in short mode - writes >1MB of data")
	}

	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a temporary directory for test logs
	tmpDir := t.TempDir()
	testLogPath := tmpDir + "/audit.log"

	// Create a FileLogger with mocked readWriter to bypass validation
	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "v1.0.0", logger)
	require.NoError(err)

	// Set very small MaxSize to trigger rotation quickly (100KB instead of 2MB)
	auditLogger.rotatingLog.Filename = testLogPath
	auditLogger.rotatingLog.MaxSize = 1    // 1MB
	auditLogger.rotatingLog.MaxBackups = 2 // Only keep 2 backups for testing

	ctx := context.Background()

	// Mock PathFor to return "" → triggers production mode (lumberjack)
	mockRW.EXPECT().PathFor("").Return("").AnyTimes()

	// Calculate how many events needed to exceed 1MB
	// Each event is ~90 bytes (measured: 7000 events = 631KB)
	// Need ~12,000 events to exceed 1MB, write 15,000 to ensure rotation
	eventCount := 15000
	t.Logf("Writing %d events to trigger rotation...", eventCount)

	for i := 0; i < eventCount; i++ {
		auditInfo := &AuditEventInfo{
			Device:               "test-device-with-a-long-name-to-increase-event-size",
			OldVersion:           "1",
			NewVersion:           "2",
			Result:               AuditResultSuccess,
			Type:                 AuditTypeUpgrade,
			FleetTemplateVersion: "template-version-12345",
			StartTime:            time.Now(),
		}
		err := auditLogger.LogEvent(ctx, auditInfo)
		require.NoError(err)
	}

	// Close the logger to ensure all writes are flushed
	err = auditLogger.Close()
	require.NoError(err)

	// Check for backup files created by rotation
	realRW := fileio.NewReadWriter()

	// List all files in temp directory to see what was created
	files, err := realRW.ReadDir(tmpDir)
	require.NoError(err)
	t.Logf("Files in temp directory:")
	for _, file := range files {
		if !file.IsDir() {
			// Get file info to see size
			info, err := file.Info()
			if err == nil {
				t.Logf("  - %s (%d bytes)", file.Name(), info.Size())
			} else {
				t.Logf("  - %s (size unknown)", file.Name())
			}
		}
	}

	// Main log file should exist
	mainExists, err := realRW.PathExists(testLogPath)
	require.NoError(err)
	require.True(mainExists, "Main audit log file should exist")

	// Count backup files (lumberjack creates timestamp-based backups, not numbered)
	// Pattern: audit-YYYY-MM-DDTHH-MM-SS.mmm.log
	backupCount := 0
	var backupSizes []int
	for _, file := range files {
		if !file.IsDir() && strings.HasPrefix(file.Name(), "audit-") && strings.HasSuffix(file.Name(), ".log") {
			backupCount++
			info, _ := file.Info()
			if info != nil {
				backupSizes = append(backupSizes, int(info.Size()))
				t.Logf("Backup file: %s (%d bytes, %.2f MB)", file.Name(), info.Size(), float64(info.Size())/(1024*1024))
			}
		}
	}

	// Get main file size
	mainData, err := realRW.ReadFile(testLogPath)
	require.NoError(err)
	mainSize := len(mainData)
	t.Logf("Main log file: %d bytes (%.2f MB)", mainSize, float64(mainSize)/(1024*1024))
	t.Logf("Total backup files created: %d", backupCount)

	// Verify rotation actually happened
	require.Greater(backupCount, 0, "Log rotation should have created at least one backup file")

	// Verify backup files are approximately 1MB (allowing for some variation)
	for i, size := range backupSizes {
		sizeMB := float64(size) / (1024 * 1024)
		require.Greater(sizeMB, 0.9, "Backup file %d should be close to 1MB, got %.2f MB", i+1, sizeMB)
		require.Less(sizeMB, 1.2, "Backup file %d should be close to 1MB, got %.2f MB", i+1, sizeMB)
	}
}

func TestFileLogger_Close(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", "test-agent-version", logger)
	require.NoError(err)

	// Test that Close() doesn't return an error
	err = auditLogger.Close()
	require.NoError(err)
}
