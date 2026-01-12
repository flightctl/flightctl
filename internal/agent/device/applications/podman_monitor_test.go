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
			expectedStatus: StatusExited,
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

func TestPodmanMonitorStatus(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter()
	readWriter.SetRootdir(tmpDir)

	mockExec := executer.NewMockExecuter(ctrl)
	podman := client.NewPodman(log, mockExec, readWriter, util.NewPollConfig())

	spec := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Content: lo.ToPtr(util.NewComposeSpec()),
				Path:    "docker-compose.yml",
			},
		},
	}

	providerSpec := v1beta1.ApplicationProviderSpec{
		Name:    lo.ToPtr("app"),
		AppType: v1beta1.AppTypeCompose,
	}
	err := providerSpec.FromInlineApplicationProviderSpec(spec)
	require.NoError(err)
	desired := v1beta1.DeviceSpec{
		Applications: &[]v1beta1.ApplicationProviderSpec{
			providerSpec,
		},
	}
	providers, err := provider.FromDeviceSpec(context.Background(), log, podman, readWriter, &desired)
	require.NoError(err)
	require.Len(providers, 1)
	application := NewApplication(providers[0])

	monitor := NewPodmanMonitor(log, podman, nil, "2023-01-01T12:00:00Z", readWriter)
	err = monitor.Ensure(application)
	require.NoError(err)

	event := &client.PodmanEvent{
		ID:   "container1",
		Name: "container1",
		Attributes: map[string]string{
			client.ComposeDockerProjectLabelKey: application.ID(),
		},
	}

	monitor.updateApplicationStatus(application, event, StatusExited, 0, lo.ToPtr(0))

	statuses, summary, err := monitor.Status()
	require.NoError(err)
	require.Len(statuses, 1)
	require.Equal(v1beta1.ApplicationStatusError, statuses[0].Status)
	require.Equal(v1beta1.ApplicationsSummaryStatusError, summary.Status)
}