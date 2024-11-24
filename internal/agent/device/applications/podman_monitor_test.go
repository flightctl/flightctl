package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestListenForEvents(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name             string
		apps             []Application
		expectedReady    string
		expectedRestarts int
		expectedStatus   v1alpha1.ApplicationStatusType
		expectedSummary  v1alpha1.ApplicationsSummaryStatusType
		events           []PodmanEvent
	}{
		{
			name: "single app start",
			apps: []Application{
				createTestApplication("app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []PodmanEvent{
				mockPodmanEvent("app1", "app1-service-1", "init"),
				mockPodmanEvent("app1", "app1-service-1", "create"),
				mockPodmanEvent("app1", "app1-service-1", "start"),
			},
			expectedReady:   "1/1",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "single app start then die",
			apps: []Application{
				createTestApplication("app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []PodmanEvent{
				mockPodmanEvent("app1", "app1-service-1", "init"),
				mockPodmanEvent("app1", "app1-service-1", "create"),
				mockPodmanEvent("app1", "app1-service-1", "start"),
				mockPodmanEvent("app1", "app1-service-1", "die"),
			},
			expectedReady:   "0/1",
			expectedStatus:  v1alpha1.ApplicationStatusError,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusError,
		},
		{
			name: "single app multiple containers one error one running",
			apps: []Application{
				createTestApplication("app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []PodmanEvent{
				mockPodmanEvent("app1", "app1-service-1", "init"),
				mockPodmanEvent("app1", "app1-service-1", "create"),
				mockPodmanEvent("app1", "app1-service-1", "start"),
				mockPodmanEvent("app1", "app1-service-2", "init"),
				mockPodmanEvent("app1", "app1-service-2", "create"),
				mockPodmanEvent("app1", "app1-service-2", "start"),
				mockPodmanEvent("app1", "app1-service-2", "die"),
			},
			expectedReady:   "1/2",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "multiple apps preparing to running",
			apps: []Application{
				createTestApplication("app1", v1alpha1.ApplicationStatusPreparing),
				createTestApplication("app2", v1alpha1.ApplicationStatusPreparing),
			},
			events: []PodmanEvent{
				mockPodmanEvent("app1", "app1-service-1", "init"),
				mockPodmanEvent("app1", "app1-service-1", "create"),
				mockPodmanEvent("app1", "app1-service-1", "start"),
				mockPodmanEvent("app2", "app1-service-1", "init"),
				mockPodmanEvent("app2", "app1-service-1", "create"),
				mockPodmanEvent("app2", "app1-service-1", "start"),
			},
			expectedReady:   "1/1",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app start then removed",
			apps: []Application{
				createTestApplication("app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []PodmanEvent{
				mockPodmanEvent("app1", "app1-service-1", "init"),
				mockPodmanEvent("app1", "app1-service-1", "create"),
				mockPodmanEvent("app1", "app1-service-1", "start"),
				mockPodmanEvent("app1", "app1-service-1", "remove"),
			},
			expectedReady:   "0/0",
			expectedStatus:  v1alpha1.ApplicationStatusUnknown,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusUnknown,
		},
		{
			name: "app upgrade different service/container counts",
			apps: []Application{
				createTestApplication("app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []PodmanEvent{
				mockPodmanEvent("app1", "app1-service-1", "init"),
				mockPodmanEvent("app1", "app1-service-1", "create"),
				mockPodmanEvent("app1", "app1-service-1", "start"),
				mockPodmanEvent("app1", "app1-service-1", "remove"),
				mockPodmanEvent("app1", "app1-service-2", "init"),
				mockPodmanEvent("app1", "app1-service-2", "create"),
				mockPodmanEvent("app1", "app1-service-2", "start"),
				mockPodmanEvent("app1", "app1-service-2", "remove"),
				mockPodmanEvent("app1", "app1-service-1", "init"),
				mockPodmanEvent("app1", "app1-service-1", "create"),
				mockPodmanEvent("app1", "app1-service-1", "start"),
			},
			expectedReady:   "1/1",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			execMock := executer.NewMockExecuter(ctrl)

			var testInspect []PodmanInspect
			restartsPerContainer := 3
			testInspect = append(testInspect, mockPodmanInspect(restartsPerContainer))
			inspectBytes, err := json.Marshal(testInspect)
			require.NoError(err)

			podman := client.NewPodman(log, execMock)
			podmanMonitor := NewPodmanMonitor(log, execMock, podman)
			defer func() {
				err := podmanMonitor.Stop(context.Background())
				require.NoError(err)
			}()

			// add test apps to the monitor
			for _, testApp := range tc.apps {
				err = podmanMonitor.ensure(testApp)
				require.NoError(err)
			}

			// create a pipe to simulate events being written to the monitor
			reader, writer := io.Pipe()
			defer reader.Close()

			go podmanMonitor.listenForEvents(context.Background(), reader)

			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(string(inspectBytes), "", 0).Times(len(tc.events))

			// simulate events being written to the pipe
			go func() {
				defer writer.Close()
				for i := range tc.events {
					event := tc.events[i]
					err := writeEvent(writer, &event)
					require.NoError(err)
				}
			}()

			timeoutDuration := 5 * time.Second
			retryDuration := 100 * time.Millisecond
			for _, testApp := range tc.apps {
				require.Eventually(func() bool {
					// get app
					app, exists := podmanMonitor.get(testApp.Name())
					if !exists {
						t.Logf("app not found: %s", testApp.Name())
						return false
					}
					// check app status
					status, summary, err := app.Status()
					require.NoError(err)
					if status == nil {
						t.Logf("app has no status: %s", testApp.Name())
						return false
					}
					if tc.expectedSummary != summary.Status {
						t.Logf("app %s expected summary %s but got %s", testApp.Name(), tc.expectedSummary, summary.Status)
						return false
					}
					// ensure the app has the expected number of containers
					if status.Ready != tc.expectedReady {
						t.Logf("app %s expected ready %s but got %s", testApp.Name(), tc.expectedReady, status.Ready)
						return false
					}

					// ensure the app has the expected status
					if status.Status != tc.expectedStatus {
						t.Logf("app %s expected status %s but got %s", testApp.Name(), tc.expectedStatus, status.Status)
						return false
					}

					return true
				}, timeoutDuration, retryDuration, "data was not processed in time")
			}

		})
	}
}

func createTestApplication(name string, status v1alpha1.ApplicationStatusType) Application {
	var provider v1alpha1.ImageApplicationProvider
	app := NewApplication(name, provider, AppCompose)
	app.status.Status = status
	return app
}

func writeEvent(writer io.WriteCloser, event *PodmanEvent) error {
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return err
	}
	eventBytes = append(eventBytes, '\n')
	_, err = writer.Write(eventBytes)
	return err
}

func mockPodmanEvent(name, service, status string) PodmanEvent {
	return PodmanEvent{
		ID:     "8559c630e04ea852101467742e95b9e371fe6dd8c9195910354636d68d388a40",
		Image:  "docker.io/library/alpine:latest",
		Name:   fmt.Sprintf("%s-container", service),
		Status: status,
		Type:   "container",
		Attributes: map[string]string{
			"PODMAN_SYSTEMD_UNIT":                     "podman-compose@user.service",
			"com.docker.compose.container-number":     "1",
			"com.docker.compose.project":              name,
			"com.docker.compose.project.config_files": "podman-compose.yaml",
			"com.docker.compose.project.working_dir":  path.Join("/usr/local/lib/compose", name),
			"com.docker.compose.service":              service,
			"io.podman.compose.config-hash":           "dc33a4cfdb3cf6b442309e44bd819fcba2ce89393f5d80d6b6b0e9ebb4767e25",
			"io.podman.compose.project":               name,
			"io.podman.compose.version":               "1.0.6",
		},
	}
}

func mockPodmanInspect(restarts int) PodmanInspect {
	return PodmanInspect{
		Restarts: restarts,
	}
}
