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
