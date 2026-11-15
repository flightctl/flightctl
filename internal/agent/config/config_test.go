package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
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
			name:             "When enrollment uses agent-api route hostname with no port it should use agent-remote-access with no port",
			enrollmentServer: "https://agent-api.example.com",
			want:             "https://agent-remote-access.example.com",
		},
		{
			name:             "When enrollment uses agent-api gateway hostname on a non-standard TLS port it should use agent-remote-access on the same port",
			enrollmentServer: "https://agent-api.example.com:8443",
			want:             "https://agent-remote-access.example.com:8443",
		},
		{
			name:             "When enrollment uses agent-api nodePort hostname on port 7443 it should use agent-remote-access on port 7444",
			enrollmentServer: "https://agent-api.127.0.0.1.nip.io:7443",
			want:             "https://agent-remote-access.127.0.0.1.nip.io:7444",
		},
		{
			name:             "When enrollment hostname does not have the agent-api prefix it should keep the hostname unchanged on port 7444",
			enrollmentServer: "https://enrollment.example.com:7443",
			want:             "https://enrollment.example.com:7444",
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

func TestPreEnrollmentFailurePolicy(t *testing.T) {
	require := require.New(t)

	t.Run("When no failurePolicy specified it should default to Continue", func(t *testing.T) {
		cfg := NewDefault()
		require.NoError(cfg.Complete())
		require.Equal("Continue", cfg.Enrollment.PreEnrollment.FailurePolicy)
	})

	t.Run("When failurePolicy is Block it should survive config load", func(t *testing.T) {
		tmpDir := t.TempDir()
		configDir := filepath.Join(tmpDir, "etc", "flightctl")
		dataDir := filepath.Join(tmpDir, "var", "lib", "flightctl")
		require.NoError(os.MkdirAll(configDir, 0o755))
		require.NoError(os.MkdirAll(dataDir, 0o755))

		configFile := filepath.Join(configDir, "config.yaml")
		content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
enrollment:
  preEnrollment:
    failurePolicy: Block
`
		require.NoError(os.WriteFile(configFile, []byte(content), 0o600))

		cfg := NewDefault()
		cfg.ConfigDir = configDir
		cfg.DataDir = dataDir
		cfg.readWriter = fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
		require.NoError(cfg.LoadWithOverrides(configFile))
		require.NoError(cfg.Complete())
		require.Equal("Block", cfg.Enrollment.PreEnrollment.FailurePolicy)
	})

	t.Run("When drop-in overrides failurePolicy it should take precedence", func(t *testing.T) {
		tmpDir := t.TempDir()
		configDir := filepath.Join(tmpDir, "etc", "flightctl")
		dataDir := filepath.Join(tmpDir, "var", "lib", "flightctl")
		require.NoError(os.MkdirAll(configDir, 0o755))
		require.NoError(os.MkdirAll(dataDir, 0o755))

		configFile := filepath.Join(configDir, "config.yaml")
		content := `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
status-update-interval: 0m10s
enrollment:
  preEnrollment:
    failurePolicy: Continue
`
		require.NoError(os.WriteFile(configFile, []byte(content), 0o600))

		dropinDir := filepath.Join(configDir, "conf.d")
		require.NoError(os.MkdirAll(dropinDir, 0o755))
		require.NoError(os.WriteFile(
			filepath.Join(dropinDir, "10-block.yaml"),
			[]byte("enrollment:\n  preEnrollment:\n    failurePolicy: Block\n"), 0o600))

		cfg := NewDefault()
		cfg.ConfigDir = configDir
		cfg.DataDir = dataDir
		cfg.readWriter = fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
		require.NoError(cfg.LoadWithOverrides(configFile))
		require.NoError(cfg.Complete())
		require.Equal("Block", cfg.Enrollment.PreEnrollment.FailurePolicy)
	})

	t.Run("When failurePolicy is invalid it should fail Complete", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Enrollment.PreEnrollment.FailurePolicy = "block"
		err := cfg.Complete()
		require.Error(err)
		require.Contains(err.Error(), "failurePolicy")
	})
}

func TestSystemInfoCollectionInterval(t *testing.T) {
	t.Run("When no drop-ins exist and system-info-periodic interval is below the minimum it should fail loading", func(t *testing.T) {
		require := require.New(t)

		configDir := t.TempDir()
		dataDir := filepath.Join(configDir, "data")
		readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
		require.NoError(readWriter.MkdirAll(dataDir, 0o755))
		configFile := filepath.Join(configDir, "config.yaml")
		require.NoError(readWriter.WriteFile(configFile, []byte(yamlConfig+`
system-info-periodic:
  interval: 1s
`), 0o600))

		cfg := NewDefault()
		cfg.ConfigDir = configDir
		cfg.DataDir = dataDir
		cfg.readWriter = readWriter
		err := cfg.LoadWithOverrides(configFile)
		require.ErrorContains(err, "minimum system info periodic interval is 2s have 1s")
	})

	t.Run("When a drop-in sets system-info-periodic interval it should override the base interval", func(t *testing.T) {
		require := require.New(t)

		configDir := t.TempDir()
		dataDir := filepath.Join(configDir, "data")
		readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
		require.NoError(readWriter.MkdirAll(dataDir, 0o755))
		configFile := filepath.Join(configDir, "config.yaml")
		require.NoError(readWriter.WriteFile(configFile, []byte(yamlConfig+`
system-info-periodic:
  interval: 1m
`), 0o600))

		dropinDir := filepath.Join(configDir, "conf.d")
		require.NoError(readWriter.MkdirAll(dropinDir, 0o755))
		require.NoError(readWriter.WriteFile(filepath.Join(dropinDir, "10-system-info-periodic.yaml"), []byte("system-info-periodic:\n  interval: 5m\n"), 0o600))

		cfg := NewDefault()
		cfg.ConfigDir = configDir
		cfg.DataDir = dataDir
		cfg.readWriter = readWriter
		require.NoError(cfg.LoadWithOverrides(configFile))
		require.Equal(util.Duration(5*time.Minute), cfg.SystemInfoCollectionInterval())
	})

	t.Run("When system-info-periodic interval is set it should use that interval", func(t *testing.T) {
		require := require.New(t)

		cfg := NewDefault()
		customInterval := util.Duration(5 * time.Minute)
		cfg.SystemInfoPeriodic.Interval = customInterval

		result := cfg.SystemInfoCollectionInterval()
		require.Equal(customInterval, result)
	})

	t.Run("When system-info-periodic interval is zero it should fall back to StatusUpdateInterval", func(t *testing.T) {
		require := require.New(t)

		cfg := NewDefault()
		// SystemInfoPeriodic.Interval is zero by default

		result := cfg.SystemInfoCollectionInterval()
		require.Equal(cfg.StatusUpdateInterval, result)
	})

	t.Run("When system-info-periodic interval is absent it should fall back to StatusUpdateInterval", func(t *testing.T) {
		require := require.New(t)

		cfg := NewDefault()
		cfg.StatusUpdateInterval = util.Duration(30 * time.Second)

		result := cfg.SystemInfoCollectionInterval()
		require.Equal(util.Duration(30*time.Second), result)
	})

	t.Run("When system-info-periodic interval is below the minimum it should fail validation", func(t *testing.T) {
		require := require.New(t)

		cfg := NewDefault()
		cfg.SystemInfoPeriodic.Interval = util.Duration(time.Second)
		err := cfg.validateSyncIntervals()
		require.ErrorContains(err, "minimum system info periodic interval is 2s have 1s")
	})

	t.Run("When system-info-periodic interval is negative it should fail validation", func(t *testing.T) {
		require := require.New(t)

		cfg := NewDefault()
		cfg.SystemInfoPeriodic.Interval = util.Duration(-time.Minute)
		err := cfg.validateSyncIntervals()
		require.ErrorContains(err, "minimum system info periodic interval is 2s have -1m0s")
	})

	t.Run("When system-info-periodic interval is zero no validation should occur", func(t *testing.T) {
		require := require.New(t)

		cfg := NewDefault()
		cfg.SystemInfoPeriodic.Interval = util.Duration(0)
		err := cfg.validateSyncIntervals()
		require.NoError(err)
	})
}

func TestSystemInfoCustomYAMLDeserialization(t *testing.T) {
	tests := []struct {
		name          string
		yamlInput     string
		expectNil     bool
		expectedLen   int
		expectedValue []string
	}{
		{
			name:      "When system-info-custom is absent in YAML it should deserialize as nil",
			yamlInput: "log-level: info\n",
			expectNil: true,
		},
		{
			name:        "When system-info-custom is empty list it should deserialize as non-nil with len 0",
			yamlInput:   "system-info-custom: []\n",
			expectNil:   false,
			expectedLen: 0,
		},
		{
			name:          "When system-info-custom has an explicit key it should deserialize with that key",
			yamlInput:     "system-info-custom:\n  - my-key\n",
			expectNil:     false,
			expectedLen:   1,
			expectedValue: []string{"my-key"},
		},
		{
			name:          "When system-info-custom has wildcard it should deserialize as ['*']",
			yamlInput:     "system-info-custom:\n  - '*'\n",
			expectNil:     false,
			expectedLen:   1,
			expectedValue: []string{"*"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			var cfg Config
			err := yaml.Unmarshal([]byte(tt.yamlInput), &cfg)
			require.NoError(err)

			if tt.expectNil {
				require.Nil(cfg.SystemInfoCustom, "expected SystemInfoCustom to be nil")
				return
			}

			require.NotNil(cfg.SystemInfoCustom, "expected SystemInfoCustom to be non-nil")
			require.Len(cfg.SystemInfoCustom, tt.expectedLen)

			if tt.expectedValue != nil {
				require.Equal(tt.expectedValue, cfg.SystemInfoCustom)
			}
		})
	}
}

const firmwareAccessExample = `firmware:
  updater-access:
    prepare:
      network-repositories:
        - vendor-firmware
      credential-repositories:
        - vendor-firmware
      read-paths:
        - /etc/vendor
      read-devices:
        - /dev/mtd0
      workspace-write: true
    apply:
      read-paths:
        - /etc/vendor
      write-paths:
        - /opt/vendor
      read-devices:
        - /dev/mtd0
      write-devices:
        - /dev/mtd0
      workspace-write: true
    verify:
      read-paths:
        - /etc/vendor
        - /opt/vendor
      read-devices:
        - /dev/mtd0
`

const narrowerFirmwareAccess = `firmware:
  updater-access:
    verify:
      read-paths:
        - /etc/vendor
`

var exampleUpdaterAccess = UpdaterAccessPolicy{
	Prepare: UpdaterAccessGrant{
		NetworkRepositories:    []string{"vendor-firmware"},
		CredentialRepositories: []string{"vendor-firmware"},
		ReadPaths:              []string{"/etc/vendor"},
		ReadDevices:            []string{"/dev/mtd0"},
		WorkspaceWrite:         true,
	},
	Apply: UpdaterAccessGrant{
		ReadPaths:      []string{"/etc/vendor"},
		WritePaths:     []string{"/opt/vendor"},
		ReadDevices:    []string{"/dev/mtd0"},
		WriteDevices:   []string{"/dev/mtd0"},
		WorkspaceWrite: true,
	},
	Verify: UpdaterAccessGrant{
		ReadPaths:   []string{"/etc/vendor", "/opt/vendor"},
		ReadDevices: []string{"/dev/mtd0"},
	},
}

var narrowerUpdaterAccess = UpdaterAccessPolicy{Verify: UpdaterAccessGrant{ReadPaths: []string{"/etc/vendor"}}}

// firmwareGrant renders a firmware section granting entries to one phase field.
func firmwareGrant(phase, field string, entries ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "firmware:\n  updater-access:\n    %s:\n      %s:\n", phase, field)
	for _, e := range entries {
		fmt.Fprintf(&b, "        - %s\n", e)
	}
	return b.String()
}

// loadConfigLayers writes the main config.yaml and the ordered conf.d drop-ins
// beneath a test root and loads them through the public Load.
func loadConfigLayers(t *testing.T, main string, dropins ...string) *Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv(TestRootDirEnvKey, root)
	configDir := filepath.Join(root, DefaultConfigDir)
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(main), 0o600))
	if len(dropins) > 0 {
		dropinDir := filepath.Join(configDir, "conf.d")
		require.NoError(t, os.MkdirAll(dropinDir, 0o755))
		for i, content := range dropins {
			require.NoError(t, os.WriteFile(filepath.Join(dropinDir, fmt.Sprintf("%02d-dropin.yaml", i+1)), []byte(content), 0o600))
		}
	}
	cfg, err := Load(DefaultConfigFile)
	require.NoError(t, err)
	return cfg
}

func firmwareWarnings(cfg *Config) []string {
	var warnings []string
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "firmware.updater-access") {
			warnings = append(warnings, w)
		}
	}
	return warnings
}

func TestFirmwareUpdaterAccessPolicy(t *testing.T) {
	base := yamlConfig + "\n"
	withExample := base + firmwareAccessExample

	type layers struct {
		main    string
		dropins []string
	}
	testCases := []struct {
		name             string
		layers           layers
		wantPolicy       UpdaterAccessPolicy
		wantFirmwareWarn int
		wantOtherWarn    int
		check            func(t *testing.T, cfg *Config)
	}{
		{name: "When no firmware section exists it should deny all access without a warning", layers: layers{main: base}},
		{name: "When the firmware section is valid it should parse every phase", layers: layers{main: withExample}, wantPolicy: exampleUpdaterAccess},
		{name: "When apply grants network access it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("apply", "network-repositories", "vendor-firmware")}, wantFirmwareWarn: 1},
		{name: "When verify grants network access it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("verify", "network-repositories", "vendor-firmware")}, wantFirmwareWarn: 1},
		{name: "When apply grants source credentials it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("apply", "credential-repositories", "vendor-firmware")}, wantFirmwareWarn: 1},
		{name: "When verify grants source credentials it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("verify", "credential-repositories", "vendor-firmware")}, wantFirmwareWarn: 1},
		{name: "When prepare grants host path writes it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("prepare", "write-paths", "/opt/vendor")}, wantFirmwareWarn: 1},
		{name: "When prepare grants device writes it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("prepare", "write-devices", "/dev/mtd0")}, wantFirmwareWarn: 1},
		{name: "When verify grants host path writes it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("verify", "write-paths", "/opt/vendor")}, wantFirmwareWarn: 1},
		{name: "When verify grants device writes it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("verify", "write-devices", "/dev/mtd0")}, wantFirmwareWarn: 1},
		{name: "When verify grants workspace writes it should warn and deny all access",
			layers: layers{main: base + "firmware:\n  updater-access:\n    verify:\n      workspace-write: true\n"}, wantFirmwareWarn: 1},
		{name: "When a credential has no matching network grant it should warn and deny all access",
			layers: layers{main: base + "firmware:\n  updater-access:\n    prepare:\n      network-repositories: [vendor-firmware]\n      credential-repositories: [other]\n"}, wantFirmwareWarn: 1},
		{name: "When a list repeats an entry it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("verify", "read-paths", "/etc/vendor", "/etc/vendor")}, wantFirmwareWarn: 1},
		{name: "When the access section has an unknown phase it should warn and deny all access",
			layers: layers{main: base + "firmware:\n  updater-access:\n    install:\n      read-paths: [/etc/vendor]\n"}, wantFirmwareWarn: 1},
		{name: "When a grant has an unknown key it should warn and deny all access",
			layers: layers{main: base + firmwareGrant("prepare", "network-repository", "vendor-firmware")}, wantFirmwareWarn: 1},
		{name: "When workspace-write is a string it should warn, deny all access and keep other settings",
			layers: layers{main: base + "log-level: debug\nfirmware:\n  updater-access:\n    prepare:\n      workspace-write: \"yes\"\n"}, wantFirmwareWarn: 1,
			check: func(t *testing.T, cfg *Config) { require.Equal(t, "debug", cfg.LogLevel) }},
		{name: "When read-paths is a scalar it should warn, deny all access and keep other settings",
			layers: layers{main: "firmware:\n  updater-access:\n    verify:\n      read-paths: /opt/vendor\nlog-level: debug\n" + base}, wantFirmwareWarn: 1,
			check: func(t *testing.T, cfg *Config) {
				require.Equal(t, "debug", cfg.LogLevel)
				require.Equal(t, "https://management.endpoint", cfg.ManagementService.Service.Server)
			}},
		{name: "When a later drop-in sets a narrower policy it should replace the earlier section entirely",
			layers: layers{main: withExample, dropins: []string{narrowerFirmwareAccess}}, wantPolicy: narrowerUpdaterAccess},
		{name: "When a drop-in is skipped it should deny all access and warn alongside the skip warning",
			layers: layers{main: withExample, dropins: []string{"{{{not yaml at all\n"}}, wantFirmwareWarn: 1, wantOtherWarn: 1},
		{name: "When the main firmware section is an array it should load, warn, deny all access and keep other settings",
			layers: layers{main: base + "log-level: debug\nfirmware: []\n"}, wantFirmwareWarn: 1,
			check: func(t *testing.T, cfg *Config) {
				require.Equal(t, "debug", cfg.LogLevel)
				require.Equal(t, "https://enrollment.endpoint", cfg.EnrollmentService.Service.Server)
			}},
		{name: "When a drop-in sets firmware to null it should drop inherited grants without a warning",
			layers: layers{main: withExample, dropins: []string{"firmware: null\n"}}},
		{name: "When a drop-in sets firmware without a value it should drop inherited grants without a warning",
			layers: layers{main: withExample, dropins: []string{"firmware:\n"}}},
		{name: "When a drop-in sets firmware to an empty object it should drop inherited grants without a warning",
			layers: layers{main: withExample, dropins: []string{"firmware: {}\n"}}},
		{name: "When a drop-in sets updater-access to null it should drop inherited grants without a warning",
			layers: layers{main: withExample, dropins: []string{"firmware:\n  updater-access: null\n"}}},
		{name: "When a drop-in sets updater-access to an empty object it should drop inherited grants without a warning",
			layers: layers{main: withExample, dropins: []string{"firmware:\n  updater-access: {}\n"}}},
		{name: "When an invalid firmware drop-in is followed by an unrelated drop-in it should keep denying access and apply both settings",
			layers: layers{main: withExample, dropins: []string{"log-level: debug\nfirmware: []\n", "metrics-enabled: true\n"}}, wantFirmwareWarn: 1,
			check: func(t *testing.T, cfg *Config) {
				require.Equal(t, "debug", cfg.LogLevel)
				require.True(t, cfg.MetricsEnabled)
			}},
		{name: "When an invalid firmware drop-in is replaced by a later valid one it should apply the later policy and keep the warning",
			layers: layers{main: withExample, dropins: []string{"firmware: invalid\n", narrowerFirmwareAccess}}, wantPolicy: narrowerUpdaterAccess, wantFirmwareWarn: 1},
		{name: "When a drop-in does not set firmware it should keep the inherited policy",
			layers: layers{main: withExample, dropins: []string{"log-level: debug\n"}}, wantPolicy: exampleUpdaterAccess,
			check: func(t *testing.T, cfg *Config) { require.Equal(t, "debug", cfg.LogLevel) }},
		{name: "When a repository entry is an unquoted number it should warn and deny all access",
			layers: layers{main: base + "firmware:\n  updater-access:\n    prepare:\n      network-repositories: [123]\n"}, wantFirmwareWarn: 1},
		{name: "When a repository entry is a YAML boolean it should warn and deny all access",
			layers: layers{main: base + "firmware:\n  updater-access:\n    prepare:\n      network-repositories: [on]\n"}, wantFirmwareWarn: 1},
		{name: "When a numeric repository name is quoted it should be accepted",
			layers:     layers{main: base + "firmware:\n  updater-access:\n    prepare:\n      network-repositories: [\"123\"]\n"},
			wantPolicy: UpdaterAccessPolicy{Prepare: UpdaterAccessGrant{NetworkRepositories: []string{"123"}}}},
	}
	for _, scalar := range []string{"invalid", "5", "true"} {
		testCases = append(testCases, struct {
			name             string
			layers           layers
			wantPolicy       UpdaterAccessPolicy
			wantFirmwareWarn int
			wantOtherWarn    int
			check            func(t *testing.T, cfg *Config)
		}{
			name:   fmt.Sprintf("When the main firmware section is the scalar %s it should load, warn, deny all access and keep other settings", scalar),
			layers: layers{main: base + "log-level: debug\nfirmware: " + scalar + "\n"}, wantFirmwareWarn: 1,
			check: func(t *testing.T, cfg *Config) { require.Equal(t, "debug", cfg.LogLevel) },
		})
	}
	for _, section := range []string{"firmware: {updater-access: []}", "firmware: {updater-access: invalid}", "firmware: {unknown: 1}"} {
		testCases = append(testCases, struct {
			name             string
			layers           layers
			wantPolicy       UpdaterAccessPolicy
			wantFirmwareWarn int
			wantOtherWarn    int
			check            func(t *testing.T, cfg *Config)
		}{
			name:   fmt.Sprintf("When the main config has %q it should load, warn, deny all access and keep other settings", section),
			layers: layers{main: base + "log-level: debug\n" + section + "\n"}, wantFirmwareWarn: 1,
			check: func(t *testing.T, cfg *Config) { require.Equal(t, "debug", cfg.LogLevel) },
		})
	}
	for _, repo := range []string{"quay.io/vendor/fw", "https://quay.io/vendor", "vendor:1.0", "vendor@sha256:" + strings.Repeat("a", 64), "-vendor", ".vendor", "a" + strings.Repeat("b", 128)} {
		testCases = append(testCases, struct {
			name             string
			layers           layers
			wantPolicy       UpdaterAccessPolicy
			wantFirmwareWarn int
			wantOtherWarn    int
			check            func(t *testing.T, cfg *Config)
		}{
			name:   fmt.Sprintf("When repository %q is not a logical name it should warn and deny all access", repo),
			layers: layers{main: base + firmwareGrant("prepare", "network-repositories", strconv.Quote(repo))}, wantFirmwareWarn: 1,
		})
	}
	for _, p := range []string{"opt/vendor", "/", "/opt/vendor/", "/opt//vendor", "/opt/./vendor", "/opt/vendor/..", "/opt/*", "/opt/vendor?", "/opt/[ab]", "/opt/a\x00b", "/opt/a\rb", "/opt/a\nb"} {
		testCases = append(testCases, struct {
			name             string
			layers           layers
			wantPolicy       UpdaterAccessPolicy
			wantFirmwareWarn int
			wantOtherWarn    int
			check            func(t *testing.T, cfg *Config)
		}{
			name:   fmt.Sprintf("When path %q is not an explicit clean absolute path it should warn and deny all access", p),
			layers: layers{main: base + firmwareGrant("verify", "read-paths", strconv.Quote(p))}, wantFirmwareWarn: 1,
		})
	}
	for _, device := range []string{"/sys/class/mtd/mtd0", "/devx/mtd0", "/dev/../mtd0"} {
		testCases = append(testCases, struct {
			name             string
			layers           layers
			wantPolicy       UpdaterAccessPolicy
			wantFirmwareWarn int
			wantOtherWarn    int
			check            func(t *testing.T, cfg *Config)
		}{
			name:   fmt.Sprintf("When device %q is not a path under /dev it should warn and deny all access", device),
			layers: layers{main: base + firmwareGrant("prepare", "read-devices", strconv.Quote(device))}, wantFirmwareWarn: 1,
		})
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			cfg := loadConfigLayers(t, tc.layers.main, tc.layers.dropins...)
			require.Equal(tc.wantPolicy, cfg.Firmware.UpdaterAccess)
			require.Len(firmwareWarnings(cfg), tc.wantFirmwareWarn, "warnings: %v", cfg.Warnings)
			require.Len(cfg.Warnings, tc.wantFirmwareWarn+tc.wantOtherWarn, "warnings: %v", cfg.Warnings)
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

func TestFirmwareSectionDoesNotRecoverMalformedMainYAML(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	t.Setenv(TestRootDirEnvKey, root)
	configDir := filepath.Join(root, DefaultConfigDir)
	require.NoError(os.MkdirAll(configDir, 0o755))
	require.NoError(os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yamlConfig+"\nfirmware: [\n"), 0o600))
	cfg, err := Load(DefaultConfigFile)
	require.Error(err)
	require.Nil(cfg)
}

func TestFirmwareSectionDoesNotRecoverOtherSectionsInDropins(t *testing.T) {
	require := require.New(t)
	cfg := loadConfigLayers(t, yamlConfig+"\n"+firmwareAccessExample, "log-level: [debug]\n")
	require.Equal(UpdaterAccessPolicy{}, cfg.Firmware.UpdaterAccess)
	require.Equal(logrus.InfoLevel.String(), cfg.LogLevel)
	require.Len(firmwareWarnings(cfg), 1, "warnings: %v", cfg.Warnings)
	require.Len(cfg.Warnings, 2, "warnings: %v", cfg.Warnings)
}

func TestFirmwareSectionAppearsInLoggedConfigOnlyWhenConfigured(t *testing.T) {
	require := require.New(t)
	unmanaged := loadConfigLayers(t, yamlConfig)
	require.NotContains(unmanaged.StringSanitized(), "firmware")

	configured := loadConfigLayers(t, yamlConfig+"\n"+narrowerFirmwareAccess)
	require.Contains(configured.StringSanitized(), `"firmware":{"updater-access":`)
}

func TestUpdaterAccessPolicyDecodingNeverFails(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  UpdaterAccessPolicy
	}{
		{"When the policy is an object it should decode each phase", `{"prepare":{"workspace-write":true},"verify":{"read-paths":["/etc/vendor"]}}`,
			UpdaterAccessPolicy{Prepare: UpdaterAccessGrant{WorkspaceWrite: true}, Verify: UpdaterAccessGrant{ReadPaths: []string{"/etc/vendor"}}}},
		{"When the policy is null it should decode to an empty policy", `null`, UpdaterAccessPolicy{}},
		{"When the policy has the wrong type it should decode to an empty policy without failing", `["prepare"]`, UpdaterAccessPolicy{}},
		{"When the policy has an unknown phase it should decode to an empty policy without failing", `{"install":{"read-paths":["/etc"]}}`, UpdaterAccessPolicy{}},
		{"When an entry has the wrong type it should decode to an empty policy without failing", `{"prepare":{"read-paths":"/etc/vendor"}}`, UpdaterAccessPolicy{}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			var p UpdaterAccessPolicy
			require.NoError(json.Unmarshal([]byte(tc.input), &p))
			require.Equal(tc.want.Prepare, p.Prepare)
			require.Equal(tc.want.Apply, p.Apply)
			require.Equal(tc.want.Verify, p.Verify)
		})
	}
}
