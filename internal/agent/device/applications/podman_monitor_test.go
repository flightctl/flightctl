package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
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
		expectedStatus   v1beta1.ApplicationStatusType
		expectedSummary  v1beta1.ApplicationsSummaryStatusType
		events           []client.PodmanEvent
	}{
		{
			name: "single app start",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
			},
			expectedReady:    "1/1",
			expectedStatus:   v1beta1.ApplicationStatusRunning,
			expectedSummary:  v1beta1.ApplicationsSummaryStatusHealthy,
			expectedRestarts: 0,
		},
		{
			name: "single app multiple containers started then one manual stop exit code 0",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
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
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "single app multiple containers started then one manual stop result sigkill",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
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
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "single app start then die",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-1", "die"),
			},
			expectedReady:   "0/1",
			expectedStatus:  v1beta1.ApplicationStatusError,
			expectedSummary: v1beta1.ApplicationsSummaryStatusError,
		},
		{
			name: "single app multiple containers one error one running",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
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
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "multiple apps preparing to running",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing),
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
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app start then removed",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-1", "remove"),
			},
			expectedReady:   "0/0",
			expectedStatus:  v1beta1.ApplicationStatusUnknown,
			expectedSummary: v1beta1.ApplicationsSummaryStatusUnknown,
		},
		{
			name: "app upgrade different service/container counts",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
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
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app only creates container no start",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", "app1-service-2", "create"), // no start
			},
			expectedReady:   "1/2",
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
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

			podman := client.NewPodman(log, execMock, rw, util.NewPollConfig())
			systemdMgr := systemd.NewMockManager(ctrl)
			systemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			systemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
			podmanMonitor := NewPodmanMonitor(log, podman, systemdMgr, "", rw)

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
		initialStatus  v1beta1.ApplicationStatusType
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

			podman := client.NewPodman(log, execMock, readWriter, util.NewPollConfig())
			systemdMgr := systemd.NewMockManager(ctrl)
			systemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			systemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
			podmanMonitor := NewPodmanMonitor(log, podman, systemdMgr, "", readWriter)
			testApp := createTestApplication(require, tc.appName, v1beta1.ApplicationStatusPreparing)

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

func createTestApplication(require *require.Assertions, name string, status v1beta1.ApplicationStatusType) Application {
	return createTestApplicationWithType(require, name, status, v1beta1.AppTypeCompose)
}

func createTestApplicationWithType(require *require.Assertions, name string, status v1beta1.ApplicationStatusType, appType v1beta1.AppType) Application {
	provider := newMockProvider(require, name, appType)
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

func newMockProvider(require *require.Assertions, name string, appType v1beta1.AppType) provider.Provider {
	return &mockProvider{name: name, require: require, appType: appType}
}

type mockProvider struct {
	name    string
	require *require.Assertions
	appType v1beta1.AppType
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Spec() *provider.ApplicationSpec {
	volManager, err := provider.NewVolumeManager(nil, m.name, v1beta1.AppTypeCompose, nil)
	m.require.NoError(err)
	return &provider.ApplicationSpec{
		ID:      client.NewComposeID(m.name),
		Name:    m.name,
		Volume:  volManager,
		AppType: m.appType,
	}
}

func (m *mockProvider) OCITargets(ctx context.Context, pullSecret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
	return nil, nil
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

func TestPodmanMonitorMultipleAddRemoveCycles(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, util.NewPollConfig())

	readWriter := fileio.NewReadWriter()
	tmpDir := t.TempDir()
	readWriter.SetRootdir(tmpDir)

	// Unlike the real podman events call, this will emit a single event and then close
	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", gomock.Any()).
		DoAndReturn(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			now := time.Now().UnixNano()
			return exec.CommandContext(ctx, "echo", fmt.Sprintf(`{"timeNano": %d}`, now)) //nolint:gosec
		}).AnyTimes()

	podmanMonitor := NewPodmanMonitor(log, mockPodmanClient, systemd.NewMockManager(ctrl), "", readWriter)

	// Override lifecycle handlers with no-op mocks to avoid file/exec expectations
	mockComposeHandler := lifecycle.NewMockActionHandler(ctrl)
	mockQuadletHandler := lifecycle.NewMockActionHandler(ctrl)
	mockComposeHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockQuadletHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	podmanMonitor.handlers[v1beta1.AppTypeCompose] = mockComposeHandler
	podmanMonitor.handlers[v1beta1.AppTypeQuadlet] = mockQuadletHandler

	// Create test applications
	app1 := createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing)
	app2 := createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing)

	// Application IDs for tracking
	app1ID := app1.ID()
	app2ID := app2.ID()

	// 1. Start with no applications - verify monitor is not running
	require.False(podmanMonitor.isRunning())
	require.Equal(0, len(podmanMonitor.apps))

	// 2. Add two applications
	err := podmanMonitor.Ensure(app1)
	require.NoError(err)
	err = podmanMonitor.Ensure(app2)
	require.NoError(err)

	// Execute actions - monitor should start because we have apps
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.True(podmanMonitor.isRunning())
	require.True(podmanMonitor.Has(app1ID))
	require.True(podmanMonitor.Has(app2ID))

	// Process an event
	require.Eventually(func() bool {
		return podmanMonitor.getLastEventTime() != ""
	}, time.Millisecond*100, 5*time.Millisecond)

	// 3. Remove app1 - monitor should still be running
	err = podmanMonitor.Remove(app1)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.True(podmanMonitor.isRunning()) // Still running because app2 exists
	require.False(podmanMonitor.Has(app1ID))
	require.True(podmanMonitor.Has(app2ID))

	// 4. Remove app2 - monitor should stop
	err = podmanMonitor.Remove(app2)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.False(podmanMonitor.isRunning()) // Stopped because no apps
	require.False(podmanMonitor.Has(app1ID))
	require.False(podmanMonitor.Has(app2ID))

	// ensure no panic occurs as the result of stopping an already stopped monitor
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.False(podmanMonitor.isRunning())

	// 5. Add app1 again - monitor should start
	err = podmanMonitor.Ensure(app1)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.True(podmanMonitor.isRunning()) // Started again
	require.True(podmanMonitor.Has(app1ID))

	// 6. Remove app1 final time - monitor should stop
	err = podmanMonitor.Remove(app1)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.False(podmanMonitor.isRunning()) // Stopped again
	require.False(podmanMonitor.Has(app1ID))
}

func TestPodmanMonitorHandlerSelection(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, util.NewPollConfig())

	readWriter := fileio.NewReadWriter()
	tmpDir := t.TempDir()
	readWriter.SetRootdir(tmpDir)

	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", gomock.Any()).
		DoAndReturn(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			now := time.Now().UnixNano()
			return exec.CommandContext(ctx, "echo", fmt.Sprintf(`{"timeNano": %d}`, now)) //nolint:gosec
		}).AnyTimes()

	podmanMonitor := NewPodmanMonitor(log, mockPodmanClient, systemd.NewMockManager(ctrl), "", readWriter)

	// Create separate mock handlers to track which one gets called
	mockComposeHandler := lifecycle.NewMockActionHandler(ctrl)
	mockQuadletHandler := lifecycle.NewMockActionHandler(ctrl)

	// Track calls to each handler
	var composeActions []*lifecycle.Action
	var quadletActions []*lifecycle.Action

	mockComposeHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, action *lifecycle.Action) error {
			composeActions = append(composeActions, action)
			return nil
		}).AnyTimes()
	mockQuadletHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, action *lifecycle.Action) error {
			quadletActions = append(quadletActions, action)
			return nil
		}).AnyTimes()

	podmanMonitor.handlers[v1beta1.AppTypeCompose] = mockComposeHandler
	podmanMonitor.handlers[v1beta1.AppTypeQuadlet] = mockQuadletHandler

	// Create apps with different types
	composeApp := createTestApplicationWithType(require, "compose-app", v1beta1.ApplicationStatusPreparing, v1beta1.AppTypeCompose)
	quadletApp := createTestApplicationWithType(require, "quadlet-app", v1beta1.ApplicationStatusPreparing, v1beta1.AppTypeQuadlet)

	// Ensure both apps
	err := podmanMonitor.Ensure(composeApp)
	require.NoError(err)
	err = podmanMonitor.Ensure(quadletApp)
	require.NoError(err)

	// Execute actions
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)

	// Verify compose handler was called for compose app
	require.Equal(1, len(composeActions), "Compose handler should be called once")
	require.Equal(composeApp.ID(), composeActions[0].ID)
	require.Equal(v1beta1.AppTypeCompose, composeActions[0].AppType)
	require.Equal(lifecycle.ActionAdd, composeActions[0].Type)

	// Verify quadlet handler was called for quadlet app
	require.Equal(1, len(quadletActions), "Quadlet handler should be called once")
	require.Equal(quadletApp.ID(), quadletActions[0].ID)
	require.Equal(v1beta1.AppTypeQuadlet, quadletActions[0].AppType)
	require.Equal(lifecycle.ActionAdd, quadletActions[0].Type)

	// Reset tracking
	composeActions = nil
	quadletActions = nil

	// Remove compose app
	err = podmanMonitor.Remove(composeApp)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)

	// Verify compose handler was called for removal
	require.Equal(1, len(composeActions), "Compose handler should be called once for removal")
	require.Equal(composeApp.ID(), composeActions[0].ID)
	require.Equal(lifecycle.ActionRemove, composeActions[0].Type)

	// Verify quadlet handler was NOT called
	require.Equal(0, len(quadletActions), "Quadlet handler should not be called")
}

func TestPodmanMonitorStatusAggregation(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name           string
		apps           []Application
		workloads      map[string][]Workload
		expectedStatus v1beta1.ApplicationsSummaryStatusType
		expectedInfo   *string
	}{
		{
			name:           "no apps returns NoApplications",
			apps:           []Application{},
			workloads:      map[string][]Workload{},
			expectedStatus: v1beta1.ApplicationsSummaryStatusNoApplications,
			expectedInfo:   nil,
		},
		{
			name: "single healthy app returns Healthy",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
			},
			workloads: map[string][]Workload{
				"app1": {{Name: "container1", Status: StatusRunning}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusHealthy,
			expectedInfo:   nil,
		},
		{
			name: "single unknown app (no workloads) returns Degraded",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
			},
			workloads:      map[string][]Workload{},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expectedInfo:   lo.ToPtr("Not started: app1"),
		},
		{
			name: "two unknown apps returns Degraded with both names",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing),
			},
			workloads:      map[string][]Workload{},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "one healthy one unknown returns Degraded",
			apps: []Application{
				createTestApplication(require, "healthy-app", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "unknown-app", v1beta1.ApplicationStatusPreparing),
			},
			workloads: map[string][]Workload{
				"healthy-app": {{Name: "container1", Status: StatusRunning}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "two unknown one healthy returns Degraded",
			apps: []Application{
				createTestApplication(require, "unknown1", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "unknown2", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "healthy", v1beta1.ApplicationStatusPreparing),
			},
			workloads: map[string][]Workload{
				"healthy": {{Name: "container1", Status: StatusRunning}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "one healthy one error returns Error",
			apps: []Application{
				createTestApplication(require, "healthy-app", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "error-app", v1beta1.ApplicationStatusPreparing),
			},
			workloads: map[string][]Workload{
				"healthy-app": {{Name: "container1", Status: StatusRunning}},
				"error-app":   {{Name: "container1", Status: StatusDie}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusError,
			expectedInfo:   nil,
		},
		{
			name: "one unknown one error returns Error",
			apps: []Application{
				createTestApplication(require, "unknown-app", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "error-app", v1beta1.ApplicationStatusPreparing),
			},
			workloads: map[string][]Workload{
				"error-app": {{Name: "container1", Status: StatusDie}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusError,
			expectedInfo:   nil,
		},
		{
			name: "all healthy returns Healthy",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "app3", v1beta1.ApplicationStatusPreparing),
			},
			workloads: map[string][]Workload{
				"app1": {{Name: "container1", Status: StatusRunning}},
				"app2": {{Name: "container1", Status: StatusRunning}},
				"app3": {{Name: "container1", Status: StatusRunning}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusHealthy,
			expectedInfo:   nil,
		},
		{
			name: "one degraded one healthy returns Degraded",
			apps: []Application{
				createTestApplication(require, "healthy-app", v1beta1.ApplicationStatusPreparing),
				createTestApplication(require, "degraded-app", v1beta1.ApplicationStatusPreparing),
			},
			workloads: map[string][]Workload{
				"healthy-app": {{Name: "container1", Status: StatusRunning}},
				"degraded-app": {
					{Name: "container1", Status: StatusRunning},
					{Name: "container2", Status: StatusDie},
				},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expectedInfo:   lo.ToPtr("degraded-app is in status Degraded"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			testLog := log.NewPrefixLogger("test")
			testLog.SetLevel(logrus.DebugLevel)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)
			execMock := executer.NewMockExecuter(ctrl)

			podman := client.NewPodman(testLog, execMock, rw, util.NewPollConfig())
			systemdMgr := systemd.NewMockManager(ctrl)
			podmanMonitor := NewPodmanMonitor(testLog, podman, systemdMgr, "", rw)

			for _, app := range tc.apps {
				podmanMonitor.apps[app.ID()] = app

				if workloads, ok := tc.workloads[app.Name()]; ok {
					for _, w := range workloads {
						workload := w
						app.AddWorkload(&workload)
					}
				}
			}

			statuses, summary, err := podmanMonitor.Status()
			require.NoError(err)

			require.Equal(tc.expectedStatus, summary.Status,
				"expected summary status %s but got %s", tc.expectedStatus, summary.Status)

			if tc.expectedInfo != nil {
				require.NotNil(summary.Info, "expected Info to be set")
				require.Equal(*tc.expectedInfo, *summary.Info,
					"expected Info %q but got %q", *tc.expectedInfo, *summary.Info)
			}

			require.Equal(len(tc.apps), len(statuses),
				"expected %d app statuses but got %d", len(tc.apps), len(statuses))
		})
	}
}

func TestBuildAppSummaryInfo(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name         string
		erroredApps  []string
		degradedApps []string
		maxLen       int
		expectedInfo *string
	}{
		{
			name:         "no problems returns nil",
			erroredApps:  []string{},
			degradedApps: []string{},
			maxLen:       256,
			expectedInfo: nil,
		},
		{
			name:         "single errored app",
			erroredApps:  []string{"app1 is in status Error"},
			degradedApps: []string{},
			maxLen:       256,
			expectedInfo: lo.ToPtr("app1 is in status Error"),
		},
		{
			name:         "single degraded app",
			erroredApps:  []string{},
			degradedApps: []string{"app1 is in status Degraded"},
			maxLen:       256,
			expectedInfo: lo.ToPtr("app1 is in status Degraded"),
		},
		{
			name:         "errors listed before degradations",
			erroredApps:  []string{"error-app is in status Error"},
			degradedApps: []string{"degraded-app is in status Degraded"},
			maxLen:       256,
			expectedInfo: lo.ToPtr("error-app is in status Error, degraded-app is in status Degraded"),
		},
		{
			name:         "multiple errors and degradations",
			erroredApps:  []string{"err1 is in status Error", "err2 is in status Error"},
			degradedApps: []string{"deg1 is in status Degraded"},
			maxLen:       256,
			expectedInfo: lo.ToPtr("err1 is in status Error, err2 is in status Error, deg1 is in status Degraded"),
		},
		{
			name:         "truncation when exceeding max length",
			erroredApps:  []string{"very-long-app-name-1 is in status Error", "very-long-app-name-2 is in status Error"},
			degradedApps: []string{"very-long-app-name-3 is in status Degraded"},
			maxLen:       50,
			expectedInfo: lo.ToPtr("very-long-app-name-1 is in status Error, very-l..."),
		},
		{
			name:         "exact max length not truncated",
			erroredApps:  []string{"app1"},
			degradedApps: []string{},
			maxLen:       4,
			expectedInfo: lo.ToPtr("app1"),
		},
		{
			name:         "one over max length gets truncated",
			erroredApps:  []string{"app12"},
			degradedApps: []string{},
			maxLen:       4,
			expectedInfo: lo.ToPtr("a..."),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := buildAppSummaryInfo(tc.erroredApps, tc.degradedApps, tc.maxLen)

			if tc.expectedInfo == nil {
				require.Nil(result)
			} else {
				require.NotNil(result)
				require.Equal(*tc.expectedInfo, *result)
				require.LessOrEqual(len(*result), tc.maxLen, "result exceeds max length")
			}
		})
	}
}
