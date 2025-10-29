package audit

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewDefaultAuditConfig(t *testing.T) {
	require := require.New(t)

	config := NewDefaultAuditConfig()

	require.NotNil(config)
	require.Equal(DefaultEnabled, config.Enabled)
	require.Equal(DefaultMaxSize, config.MaxSize)
	require.Equal(DefaultMaxBackups, config.MaxBackups)
	require.Equal(DefaultMaxAge, config.MaxAge)
}

func TestAuditConfig_Complete(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name           string
		config         *AuditConfig
		expectedConfig *AuditConfig
	}{
		{
			name: "all fields set - no changes",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    200,
				MaxBackups: 5,
				MaxAge:     14,
			},
			expectedConfig: &AuditConfig{
				Enabled:    true,
				MaxSize:    200,
				MaxBackups: 5,
				MaxAge:     14,
			},
		},
		{
			name: "zero max size - use default",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    0,
				MaxBackups: 5,
				MaxAge:     14,
			},
			expectedConfig: &AuditConfig{
				Enabled:    true,
				MaxSize:    DefaultMaxSize,
				MaxBackups: 5,
				MaxAge:     14,
			},
		},
		{
			name: "negative max size - use default",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    -10,
				MaxBackups: 5,
				MaxAge:     14,
			},
			expectedConfig: &AuditConfig{
				Enabled:    true,
				MaxSize:    DefaultMaxSize,
				MaxBackups: 5,
				MaxAge:     14,
			},
		},
		{
			name: "negative max backups - use default",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: -1,
				MaxAge:     14,
			},
			expectedConfig: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: DefaultMaxBackups,
				MaxAge:     14,
			},
		},
		{
			name: "zero max age - use default",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 5,
				MaxAge:     0,
			},
			expectedConfig: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 5,
				MaxAge:     DefaultMaxAge,
			},
		},
		{
			name: "all defaults needed",
			config: &AuditConfig{
				Enabled:    false,
				MaxSize:    0,
				MaxBackups: -1,
				MaxAge:     0,
			},
			expectedConfig: &AuditConfig{
				Enabled:    false,
				MaxSize:    DefaultMaxSize,
				MaxBackups: DefaultMaxBackups,
				MaxAge:     DefaultMaxAge,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Complete()
			require.NoError(err)
			require.Equal(tc.expectedConfig, tc.config)
		})
	}
}

func TestAuditConfig_Validate(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name          string
		config        *AuditConfig
		setupMocks    func(*fileio.MockReadWriter)
		expectedError string
	}{
		{
			name: "audit disabled - validation skipped",
			config: &AuditConfig{
				Enabled:    false,
				MaxSize:    100,
				MaxBackups: 3,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				// No expectations - validation should be skipped
			},
		},
		{
			name: "valid config with existing directory",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 3,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
		},
		{
			name: "valid config with non-existing directory - creates it",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 3,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(false, nil)
				mockRW.EXPECT().MkdirAll(filepath.Dir(DefaultLogPath), fileio.DefaultDirectoryPermissions).Return(nil)
			},
		},
		{
			name: "error checking directory existence",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 3,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(false, errors.New("path check failed"))
			},
			expectedError: "checking audit log directory",
		},
		{
			name: "error creating directory",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 3,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(false, nil)
				mockRW.EXPECT().MkdirAll(filepath.Dir(DefaultLogPath), fileio.DefaultDirectoryPermissions).Return(errors.New("mkdir failed"))
			},
			expectedError: "creating audit log directory",
		},
		{
			name: "invalid max size - zero",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    0,
				MaxBackups: 3,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
			expectedError: "audit log max size must be positive",
		},
		{
			name: "invalid max size - negative",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    -10,
				MaxBackups: 3,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
			expectedError: "audit log max size must be positive",
		},
		{
			name: "invalid max backups - negative",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: -1,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
			expectedError: "audit log max backups must be non-negative",
		},
		{
			name: "valid max backups - zero is allowed",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 0,
				MaxAge:     28,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
		},
		{
			name: "invalid max age - zero",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 3,
				MaxAge:     0,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
			expectedError: "audit log max age must be positive",
		},
		{
			name: "invalid max age - negative",
			config: &AuditConfig{
				Enabled:    true,
				MaxSize:    100,
				MaxBackups: 3,
				MaxAge:     -5,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
			expectedError: "audit log max age must be positive",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockRW)

			err := tc.config.Validate(mockRW)

			if tc.expectedError != "" {
				require.Error(err)
				require.Contains(err.Error(), tc.expectedError)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	require := require.New(t)

	// Test that constants have expected values
	require.Equal("/var/log/flightctl/audit.log", DefaultLogPath)
	require.Equal(100, DefaultMaxSize)
	require.Equal(3, DefaultMaxBackups)
	require.Equal(28, DefaultMaxAge)
	require.Equal(true, DefaultEnabled)
}
