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

			// Double check that other tasks are preserved with defaults
			for taskType, defaultMeta := range periodicTasks {
				require.Contains(t, result, taskType)
				if taskType != PeriodicTaskTypeResourceSync {
					require.Equal(t, defaultMeta.Interval, result[taskType].Interval)
				}
			}
		})
	}
}
