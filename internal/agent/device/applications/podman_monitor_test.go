func TestResolveStatus(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name          string
		status        string
		inspectData   []client.PodmanInspect
		expectedStatus StatusType
	}{
		{
			name:   "running container",
			status: "start",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						Status: "running",
					},
				},
			},
			expectedStatus: StatusRunning,
		},
		{
			name:   "completed successfully",
			status: "died",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						FinishedAt: "2023-01-01T12:00:00Z",
					},
				},
			},
			expectedStatus: StatusDied,
		},
		{
			name:   "exited with error",
			status: "died",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   1,
						FinishedAt: "2023-01-01T12:00:00Z",
					},
				},
			},
			expectedStatus: StatusDied,
		},
		{
			name:   "stopped with sigterm and exit 0",
			status: "died",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   0,
						ExitSignal: 15, // SIGTERM
						FinishedAt: "2023-01-01T12:00:00Z",
					},
				},
			},
			expectedStatus: StatusStopped,
		},
		{
			name:   "stopped with sigkill",
			status: "died",
			inspectData: []client.PodmanInspect{
				{
					State: client.PodmanContainerState{
						ExitCode:   137,
						ExitSignal: 9, // SIGKILL
						FinishedAt: "2023-01-01T12:00:00Z",
					},
				},
			},
			expectedStatus: StatusDied,
		},
	}

	log := log.NewPrefixLogger("test")
	monitor := &PodmanMonitor{log: log}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := monitor.resolveStatus(tc.status, tc.inspectData)
			require.Equal(tc.expectedStatus, status)
		})
	}
}