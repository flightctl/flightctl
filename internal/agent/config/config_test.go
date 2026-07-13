package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
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

func TestDeriveRemoteAccessServer(t *testing.T) {
	tests := []struct {
		name             string
		enrollmentServer string
		want             string
		wantErr          bool
	}{
		{
			name:             "When enrollment uses agent-api route hostname it should use agent-remote-access on port 7444",
			enrollmentServer: "https://agent-api.example.com",
			want:             "https://agent-remote-access.example.com:7444",
		},
		{
			name:             "When enrollment uses agent-api nodePort hostname it should use agent-remote-access on port 7444",
			enrollmentServer: "https://agent-api.127.0.0.1.nip.io:7443",
			want:             "https://agent-remote-access.127.0.0.1.nip.io:7444",
		},
		{
			name:             "When enrollment URL is invalid it should return an error",
			enrollmentServer: "://bad-url",
			wantErr:          true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			got, err := deriveRemoteAccessServer(tt.enrollmentServer)
			if tt.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
			require.Equal(tt.want, got)
		})
	}
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

func TestLoadPruningFromConfig(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name            string
		setupFiles      func(t *testing.T, configDir string) string
		expectedEnabled bool
		wantErr         bool
	}{
		{
			name: "no config files - uses default",
			setupFiles: func(t *testing.T, configDir string) string {
				configFile := filepath.Join(configDir, "config.yaml")
				content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
`
				require.NoError(os.WriteFile(configFile, []byte(content), 0o600))
				return configFile
			},
			expectedEnabled: false, // Default from NewDefault() - nil pointer means disabled
		},
		{
			name: "base config file enables pruning",
			setupFiles: func(t *testing.T, configDir string) string {
				configFile := filepath.Join(configDir, "config.yaml")
				content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
image-pruning:
  enabled: true
`
				require.NoError(os.WriteFile(configFile, []byte(content), 0o600))
				return configFile
			},
			expectedEnabled: true,
		},
		{
			name: "base config file disables pruning",
			setupFiles: func(t *testing.T, configDir string) string {
				configFile := filepath.Join(configDir, "config.yaml")
				content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
image-pruning:
  enabled: false
`
				require.NoError(os.WriteFile(configFile, []byte(content), 0o600))
				return configFile
			},
			expectedEnabled: false,
		},
		{
			name: "dropin overrides base config file",
			setupFiles: func(t *testing.T, configDir string) string {
				// Base file enables
				configFile := filepath.Join(configDir, "config.yaml")
				content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
image-pruning:
  enabled: true
`
				require.NoError(os.WriteFile(configFile, []byte(content), 0o600))
				// Dropin disables
				dropinDir := filepath.Join(configDir, "conf.d")
				require.NoError(os.MkdirAll(dropinDir, 0o755))
				dropinPath := filepath.Join(dropinDir, "01-disable.yaml")
				require.NoError(os.WriteFile(dropinPath, []byte("image-pruning:\n  enabled: false\n"), 0o600))
				return configFile
			},
			expectedEnabled: false,
		},
		{
			name: "multiple dropins - later overrides earlier",
			setupFiles: func(t *testing.T, configDir string) string {
				configFile := filepath.Join(configDir, "config.yaml")
				content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
`
				require.NoError(os.WriteFile(configFile, []byte(content), 0o600))
				dropinDir := filepath.Join(configDir, "conf.d")
				require.NoError(os.MkdirAll(dropinDir, 0o755))
				// First dropin enables
				require.NoError(os.WriteFile(filepath.Join(dropinDir, "01-enable.yaml"), []byte("image-pruning:\n  enabled: true\n"), 0o600))
				// Second dropin disables (should win)
				require.NoError(os.WriteFile(filepath.Join(dropinDir, "02-disable.yaml"), []byte("image-pruning:\n  enabled: false\n"), 0o600))
				return configFile
			},
			expectedEnabled: false,
		},
		{
			name: "invalid yaml in base config file",
			setupFiles: func(t *testing.T, configDir string) string {
				configFile := filepath.Join(configDir, "config.yaml")
				require.NoError(os.WriteFile(configFile, []byte("invalid: yaml: content: [\n"), 0o600))
				return configFile
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configDir := filepath.Join(tmpDir, "etc", "flightctl")
			dataDir := filepath.Join(tmpDir, "var", "lib", "flightctl")

			// Create necessary directories
			require.NoError(os.MkdirAll(configDir, 0o755))
			require.NoError(os.MkdirAll(dataDir, 0o755))

			cfg := NewDefault()
			cfg.ConfigDir = configDir
			cfg.DataDir = dataDir
			cfg.readWriter = fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())

			configFile := tt.setupFiles(t, configDir)

			err := cfg.LoadWithOverrides(configFile)
			if tt.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
			if tt.expectedEnabled {
				require.NotNil(cfg.ImagePruning.Enabled, "Enabled should not be nil when expected to be true")
				require.True(*cfg.ImagePruning.Enabled, "Enabled should be true")
			} else {
				if cfg.ImagePruning.Enabled != nil {
					require.False(*cfg.ImagePruning.Enabled, "Enabled should be false when not nil")
				}
			}
		})
	}
}

func TestLoadWithOverridesIncludesPruningFromConfD(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "etc", "flightctl")
	dataDir := filepath.Join(tmpDir, "var", "lib", "flightctl")

	// Create necessary directories
	require.NoError(os.MkdirAll(configDir, 0o755))
	require.NoError(os.MkdirAll(dataDir, 0o755))

	cfg := NewDefault()
	cfg.ConfigDir = configDir
	cfg.DataDir = dataDir
	cfg.readWriter = fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())

	// Create base config file with pruning disabled
	configFile := filepath.Join(configDir, "config.yaml")
	content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
image-pruning:
  enabled: false
`
	require.NoError(os.WriteFile(configFile, []byte(content), 0o600))

	// Create dropin in conf.d that enables pruning (should override config)
	dropinDir := filepath.Join(configDir, "conf.d")
	require.NoError(os.MkdirAll(dropinDir, 0o755))
	dropinPath := filepath.Join(dropinDir, "enable.yaml")
	require.NoError(os.WriteFile(dropinPath, []byte("image-pruning:\n  enabled: true\n"), 0o600))

	// Load config with overrides
	err := cfg.LoadWithOverrides(configFile)
	require.NoError(err)
	// Dropin should override config, so pruning should be enabled
	require.NotNil(cfg.ImagePruning.Enabled, "Enabled should not be nil when dropin enables pruning")
	require.True(*cfg.ImagePruning.Enabled, "pruning dropin should override config setting")
}

func TestLoadWithOverrides_DropinErrorHandling(t *testing.T) {
	tests := []struct {
		name             string
		baseConfig       string
		setupDropins     func(t *testing.T, dropinDir string)
		wantErr          bool
		skipOnRoot       bool
		expectedLogLevel string
		expectedWarnings int
		warningContains  string
	}{
		{
			name:       "When an invalid dropin exists it should skip it and apply valid ones",
			baseConfig: yamlConfig,
			setupDropins: func(t *testing.T, dropinDir string) {
				require.NoError(t, os.WriteFile(
					filepath.Join(dropinDir, "01-bad.yaml"),
					[]byte("image-pruning:\n\t enabled: true\n"),
					0o600,
				))
				require.NoError(t, os.WriteFile(
					filepath.Join(dropinDir, "02-good.yaml"),
					[]byte("log-level: debug\n"),
					0o600,
				))
			},
			expectedLogLevel: "debug",
			expectedWarnings: 1,
			warningContains:  "01-bad.yaml",
		},
		{
			name:       "When the base config is invalid it should return an error",
			baseConfig: "invalid: yaml: [\n",
			wantErr:    true,
		},
		{
			name:       "When only invalid dropins exist it should still succeed with base config",
			baseConfig: yamlConfig,
			setupDropins: func(t *testing.T, dropinDir string) {
				require.NoError(t, os.WriteFile(
					filepath.Join(dropinDir, "01-bad.yaml"),
					[]byte("image-pruning:\n\t enabled: true\n"),
					0o600,
				))
				require.NoError(t, os.WriteFile(
					filepath.Join(dropinDir, "02-also-bad.yaml"),
					[]byte("{{{not yaml at all\n"),
					0o600,
				))
			},
			expectedWarnings: 2,
		},
		{
			name:       "When an unreadable dropin exists it should skip it and apply valid ones",
			baseConfig: yamlConfig,
			skipOnRoot: true,
			setupDropins: func(t *testing.T, dropinDir string) {
				require.NoError(t, os.WriteFile(
					filepath.Join(dropinDir, "01-unreadable.yaml"),
					[]byte("log-level: debug\n"),
					0o000,
				))
				require.NoError(t, os.WriteFile(
					filepath.Join(dropinDir, "02-good.yaml"),
					[]byte("log-level: warn\n"),
					0o600,
				))
			},
			expectedLogLevel: "warn",
			expectedWarnings: 1,
			warningContains:  "01-unreadable.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipOnRoot && os.Getuid() == 0 {
				t.Skip("permission test is not meaningful when running as root")
			}

			require := require.New(t)
			tmpDir := t.TempDir()
			configDir := filepath.Join(tmpDir, "etc", "flightctl")
			dataDir := filepath.Join(tmpDir, "var", "lib", "flightctl")

			require.NoError(os.MkdirAll(configDir, 0o755))
			require.NoError(os.MkdirAll(dataDir, 0o755))

			cfg := NewDefault()
			cfg.ConfigDir = configDir
			cfg.DataDir = dataDir
			cfg.readWriter = fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())

			configFile := filepath.Join(configDir, "config.yaml")
			require.NoError(os.WriteFile(configFile, []byte(tt.baseConfig), 0o600))

			if tt.setupDropins != nil {
				dropinDir := filepath.Join(configDir, "conf.d")
				require.NoError(os.MkdirAll(dropinDir, 0o755))
				tt.setupDropins(t, dropinDir)
			}

			err := cfg.LoadWithOverrides(configFile)
			if tt.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)

			if tt.expectedLogLevel != "" {
				require.Equal(tt.expectedLogLevel, cfg.LogLevel)
			}
			require.Len(cfg.Warnings, tt.expectedWarnings)
			if tt.warningContains != "" {
				require.Contains(cfg.Warnings[0], tt.warningContains)
			}
		})
	}
}
