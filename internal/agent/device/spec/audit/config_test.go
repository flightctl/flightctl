package audit

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewDefaultAuditConfig(t *testing.T) {
	require := require.New(t)

	config := NewDefaultAuditConfig()

	require.NotNil(config)
	require.NotNil(config.Enabled)
	require.Equal(DefaultEnabled, *config.Enabled)
}

func TestAuditConfig_Complete(t *testing.T) {
	require := require.New(t)

	// Test that Complete() doesn't modify the config since rotation settings are hardcoded
	enabled := true
	disabled := false
	testCases := []struct {
		name   string
		config *AuditConfig
	}{
		{
			name: "enabled config unchanged",
			config: &AuditConfig{
				Enabled: &enabled,
			},
		},
		{
			name: "disabled config unchanged",
			config: &AuditConfig{
				Enabled: &disabled,
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

	disabled := false
	enabled := true
	testCases := []struct {
		name          string
		config        *AuditConfig
		setupMocks    func(*fileio.MockReadWriter)
		expectedError string
	}{
		{
			name: "audit disabled - validation skipped",
			config: &AuditConfig{
				Enabled: &disabled,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				// No expectations - validation should be skipped
			},
		},
		{
			name: "audit enabled - pure validation with no side effects",
			config: &AuditConfig{
				Enabled: &enabled,
			},
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				// No expectations - validation is pure with no directory creation
				// Directory will be created lazily on first write
			},
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
	require.Equal(2048, DefaultMaxSizeKB) // 2MB per file (≈6,990 records)
	require.Equal(3, DefaultMaxBackups)   // 3 backups (8 MB total with active file)
	require.Equal(0, DefaultMaxAge)       // No time-based pruning
}
