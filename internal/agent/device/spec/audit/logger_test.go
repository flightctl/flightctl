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
				// Mock the validation calls
				mockRW.EXPECT().PathExists("/var/log/flightctl").Return(true, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRW, ok := tt.readWriter.(*fileio.MockReadWriter)
			if ok && tt.setupMocks != nil {
				tt.setupMocks(mockRW)
			}

			logger, err := NewFileLogger(tt.config, tt.readWriter, tt.deviceID, tt.log)

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

	// Setup validation mocks
	mockRW.EXPECT().PathExists("/var/log/flightctl").Return(true, nil)

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", logger)
	require.NoError(err)

	// Test successful log apply
	mockRW.EXPECT().PathExists(DefaultLogPath).Return(false, nil)
	mockRW.EXPECT().WriteFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
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
			require.Equal("hash1", event.OldHash)
			require.Equal("hash2", event.NewHash)
			require.Equal(AuditTypeApply, event.Type)
			require.Equal(AuditResultSuccess, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal(int64(1500), event.DurationMs)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:     "test-device",
		OldVersion: "1",
		NewVersion: "2",
		OldHash:    "hash1",
		NewHash:    "hash2",
		Result:     AuditResultSuccess,
		DurationMs: 1500,
		Type:       AuditTypeApply,
		StartTime:  time.Now(),
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

	// Setup validation mocks
	mockRW.EXPECT().PathExists("/var/log/flightctl").Return(true, nil)

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", logger)
	require.NoError(err)

	// Test failure logging
	mockRW.EXPECT().PathExists(DefaultLogPath).Return(false, nil)
	mockRW.EXPECT().WriteFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
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
			require.Equal("oldhash", event.OldHash)
			require.Equal("newhash", event.NewHash)
			require.Equal(AuditTypeFailure, event.Type)
			require.Equal(AuditResultFailure, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal(int64(500), event.DurationMs)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:     "test-device",
		OldVersion: "2",
		NewVersion: "3",
		OldHash:    "oldhash",
		NewHash:    "newhash",
		Result:     AuditResultFailure,
		DurationMs: 500,
		Type:       AuditTypeFailure,
		StartTime:  time.Now(),
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

	// Setup validation mocks
	mockRW.EXPECT().PathExists("/var/log/flightctl").Return(true, nil)

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", logger)
	require.NoError(err)

	// Test successful log rollback
	mockRW.EXPECT().PathExists(DefaultLogPath).Return(false, nil)
	mockRW.EXPECT().WriteFile(DefaultLogPath, gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(
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
			require.Equal("rollback-old", event.OldHash)
			require.Equal("rollback-new", event.NewHash)
			require.Equal(AuditTypeRollback, event.Type)
			require.Equal(AuditResultSuccess, event.Result)
			require.NotEmpty(event.Ts)
			require.Equal(int64(800), event.DurationMs)

			return nil
		})

	auditInfo := &AuditEventInfo{
		Device:     "test-device",
		OldVersion: "3",
		NewVersion: "2",
		OldHash:    "rollback-old",
		NewHash:    "rollback-new",
		Result:     AuditResultSuccess,
		DurationMs: 800,
		Type:       AuditTypeRollback,
		StartTime:  time.Now(),
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
	config.Enabled = false // Disable logging
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", logger)
	require.NoError(err)

	// Should not make any file operations when disabled
	auditInfo := &AuditEventInfo{
		Device:     "test-device",
		OldVersion: "1",
		NewVersion: "2",
		OldHash:    "hash1",
		NewHash:    "hash2",
		Result:     AuditResultSuccess,
		DurationMs: 1000,
		Type:       AuditTypeApply,
		StartTime:  time.Now(),
	}
	err = auditLogger.LogEvent(ctx, auditInfo)
	require.NoError(err)
}

func TestFileLogger_Close(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	config := NewDefaultAuditConfig()
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	// Setup validation mocks
	mockRW.EXPECT().PathExists("/var/log/flightctl").Return(true, nil)

	auditLogger, err := NewFileLogger(config, mockRW, "test-device", logger)
	require.NoError(err)

	// Test that Close() doesn't return an error
	err = auditLogger.Close()
	require.NoError(err)
}
