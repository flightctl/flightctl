package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
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
		events           []client.PodmanEvent
	}{
		{
			name: "single app start",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
			},
			expectedReady:    "1/1",
			expectedStatus:   v1alpha1.ApplicationStatusRunning,
			expectedSummary:  v1alpha1.ApplicationsSummaryStatusHealthy,
			expectedRestarts: 0,
		},
		{
			name: "single app multiple containers started then one manual stop exit code 0",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", "app1-service-2", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "stop"),
			},
			expectedReady:   "1/2",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "single app multiple containers started then one manual stop result sigkill",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", "app1-service-2", "start"),
				mockPodmanEventError("app1", "app1-service-2", "died", 137),
			},
			expectedReady:   "1/2",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "single app start then die",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-1", "die"),
			},
			expectedReady:   "0/1",
			expectedStatus:  v1alpha1.ApplicationStatusError,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusError,
		},
		{
			name: "single app multiple containers one error one running",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", "app1-service-2", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "die"),
			},
			expectedReady:   "1/2",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "multiple apps preparing to running",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
				createTestApplication(require, "app2", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app2", "app1-service-1", "init"),
				mockPodmanEventSuccess("app2", "app1-service-1", "create"),
				mockPodmanEventSuccess("app2", "app1-service-1", "start"),
			},
			expectedReady:   "1/1",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app start then removed",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-1", "remove"),
			},
			expectedReady:   "0/0",
			expectedStatus:  v1alpha1.ApplicationStatusUnknown,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusUnknown,
		},
		{
			name: "app upgrade different service/container counts",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-1", "remove"),
				mockPodmanEventSuccess("app1", "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", "app1-service-2", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "remove"),
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
			},
			expectedReady:   "1/1",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app only creates container no start",
			apps: []Application{
				createTestApplication(require, "app1", v1alpha1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "create"), // no start
			},
			expectedReady:   "1/2",
			expectedStatus:  v1alpha1.ApplicationStatusRunning,
			expectedSummary: v1alpha1.ApplicationsSummaryStatusDegraded,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)
			execMock := executer.NewMockExecuter(ctrl)

			var testInspect []client.PodmanInspect
			restartsPerContainer := 3
			testInspect = append(testInspect, mockPodmanInspect(restartsPerContainer))
			inspectBytes, err := json.Marshal(testInspect)
			require.NoError(err)

			podman := client.NewPodman(log, execMock, rw, util.NewBackoff())
			podmanMonitor := NewPodmanMonitor(log, podman, "", rw)

			// add test apps to the monitor
			for _, testApp := range tc.apps {
				err := podmanMonitor.Ensure(testApp)
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
					app, exists := podmanMonitor.getByID(testApp.ID())
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

func TestApplicationAddRemove(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name           string
		appName        string
		expectedName   string
		initialStatus  v1alpha1.ApplicationStatusType
		action         string
		expectedExists bool
	}{
		{
			name:           "add app with '@' character",
			appName:        "app1@2",
			expectedName:   "app1_2-819634",
			action:         "add",
			expectedExists: true,
		},
		{
			name:           "add app with ':' character",
			appName:        "app-2:v2",
			expectedName:   "app-2_v2-721985",
			action:         "add",
			expectedExists: true,
		},
		{
			name:           "remove app1",
			appName:        "app1@2",
			expectedName:   "app1_2-819634",
			action:         "remove",
			expectedExists: false,
		},
		{
			name:           "remove app2",
			appName:        "app-2:v2",
			expectedName:   "app-2_v2-721985",
			action:         "remove",
			expectedExists: false,
		},
		{
			name:           "add app with '.' character",
			appName:        "quay.io/test/app:v2.1",
			expectedName:   "quay_io_test_app_v2_1-736341",
			action:         "add",
			expectedExists: true,
		},
		{
			name:           "add app with leading special characters",
			appName:        "@app",
			expectedName:   "_app-221494",
			action:         "add",
			expectedExists: true,
		},
		{
			name:           "add app with trailing special characters",
			appName:        "app@",
			expectedName:   "app_-583275",
			action:         "add",
			expectedExists: true,
		},
		{
			name:           "add app with special characters in sequence",
			appName:        "app!!",
			expectedName:   "app__-260528",
			action:         "add",
			expectedExists: true,
		},
	}

	// Execute test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
			execMock := executer.NewMockExecuter(ctrl)

			podman := client.NewPodman(log, execMock, readWriter, util.NewBackoff())
			podmanMonitor := NewPodmanMonitor(log, podman, "", readWriter)
			testApp := createTestApplication(require, tc.appName, v1alpha1.ApplicationStatusPreparing)

			switch tc.action {
			case "add":
				err := podmanMonitor.Ensure(testApp)
				require.NoError(err)
			case "remove":
				err := podmanMonitor.Remove(testApp)
				require.NoError(err)
			}

			// Check if app is in the monitor under the sanitized name
			_, found := podmanMonitor.apps[tc.expectedName]
			require.Equal(tc.expectedExists, found, "Unexpected app for %s", tc.expectedName)
		})
	}
}

func createTestApplication(require *require.Assertions, name string, status v1alpha1.ApplicationStatusType) Application {
	provider := newMockProvider(require, name)
	app := NewApplication(provider)
	app.status.Status = status
	return app
}

func writeEvent(writer io.WriteCloser, event *client.PodmanEvent) error {
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return err
	}
	eventBytes = append(eventBytes, '\n')
	_, err = writer.Write(eventBytes)
	return err
}

func mockPodmanEventSuccess(name, service, status string) client.PodmanEvent {
	return createMockPodmanEvent(name, service, status, 0)
}

func mockPodmanEventError(name, service, status string, exitCode int) client.PodmanEvent {
	return createMockPodmanEvent(name, service, status, exitCode)
}

func createMockPodmanEvent(name, service, status string, exitCode int) client.PodmanEvent {
	event := client.PodmanEvent{
		ID:     "8559c630e04ea852101467742e95b9e371fe6dd8c9195910354636d68d388a40",
		Image:  "docker.io/library/alpine:latest",
		Name:   fmt.Sprintf("%s-container", service),
		Status: status,
		Type:   "container",
		Attributes: map[string]string{
			"PODMAN_SYSTEMD_UNIT":                     "podman-compose@user.service",
			"com.docker.compose.container-number":     "1",
			"com.docker.compose.project":              client.NewComposeID(name),
			"com.docker.compose.project.config_files": "podman-compose.yaml",
			"com.docker.compose.project.working_dir":  path.Join("/usr/local/lib/compose", name),
			"com.docker.compose.service":              service,
			"io.podman.compose.config-hash":           "dc33a4cfdb3cf6b442309e44bd819fcba2ce89393f5d80d6b6b0e9ebb4767e25",
			"io.podman.compose.project":               name,
			"io.podman.compose.version":               "1.0.6",
		},
	}
	if exitCode != 0 {
		event.ContainerExitCode = exitCode
	}
	return event
}

func mockPodmanInspect(restarts int) client.PodmanInspect {
	return client.PodmanInspect{
		Restarts: restarts,
	}
}

func BenchmarkNewComposeID(b *testing.B) {
	// bench different string length
	lengths := []int{50, 100, 253}
	for _, size := range lengths {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			input := strings.Repeat("a", size)
			for i := 0; i < b.N; i++ {
				client.NewComposeID(input)
			}
		})
	}
}

func newMockProvider(require *require.Assertions, name string) provider.Provider {
	return &mockProvider{name: name, require: require}
}

type mockProvider struct {
	name    string
	require *require.Assertions
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Spec() *provider.ApplicationSpec {
	volManager, err := provider.NewVolumeManager(nil, m.name, nil)
	m.require.NoError(err)
	return &provider.ApplicationSpec{
		ID:     client.NewComposeID(m.name),
		Name:   m.name,
		Volume: volManager,
	}
}

func (m *mockProvider) Verify(ctx context.Context) error {
	return nil
}

func (m *mockProvider) Install(ctx context.Context) error {
	return nil
}

func (m *mockProvider) Remove(ctx context.Context) error {
	return nil
}
