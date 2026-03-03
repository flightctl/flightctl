package applications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flightctl/flightctl/internal/agent/client"
)

func TestResolveStatus(t *testing.T) {
	testCases := []struct {
		name          string
		status        string
		inspectData   []client.PodmanInspect
		expectedStatus StatusType
	}{
		{
			name:   "status die, exit 0, no restart policy",
			status: "die",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						FinishedAt: "sometime",
					},
					HostConfig: client.PodmanHostConfig{
						RestartPolicy: client.PodmanRestartPolicy{
							Name: "",
						},
					},
				},
			},
			expectedStatus: StatusExited,
		},
		{
			name:   "status died, exit 0, no restart policy",
			status: "died",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						FinishedAt: "sometime",
					},
					HostConfig: client.PodmanHostConfig{
						RestartPolicy: client.PodmanRestartPolicy{
							Name: "no",
						},
					},
				},
			},
			expectedStatus: StatusExited,
		},
		{
			name:   "status die, exit 1, no restart policy",
			status: "die",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   1,
						FinishedAt: "sometime",
					},
				},
			},
			expectedStatus: StatusDie,
		},
		{
			name:   "status die, exit 0, always restart policy",
			status: "die",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						FinishedAt: "sometime",
					},
					HostConfig: client.PodmanHostConfig{
						RestartPolicy: client.PodmanRestartPolicy{
							Name: "always",
						},
					},
				},
			},
			expectedStatus: StatusDied,
		},
		{
			name:   "status die, exit 0, on-failure restart policy",
			status: "die",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						FinishedAt: "sometime",
					},
					HostConfig: client.PodmanHostConfig{
						RestartPolicy: client.PodmanRestartPolicy{
							Name: "on-failure",
						},
					},
				},
			},
			expectedStatus: StatusDied,
		},
		{
			name:           "other status",
			status:         "running",
			inspectData:   []client.PodmanInspect{},
			expectedStatus: StatusRunning,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			monitor := &PodmanMonitor{}
			status := monitor.resolveStatus(tc.status, tc.inspectData)
			r.Equal(tc.expectedStatus, status)
		})
	}
}