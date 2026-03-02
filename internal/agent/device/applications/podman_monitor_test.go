package applications

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/stretchr/testify/require"
)

func TestPodmanMonitor_resolveStatus(t *testing.T) {
	testCases := []struct {
		name          string
		status        string
		inspectData   []client.PodmanInspect
		expectedStatus StatusType
	}{
		{
			name:   "container dies with exit code 0",
			status: "die",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						FinishedAt: "2024-01-01T00:00:00Z",
					},
				},
			},
			expectedStatus: StatusStopped,
		},
		{
			name:   "container died with exit code 0",
			status: "died",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						FinishedAt: "2024-01-01T00:00:00Z",
					},
				},
			},
			expectedStatus: StatusStopped,
		},
		{
			name:   "container dies with non-zero exit code",
			status: "die",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   1,
						FinishedAt: "2024-01-01T00:00:00Z",
					},
				},
			},
			expectedStatus: StatusDie,
		},
		{
			name:   "container running",
			status: "running",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						Running: true,
					},
				},
			},
			expectedStatus: "running",
		},
		{
			name:           "no inspect data",
			status:         "die",
			inspectData:    []client.PodmanInspect{},
			expectedStatus: StatusDie,
		},
	}

	monitor := &PodmanMonitor{}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := monitor.resolveStatus(tc.status, tc.inspectData)
			require.Equal(t, tc.expectedStatus, status)
		})
	}
}