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
}

func TestAuditConfig_Complete(t *testing.T) {
	require := require.New(t)

	// Test that Complete() doesn't modify the config since rotation settings are hardcoded
	testCases := []struct {
		name   string
		config *AuditConfig
	}{
		{
			name: "enabled config unchanged",
			config: &AuditConfig{
				Enabled: true,
			},
		},
		{
			name: "disabled config unchanged",
			config: &AuditConfig{
				Enabled: false,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			originalConfig := &AuditConfig{Enabled: tc.config.Enabled}
			err := tc.config.Complete()
			require.NoError(err)
			require.Equal(originalConfig, tc.config)
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
				Enabled: false,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				// No expectations - validation should be skipped
			},
		},
		{
			name: "valid config with existing directory",
			config: &AuditConfig{
				Enabled: true,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(true, nil)
			},
		},
		{
			name: "valid config with non-existing directory - creates it",
			config: &AuditConfig{
				Enabled: true,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(false, nil)
				mockRW.EXPECT().MkdirAll(filepath.Dir(DefaultLogPath), fileio.DefaultDirectoryPermissions).Return(nil)
			},
		},
		{
			name: "error checking directory existence",
			config: &AuditConfig{
				Enabled: true,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(false, errors.New("path check failed"))
			},
			expectedError: "checking audit log directory",
		},
		{
			name: "error creating directory",
			config: &AuditConfig{
				Enabled: true,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(filepath.Dir(DefaultLogPath)).Return(false, nil)
				mockRW.EXPECT().MkdirAll(filepath.Dir(DefaultLogPath), fileio.DefaultDirectoryPermissions).Return(errors.New("mkdir failed"))
			},
			expectedError: "creating audit log directory",
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
	require.Equal(true, DefaultEnabled)

	// Test hardcoded rotation constants (non-configurable)
	require.Equal(1024, DefaultMaxSizeKB) // 1MB for ~10k records
	require.Equal(1, DefaultMaxBackups)   // Minimal rotations
	require.Equal(0, DefaultMaxAge)       // No time-based pruning
}
