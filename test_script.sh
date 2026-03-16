sed -i '/func TestManualStopPreservesStatusError/,$d' /workspace/internal/agent/device/applications/podman_monitor_test.go

cat << 'TEST' >> /workspace/internal/agent/device/applications/podman_monitor_test.go

func TestManualStopPreservesStatusError(t *testing.T) {
	require := require.New(t)

	log := log.NewPrefixLogger("test")
	podmanMonitor := NewPodmanMonitor(log, nil, nil, "", nil)
	
	app := createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, "testuser")
	err := podmanMonitor.Ensure(context.Background(), app)
	require.NoError(err)
	
	containerID := "test-container-id"
	containerName := "test-container-name"
	
	eventStart := &client.PodmanEvent{
		ID:     containerID,
		Name:   containerName,
		Type:   "container",
		Status: "start",
	}
	
	// Directly call updateApplicationStatus to simulate transitions
	podmanMonitor.updateApplicationStatus(app, eventStart, StatusRunning, 0)
	
	statusRes, err := podmanMonitor.Status()
	require.NoError(err)
	require.Equal(1, len(statusRes))
	require.Equal(v1beta1.ApplicationStatusRunning, statusRes[0].Status.Status)
	
	eventStop := &client.PodmanEvent{
		ID:     containerID,
		Name:   containerName,
		Type:   "container",
		Status: "stop",
	}
	podmanMonitor.updateApplicationStatus(app, eventStop, StatusStop, 0)
	
	statusRes, err = podmanMonitor.Status()
	require.NoError(err)
	require.Equal(1, len(statusRes))
	require.Equal(v1beta1.ApplicationStatusError, statusRes[0].Status.Status)
	
	eventDied := &client.PodmanEvent{
		ID:     containerID,
		Name:   containerName,
		Type:   "container",
		Status: "died",
	}
	
	// A manual stop results in exit code 0 which is mapped to StatusExited
	// Our fix preserves StatusStop in this case
	podmanMonitor.updateApplicationStatus(app, eventDied, StatusExited, 0)
	
	statusRes, err = podmanMonitor.Status()
	require.NoError(err)
	require.Equal(1, len(statusRes))
	require.Equal(v1beta1.ApplicationStatusError, statusRes[0].Status.Status)

	// But if it naturally exited without a previous stop event, it should be Completed
	app2 := createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing, "testuser")
	err = podmanMonitor.Ensure(context.Background(), app2)
	require.NoError(err)

	podmanMonitor.updateApplicationStatus(app2, eventStart, StatusRunning, 0)
	podmanMonitor.updateApplicationStatus(app2, eventDied, StatusExited, 0)

	statusRes, err = podmanMonitor.Status()
	require.NoError(err)
	require.Equal(2, len(statusRes))
	// app2 is index 1 because map order is not guaranteed, let's find it
	for _, res := range statusRes {
		if res.Status.Name == "app2" {
			require.Equal(v1beta1.ApplicationStatusCompleted, res.Status.Status)
		} else {
			require.Equal(v1beta1.ApplicationStatusError, res.Status.Status)
		}
	}
}
TEST

go test -run TestManualStopPreservesStatusError ./internal/agent/device/applications/...
