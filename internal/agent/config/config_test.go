package config

import (
	"os"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

var yamlConfig = `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
  enrollment-ui-endpoint: https://ui.enrollment.endpoint
management-service:
  service:
    server: https://management.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
spec-fetch-interval: 0m10s
status-update-interval: 0m10s`

func TestParseConfigFile(t *testing.T) {
	require := require.New(t)

	tmpDir := t.TempDir()
	filePath := tmpDir + "/config.yaml"
	err := os.WriteFile(filePath, []byte(yamlConfig), 0600)
	require.NoError(err)

	cfg := NewDefault()
	err = cfg.ParseConfigFile(filePath)
	require.NoError(err)

	// ensure override
	require.Equal("https://enrollment.endpoint", cfg.EnrollmentService.Service.Server)
	require.Equal("https://ui.enrollment.endpoint", cfg.EnrollmentService.EnrollmentUIEndpoint)
	require.Equal("https://management.endpoint", cfg.ManagementService.Service.Server)
	require.Equal("10s", cfg.SpecFetchInterval.String())
	require.Equal("10s", cfg.StatusUpdateInterval.String())

	// ensure defaults
	require.Equal(DefaultConfigDir, cfg.ConfigDir)
	require.Equal(DefaultDataDir, cfg.DataDir)
	require.Equal(logrus.InfoLevel.String(), cfg.LogLevel)
}

func TestParseConfigFile_NoFile(t *testing.T) {
	require := require.New(t)

	tmpDir := t.TempDir()
	filePath := tmpDir + "/nonexistent.yaml"

	cfg := NewDefault()
	err := cfg.ParseConfigFile(filePath)

	// Expect an error because the file does not exist
	require.Error(err)
}

func TestTPMConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		tpmConfig   TPMConfig
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty config should be valid",
			tpmConfig:   TPMConfig{},
			expectError: false,
		},
		{
			name: "valid key blob config",
			tpmConfig: TPMConfig{
				PersistenceType:     TPMPersistenceTypeKeyBlob,
				PersistenceMetadata: "/tmp/tpm-blob",
			},
			expectError: false,
		},
		{
			name: "valid auto handle config",
			tpmConfig: TPMConfig{
				PersistenceType:     TPMPersistenceTypeAutoHandle,
				PersistenceMetadata: "/tmp/tpm-handle",
			},
			expectError: false,
		},
		{
			name: "valid fixed handle config",
			tpmConfig: TPMConfig{
				PersistenceType:     TPMPersistenceTypeFixedHandle,
				PersistenceMetadata: "0x81000000",
			},
			expectError: false,
		},
		{
			name: "valid none config without metadata",
			tpmConfig: TPMConfig{
				PersistenceType: TPMPersistenceTypeNone,
			},
			expectError: false,
		},
		{
			name: "valid none config with metadata",
			tpmConfig: TPMConfig{
				PersistenceType:     TPMPersistenceTypeNone,
				PersistenceMetadata: "ignored-metadata",
			},
			expectError: false,
		},
		{
			name: "invalid persistence type",
			tpmConfig: TPMConfig{
				PersistenceType:     "invalid",
				PersistenceMetadata: "/tmp/tpm-blob",
			},
			expectError: true,
			errorMsg:    "TPM persistence type \"invalid\" must be one of",
		},
		{
			name: "missing metadata",
			tpmConfig: TPMConfig{
				PersistenceType: TPMPersistenceTypeKeyBlob,
			},
			expectError: true,
			errorMsg:    "TPM persistence metadata is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tpmConfig.Validate()
			if tt.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestTPMConfigMerging(t *testing.T) {
	tests := []struct {
		name     string
		base     TPMConfig
		override TPMConfig
		expected TPMConfig
	}{
		{
			name: "override empty base",
			base: TPMConfig{},
			override: TPMConfig{
				Path:                "/dev/tpm0",
				PersistenceType:     TPMPersistenceTypeKeyBlob,
				PersistenceMetadata: "/tmp/tpm-blob",
			},
			expected: TPMConfig{
				Path:                "/dev/tpm0",
				PersistenceType:     TPMPersistenceTypeKeyBlob,
				PersistenceMetadata: "/tmp/tpm-blob",
			},
		},
		{
			name: "override existing values",
			base: TPMConfig{
				Path:                "/dev/tpm1",
				PersistenceType:     TPMPersistenceTypeAutoHandle,
				PersistenceMetadata: "/tmp/old-handle",
			},
			override: TPMConfig{
				PersistenceType:     TPMPersistenceTypeKeyBlob,
				PersistenceMetadata: "/tmp/new-blob",
			},
			expected: TPMConfig{
				Path:                "/dev/tpm1",
				PersistenceType:     TPMPersistenceTypeKeyBlob,
				PersistenceMetadata: "/tmp/new-blob",
			},
		},
		{
			name: "empty override should not change base",
			base: TPMConfig{
				Path:                "/dev/tpm0",
				PersistenceType:     TPMPersistenceTypeFixedHandle,
				PersistenceMetadata: "0x81000000",
			},
			override: TPMConfig{},
			expected: TPMConfig{
				Path:                "/dev/tpm0",
				PersistenceType:     TPMPersistenceTypeFixedHandle,
				PersistenceMetadata: "0x81000000",
			},
		},
		{
			name: "override with none type",
			base: TPMConfig{
				Path:                "/dev/tpm0",
				PersistenceType:     TPMPersistenceTypeKeyBlob,
				PersistenceMetadata: "/tmp/old-blob",
			},
			override: TPMConfig{
				PersistenceType: TPMPersistenceTypeNone,
			},
			expected: TPMConfig{
				Path:                "/dev/tpm0",
				PersistenceType:     TPMPersistenceTypeNone,
				PersistenceMetadata: "/tmp/old-blob", // Metadata is preserved but ignored for "none" type
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := tt.base
			base.MergeWith(&tt.override)
			require.Equal(t, tt.expected, base)
		})
	}
}
