package periodic

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/stretchr/testify/require"
)

func TestMergeTasksWithConfig(t *testing.T) {
	defaultResourceSyncInterval := periodicTasks[PeriodicTaskTypeResourceSync].Interval

	tests := []struct {
		name                         string
		configJSON                   string
		expectedResourceSyncInterval time.Duration
	}{
		{
			name:                         "nil config returns defaults",
			configJSON:                   "",
			expectedResourceSyncInterval: defaultResourceSyncInterval,
		},
		{
			name:                         "empty config returns defaults",
			configJSON:                   `{}`,
			expectedResourceSyncInterval: defaultResourceSyncInterval,
		},
		{
			name:                         "nil periodic config returns defaults",
			configJSON:                   `{"periodic": null}`,
			expectedResourceSyncInterval: defaultResourceSyncInterval,
		},
		{
			name: "zero interval returns defaults",
			configJSON: `{
				"periodic": {
					"tasks": {
						"resourceSync": {
							"schedule": {
								"interval": "0s"
							}
						}
					}
				}
			}`,
			expectedResourceSyncInterval: defaultResourceSyncInterval,
		},
		{
			name: "custom interval overrides default",
			configJSON: `{
				"periodic": {
					"tasks": {
						"resourceSync": {
							"schedule": {
								"interval": "7m"
							}
						}
					}
				}
			}`,
			expectedResourceSyncInterval: 7 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg *config.Config
			if tt.configJSON != "" {
				cfg = &config.Config{}
				err := json.Unmarshal([]byte(tt.configJSON), cfg)
				require.NoError(t, err)
			}

			result := MergeTasksWithConfig(cfg)

			require.NotNil(t, result)
			require.Contains(t, result, PeriodicTaskTypeResourceSync)
			require.Equal(t, tt.expectedResourceSyncInterval, result[PeriodicTaskTypeResourceSync].Interval)

			for taskType, defaultMeta := range periodicTasks {
				if taskType == PeriodicTaskTypeVulnerabilitySync {
					require.NotContains(t, result, taskType)
					continue
				}
				require.Contains(t, result, taskType)
				if taskType != PeriodicTaskTypeResourceSync {
					require.Equal(t, defaultMeta.Interval, result[taskType].Interval)
				}
			}
		})
	}
}

func TestMergeTasksWithConfig_DependencySync(t *testing.T) {
	defaultDependencySyncInterval := periodicTasks[PeriodicTaskTypeDependencySyncGit].Interval

	tests := []struct {
		name             string
		configJSON       string
		expectedInterval time.Duration
	}{
		{
			name:             "When no override is configured it should retain the default interval",
			configJSON:       `{}`,
			expectedInterval: defaultDependencySyncInterval,
		},
		{
			name: "When interval is zero it should retain the default interval",
			configJSON: `{
				"periodic": {
					"tasks": {
						"dependencySync": {
							"schedule": {
								"interval": "0s"
							}
						}
					}
				}
			}`,
			expectedInterval: defaultDependencySyncInterval,
		},
		{
			name: "When a custom interval is configured it should apply to both dependency-sync-git and dependency-sync-http",
			configJSON: `{
				"periodic": {
					"tasks": {
						"dependencySync": {
							"schedule": {
								"interval": "5s"
							}
						}
					}
				}
			}`,
			expectedInterval: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			require.NoError(t, json.Unmarshal([]byte(tt.configJSON), cfg))

			result := MergeTasksWithConfig(cfg)

			require.Contains(t, result, PeriodicTaskTypeDependencySyncGit)
			require.Contains(t, result, PeriodicTaskTypeDependencySyncHttp)
			require.Equal(t, tt.expectedInterval, result[PeriodicTaskTypeDependencySyncGit].Interval)
			require.Equal(t, tt.expectedInterval, result[PeriodicTaskTypeDependencySyncHttp].Interval)
		})
	}
}

func TestMergeTasksWithConfig_VulnerabilitySync(t *testing.T) {
	tests := []struct {
		name       string
		configJSON string
		expectTask bool
	}{
		{
			name:       "When config is nil it should exclude vulnerability-sync",
			configJSON: "",
			expectTask: false,
		},
		{
			name:       "When VulnerabilityReporting is nil it should exclude vulnerability-sync",
			configJSON: `{}`,
			expectTask: false,
		},
		{
			name:       "When VulnerabilityReporting is disabled it should exclude vulnerability-sync",
			configJSON: `{"vulnerabilityReporting": {"enabled": false}}`,
			expectTask: false,
		},
		{
			name:       "When VulnerabilityReporting is enabled it should include vulnerability-sync with default interval",
			configJSON: `{"vulnerabilityReporting": {"enabled": true}}`,
			expectTask: true,
		},
		{
			name:       "When VulnerabilityReporting is enabled with custom interval it should use custom interval",
			configJSON: `{"vulnerabilityReporting": {"enabled": true, "syncInterval": "30m"}}`,
			expectTask: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg *config.Config
			if tt.configJSON != "" {
				cfg = &config.Config{}
				err := json.Unmarshal([]byte(tt.configJSON), cfg)
				require.NoError(t, err)
			}

			result := MergeTasksWithConfig(cfg)

			if tt.expectTask {
				require.Contains(t, result, PeriodicTaskTypeVulnerabilitySync)
			} else {
				require.NotContains(t, result, PeriodicTaskTypeVulnerabilitySync)
			}
		})
	}
}

func TestDeltaPrepareDeadlineTaskMetadata(t *testing.T) {
	meta, ok := periodicTasks[PeriodicTaskTypeDeltaPrepareDeadline]
	require.True(t, ok)
	require.True(t, meta.SystemWide)
	require.Equal(t, time.Minute, meta.Interval)
	require.Contains(t, MergeTasksWithConfig(nil), PeriodicTaskTypeDeltaPrepareDeadline)
}

func TestLabelMappingScanTaskMetadataAndConfiguration(t *testing.T) {
	meta, ok := periodicTasks[PeriodicTaskTypeLabelMappingScan]
	require.True(t, ok)
	require.False(t, meta.SystemWide)
	require.Equal(t, config.DefaultLabelMappingScanTaskInterval, meta.Interval)
	require.Contains(t, MergeTasksWithConfig(nil), PeriodicTaskTypeLabelMappingScan)

	defaults := labelMappingScanConfigFromConfig(config.NewDefault())
	require.Equal(t, 1000, defaults.PageSize)
	require.Equal(t, 30*time.Second, defaults.TimeBudget)

	cfg := &config.Config{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"periodic": {"tasks": {"labelMappingScan": {
			"schedule": {"interval": "7m"},
			"pageSize": 250,
			"timeBudget": "15s"
		}}}
	}`), cfg))

	configured := MergeTasksWithConfig(cfg)[PeriodicTaskTypeLabelMappingScan]
	require.Equal(t, 7*time.Minute, configured.Interval)
	taskConfig := labelMappingScanConfigFromConfig(cfg)
	require.Equal(t, 250, taskConfig.PageSize)
	require.Equal(t, 15*time.Second, taskConfig.TimeBudget)
}

func TestPackageModeTemplateMigrationTaskMetadataAndConfiguration(t *testing.T) {
	merged := MergeTasksWithConfig(nil)
	meta, ok := merged[PeriodicTaskTypePackageModeTemplateMigration]
	require.True(t, ok, "the migration task must be registered alongside the other periodic tasks")
	require.Equal(t, config.DefaultPackageModeTemplateMigrationInterval, meta.Interval)
	require.False(t, meta.SystemWide, "the migration runs per organization")

	t.Run("When nothing is configured it should use the documented defaults", func(t *testing.T) {
		resolved := packageModeTemplateMigrationConfigFromConfig(&config.Config{})
		require.Equal(t, int32(config.DefaultPackageModeTemplateMigrationPageSize), resolved.PageSize)
		require.Equal(t, config.DefaultPackageModeTemplateMigrationBatchSize, resolved.BatchSize)
		require.Equal(t, config.DefaultPackageModeTemplateMigrationTimeBudget, resolved.TimeBudget)
		require.False(t, resolved.DryRun)
	})

	t.Run("When a nil config is supplied it should still resolve defaults", func(t *testing.T) {
		resolved := packageModeTemplateMigrationConfigFromConfig(nil)
		require.Equal(t, int32(config.DefaultPackageModeTemplateMigrationPageSize), resolved.PageSize)
	})

	t.Run("When the task is configured it should use every configured setting", func(t *testing.T) {
		cfg := &config.Config{}
		require.NoError(t, json.Unmarshal([]byte(`{
			"periodic": {"tasks": {"packageModeTemplateMigration": {
				"schedule": {"interval": "9m"},
				"pageSize": 25,
				"batchSize": 5,
				"timeBudget": "12s",
				"dryRun": true
			}}}
		}`), cfg))

		configured := MergeTasksWithConfig(cfg)[PeriodicTaskTypePackageModeTemplateMigration]
		require.Equal(t, 9*time.Minute, configured.Interval,
			"a configured poll interval must reach the scheduler, not just the configuration file")

		resolved := packageModeTemplateMigrationConfigFromConfig(cfg)
		require.Equal(t, int32(25), resolved.PageSize)
		require.Equal(t, 5, resolved.BatchSize)
		require.Equal(t, 12*time.Second, resolved.TimeBudget)
		require.True(t, resolved.DryRun)
	})
}
