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

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
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

func streamDataToStdout(t *testing.T, reader io.Reader) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "cat")
	cmd.Stdin = reader
	return cmd
}

func podmanEventsCommandMock(execMock *executer.MockExecuter) *gomock.Call {
	return execMock.EXPECT().CommandContext(gomock.Any(), "podman", "events", gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
}

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
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
			},
			expectedReady:    "1/1",
			expectedStatus:   v1beta1.ApplicationStatusRunning,
			expectedSummary:  v1beta1.ApplicationsSummaryStatusHealthy,
			expectedRestarts: 0,
		},
		{
			name: "single app multiple containers started then one manual stop exit code 0",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "stop"),
			},
			expectedReady:   "1/2",
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "single app multiple containers started then one manual stop result sigkill",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "start"),
				mockPodmanEventError("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "died", 137),
			},
			expectedReady:   "1/2",
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "single app start then die",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "die"),
			},
			expectedReady:   "0/1",
			expectedStatus:  v1beta1.ApplicationStatusError,
			expectedSummary: v1beta1.ApplicationsSummaryStatusError,
		},
		{
			name: "single app multiple containers one error one running",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "die"),
			},
			expectedReady:   "1/2",
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "multiple apps preparing to running",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app2", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app2", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app2", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
			},
			expectedReady:   "1/1",
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app start then removed",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "remove"),
			},
			expectedReady:   "0/0",
			expectedStatus:  v1beta1.ApplicationStatusUnknown,
			expectedSummary: v1beta1.ApplicationsSummaryStatusUnknown,
		},
		{
			name: "app upgrade different service/container counts",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "remove"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "remove"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
			},
			expectedReady:   "1/1",
			expectedStatus:  v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app only creates container no start",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "create"), // no start
			},
			expectedReady:   "1/2",
			expectedStatus:  v1beta1.ApplicationStatusStarting,
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
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			execMock := executer.NewMockExecuter(ctrl)

			mockComposeHandler := lifecycle.NewMockActionHandler(ctrl)
			mockQuadletHandler := lifecycle.NewMockActionHandler(ctrl)
			mockComposeHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			mockQuadletHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

			var testInspect []client.PodmanInspect
			restartsPerContainer := 3
			testInspect = append(testInspect, mockPodmanInspect(restartsPerContainer))
			inspectBytes, err := json.Marshal(testInspect)
			require.NoError(err)

			// create a pipe to simulate events being written to the monitor
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()

			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(string(inspectBytes), "", 0).Times(len(tc.events))
			podmanEventsCommandMock(execMock).Return(streamDataToStdout(t, reader))

			podman := client.NewPodman(log, execMock, rw, util.NewPollConfig())
			systemdMgr := systemd.NewMockManager(ctrl)
			systemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			systemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
				return systemdMgr, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}
			podmanMonitor := NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwFactory)

			podmanMonitor.handlers[v1beta1.AppTypeCompose] = mockComposeHandler
			podmanMonitor.handlers[v1beta1.AppTypeQuadlet] = mockQuadletHandler

			// add test apps to the monitor
			for _, testApp := range tc.apps {
				err := podmanMonitor.Ensure(t.Context(), testApp)
				require.NoError(err)
			}
			err = podmanMonitor.ExecuteActions(t.Context())
			require.NoError(err)

			// simulate events being written to the pipe
			go func() {
				for i := range tc.events {
					event := tc.events[i]
					if err := writeEvent(writer, &event); err != nil {
						t.Errorf("failed to write event: %v", err)
					}
				}
			}()

			timeoutDuration := 5 * time.Second
			retryDuration := 100 * time.Millisecond
			for _, testApp := range tc.apps {
				require.Eventually(func() bool {
					podmanMonitor.mu.Lock()
					app, exists := podmanMonitor.apps[testApp.ID()]
					if !exists {
						podmanMonitor.mu.Unlock()
						t.Logf("app not found: %s", testApp.Name())
						return false
					}
					status, summary, err := app.Status()
					podmanMonitor.mu.Unlock()

					if err != nil {
						t.Logf("error getting status: %v", err)
						return false
					}
					if status == nil {
						t.Logf("app has no status: %s", testApp.Name())
						return false
					}
					if tc.expectedSummary != summary.Status {
						t.Logf("app %s expected summary %s but got %s", testApp.Name(), tc.expectedSummary, summary.Status)
						return false
					}
					if status.Ready != tc.expectedReady {
						t.Logf("app %s expected ready %s but got %s", testApp.Name(), tc.expectedReady, status.Ready)
						return false
					}
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
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			execMock := executer.NewMockExecuter(ctrl)

			podman := client.NewPodman(log, execMock, readWriter, util.NewPollConfig())
			systemdMgr := systemd.NewMockManager(ctrl)
			systemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			systemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
				return systemdMgr, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}
			podmanMonitor := NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwFactory)
			testApp := createTestApplication(require, tc.appName, v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername)

			switch tc.action {
			case "add":
				err := podmanMonitor.Ensure(t.Context(), testApp)
				require.NoError(err)
			case "remove":
				err := podmanMonitor.QueueRemove(testApp)
				require.NoError(err)
			}

			// Check if app is in the monitor under the sanitized name
			_, found := podmanMonitor.apps[tc.expectedName]
			require.Equal(tc.expectedExists, found, "Unexpected app for %s", tc.expectedName)
		})
	}
}

func createTestApplication(require *require.Assertions, name string, status v1beta1.ApplicationStatusType, user v1beta1.Username) Application {
	return createTestApplicationWithType(require, name, status, user, v1beta1.AppTypeCompose)
}

func createTestApplicationWithType(require *require.Assertions, name string, status v1beta1.ApplicationStatusType, user v1beta1.Username, appType v1beta1.AppType) Application {
	provider := newMockProvider(require, name, user, appType)
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

func mockPodmanEventSuccess(name string, username v1beta1.Username, service, status string) client.PodmanEvent {
	return createMockPodmanEvent(name, username, service, status, 0)
}

func mockPodmanEventError(name string, username v1beta1.Username, service, status string, exitCode int) client.PodmanEvent {
	return createMockPodmanEvent(name, username, service, status, exitCode)
}

func createMockPodmanEvent(name string, username v1beta1.Username, service, status string, exitCode int) client.PodmanEvent {
	event := client.PodmanEvent{
		ID:     "8559c630e04ea852101467742e95b9e371fe6dd8c9195910354636d68d388a40",
		Image:  "docker.io/library/alpine:latest",
		Name:   fmt.Sprintf("%s-container", service),
		Status: status,
		Type:   "container",
		Attributes: map[string]string{
			"PODMAN_SYSTEMD_UNIT":                     "podman-compose@user.service",
			"com.docker.compose.container-number":     "1",
			"com.docker.compose.project":              lifecycle.GenerateAppID(name, username),
			"com.docker.compose.project.config_files": "podman-compose.yaml",
			"com.docker.compose.project.working_dir":  path.Join("/usr/local/lib/compose", name),
			"com.docker.compose.service":              service,
			"io.podman.compose.config-hash":           "dc33a4cfdb3cf6b442309e44bd819fcba2ce89393f5d80d6b6b0e9ebb4767e25",
			"io.podman.compose.project":               name,
			"io.podman.compose.version":               "1.0.6",
		},
	}
	event.ContainerExitCode = lo.ToPtr(exitCode)
	return event
}

func mockPodmanInspect(restarts int) client.PodmanInspect {
	return client.PodmanInspect{
		Restarts: restarts,
	}
}

func BenchmarkGenerateAppID(b *testing.B) {
	// bench different string length
	lengths := []int{50, 100, 253}
	for _, size := range lengths {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			input := strings.Repeat("a", size)
			for i := 0; i < b.N; i++ {
				lifecycle.GenerateAppID(input, v1beta1.CurrentProcessUsername)
			}
		})
	}
}

func newMockProvider(require *require.Assertions, name string, username v1beta1.Username, appType v1beta1.AppType) provider.Provider {
	return &mockProvider{name: name, require: require, appType: appType, username: username}
}

type mockProvider struct {
	name     string
	require  *require.Assertions
	appType  v1beta1.AppType
	username v1beta1.Username
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) ID() string {
	return lifecycle.GenerateAppID(m.name, m.username)
}

func (m *mockProvider) Spec() *provider.ApplicationSpec {
	volManager, err := provider.NewVolumeManager(nil, m.name, v1beta1.AppTypeCompose, v1beta1.CurrentProcessUsername, nil)
	m.require.NoError(err)
	return &provider.ApplicationSpec{
		ID:      lifecycle.GenerateAppID(m.name, m.username),
		Name:    m.name,
		Volume:  volManager,
		AppType: m.appType,
		User:    m.username,
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

func (m *mockProvider) EnsureDependencies(ctx context.Context) error {
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

	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	// Unlike the real podman events call, this will emit a single event and then close
	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", gomock.Any()).
		DoAndReturn(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			now := time.Now().UnixNano()
			return exec.CommandContext(ctx, "echo", fmt.Sprintf(`{"timeNano": %d}`, now)) //nolint:gosec
		}).AnyTimes()

	var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
		return mockPodmanClient, nil
	}
	var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
		return systemd.NewMockManager(ctrl), nil
	}
	var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
		return readWriter, nil
	}
	podmanMonitor := NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwFactory)

	// Override lifecycle handlers with no-op mocks to avoid file/exec expectations
	mockComposeHandler := lifecycle.NewMockActionHandler(ctrl)
	mockQuadletHandler := lifecycle.NewMockActionHandler(ctrl)
	mockComposeHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockQuadletHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	podmanMonitor.handlers[v1beta1.AppTypeCompose] = mockComposeHandler
	podmanMonitor.handlers[v1beta1.AppTypeQuadlet] = mockQuadletHandler

	rootlessUser := v1beta1.Username("flightctl")

	// Create test applications
	app1 := createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername)
	app2 := createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername)
	app3 := createTestApplication(require, "app3", v1beta1.ApplicationStatusPreparing, rootlessUser)

	// Application IDs for tracking
	app1ID := app1.ID()
	app2ID := app2.ID()
	app3ID := app3.ID()

	// 1. Start with no applications - verify monitor is not running
	require.False(podmanMonitor.isRunning(v1beta1.CurrentProcessUsername))
	require.False(podmanMonitor.isRunning(rootlessUser))
	require.Equal(0, len(podmanMonitor.apps))

	// 2. Add two applications
	err := podmanMonitor.Ensure(t.Context(), app1)
	require.NoError(err)
	err = podmanMonitor.Ensure(t.Context(), app2)
	require.NoError(err)
	err = podmanMonitor.Ensure(t.Context(), app3)
	require.NoError(err)

	// Execute actions - monitor should start because we have apps
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.True(podmanMonitor.isRunning(v1beta1.CurrentProcessUsername))
	require.True(podmanMonitor.isRunning(rootlessUser))
	require.True(podmanMonitor.Has(app1ID))
	require.True(podmanMonitor.Has(app2ID))
	require.True(podmanMonitor.Has(app3ID))

	// Process an event
	require.Eventually(func() bool {
		return !podmanMonitor.watchers[v1beta1.CurrentProcessUsername].lastEventTime.Load().IsZero()
	}, time.Millisecond*100, 5*time.Millisecond)

	require.Eventually(func() bool {
		return !podmanMonitor.watchers[rootlessUser].lastEventTime.Load().IsZero()
	}, time.Millisecond*100, 5*time.Millisecond)

	// 3. Remove app1 - monitor should still be running
	err = podmanMonitor.QueueRemove(app1)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.True(podmanMonitor.isRunning(v1beta1.CurrentProcessUsername)) // Still running because app2 exists
	require.True(podmanMonitor.isRunning(rootlessUser))                   // Still running because app3 exists
	require.False(podmanMonitor.Has(app1ID))
	require.True(podmanMonitor.Has(app2ID))
	require.True(podmanMonitor.Has(app3ID))

	// 4. Remove app2 - monitor should stop
	err = podmanMonitor.QueueRemove(app2)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.False(podmanMonitor.isRunning(v1beta1.CurrentProcessUsername)) // Stopped because only app3
	require.True(podmanMonitor.isRunning(rootlessUser))                    // Still running because app3 exists
	require.False(podmanMonitor.Has(app1ID))
	require.False(podmanMonitor.Has(app2ID))
	require.True(podmanMonitor.Has(app3ID))

	err = podmanMonitor.QueueRemove(app3)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.False(podmanMonitor.isRunning(rootlessUser)) // Stopped because no apps
	require.False(podmanMonitor.Has(app1ID))
	require.False(podmanMonitor.Has(app2ID))
	require.False(podmanMonitor.Has(app3ID))

	// ensure no panic occurs as the result of stopping an already stopped monitor
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.False(podmanMonitor.isRunning(v1beta1.CurrentProcessUsername))
	require.False(podmanMonitor.isRunning(rootlessUser))

	// 5. Add app1 again - monitor should start
	err = podmanMonitor.Ensure(t.Context(), app1)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.True(podmanMonitor.isRunning(v1beta1.CurrentProcessUsername)) // Started again
	require.True(podmanMonitor.Has(app1ID))

	// 6. Remove app1 final time - monitor should stop
	err = podmanMonitor.QueueRemove(app1)
	require.NoError(err)
	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)
	require.False(podmanMonitor.isRunning(v1beta1.CurrentProcessUsername)) // Stopped again
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

	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", gomock.Any()).
		DoAndReturn(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			now := time.Now().UnixNano()
			return exec.CommandContext(ctx, "echo", fmt.Sprintf(`{"timeNano": %d}`, now)) //nolint:gosec
		}).AnyTimes()

	var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
		return mockPodmanClient, nil
	}
	var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
		return systemd.NewMockManager(ctrl), nil
	}
	var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
		return readWriter, nil
	}
	podmanMonitor := NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwFactory)

	// Create separate mock handlers to track which one gets called
	mockComposeHandler := lifecycle.NewMockActionHandler(ctrl)
	mockQuadletHandler := lifecycle.NewMockActionHandler(ctrl)

	// Track calls to each handler
	var composeActions []lifecycle.Action
	var quadletActions []lifecycle.Action

	mockComposeHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, action lifecycle.Actions) error {
			composeActions = append(composeActions, action...)
			return nil
		}).AnyTimes()
	mockQuadletHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, action lifecycle.Actions) error {
			quadletActions = append(quadletActions, action...)
			return nil
		}).AnyTimes()

	podmanMonitor.handlers[v1beta1.AppTypeCompose] = mockComposeHandler
	podmanMonitor.handlers[v1beta1.AppTypeQuadlet] = mockQuadletHandler

	// Create apps with different types
	composeApp := createTestApplicationWithType(require, "compose-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername, v1beta1.AppTypeCompose)
	quadletApp := createTestApplicationWithType(require, "quadlet-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername, v1beta1.AppTypeQuadlet)

	// Ensure both apps
	err := podmanMonitor.Ensure(t.Context(), composeApp)
	require.NoError(err)
	err = podmanMonitor.Ensure(t.Context(), quadletApp)
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
	err = podmanMonitor.QueueRemove(composeApp)
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

func TestPodmanMonitorDrainSkipsQuadletActions(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, util.NewPollConfig())

	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", gomock.Any()).
		DoAndReturn(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			now := time.Now().UnixNano()
			return exec.CommandContext(ctx, "echo", fmt.Sprintf(`{"timeNano": %d}`, now)) //nolint:gosec
		}).AnyTimes()

	var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
		return mockPodmanClient, nil
	}
	var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
		return systemd.NewMockManager(ctrl), nil
	}
	var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
		return readWriter, nil
	}
	podmanMonitor := NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwFactory)

	mockComposeHandler := lifecycle.NewMockActionHandler(ctrl)
	mockQuadletHandler := lifecycle.NewMockActionHandler(ctrl)

	var composeActions []lifecycle.Action
	var quadletActions []lifecycle.Action

	mockComposeHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, action lifecycle.Actions) error {
			composeActions = append(composeActions, action...)
			return nil
		}).AnyTimes()
	mockQuadletHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, action lifecycle.Actions) error {
			quadletActions = append(quadletActions, action...)
			return nil
		}).AnyTimes()

	podmanMonitor.handlers[v1beta1.AppTypeCompose] = mockComposeHandler
	podmanMonitor.handlers[v1beta1.AppTypeQuadlet] = mockQuadletHandler

	composeApp := createTestApplicationWithType(require, "compose-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername, v1beta1.AppTypeCompose)
	quadletApp := createTestApplicationWithType(require, "quadlet-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername, v1beta1.AppTypeQuadlet)
	containerApp := createTestApplicationWithType(require, "container-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername, v1beta1.AppTypeContainer)

	err := podmanMonitor.Ensure(t.Context(), composeApp)
	require.NoError(err)
	err = podmanMonitor.Ensure(t.Context(), quadletApp)
	require.NoError(err)
	err = podmanMonitor.Ensure(t.Context(), containerApp)
	require.NoError(err)

	err = podmanMonitor.ExecuteActions(ctx)
	require.NoError(err)

	require.Equal(1, len(composeActions), "Compose handler should be called once for add")
	require.Equal(2, len(quadletActions), "Quadlet handler should be called twice for add (quadlet + container)")

	composeActions = nil
	quadletActions = nil

	err = podmanMonitor.Drain(ctx)
	require.NoError(err)

	require.Equal(1, len(composeActions), "Compose handler should be called once for remove during drain")
	require.Equal(0, len(quadletActions), "Quadlet handler should NOT be called during drain (system shutdown)")
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
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
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
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			workloads:      map[string][]Workload{},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expectedInfo:   lo.ToPtr("Not started: app1"),
		},
		{
			name: "two unknown apps returns Degraded with both names",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			workloads:      map[string][]Workload{},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "one healthy one unknown returns Degraded",
			apps: []Application{
				createTestApplication(require, "healthy-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "unknown-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			workloads: map[string][]Workload{
				"healthy-app": {{Name: "container1", Status: StatusRunning}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "two unknown one healthy returns Degraded",
			apps: []Application{
				createTestApplication(require, "unknown1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "unknown2", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "healthy", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			workloads: map[string][]Workload{
				"healthy": {{Name: "container1", Status: StatusRunning}},
			},
			expectedStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "one healthy one error returns Error",
			apps: []Application{
				createTestApplication(require, "healthy-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "error-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
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
				createTestApplication(require, "unknown-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "error-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
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
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "app2", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "app3", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
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
				createTestApplication(require, "healthy-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
				createTestApplication(require, "degraded-app", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
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
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			execMock := executer.NewMockExecuter(ctrl)

			podman := client.NewPodman(testLog, execMock, rw, util.NewPollConfig())
			systemdMgr := systemd.NewMockManager(ctrl)
			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
				return systemdMgr, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}
			podmanMonitor := NewPodmanMonitor(testLog, podmanFactory, systemdFactory, "", rwFactory)

			for _, app := range tc.apps {
				podmanMonitor.apps[app.ID()] = app

				if workloads, ok := tc.workloads[app.Name()]; ok {
					for _, w := range workloads {
						workload := w
						app.AddWorkload(&workload)
					}
				}
			}

			results, err := podmanMonitor.Status()
			require.NoError(err)

			statuses, summary := aggregateAppStatuses(results)

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

func TestAggregateAppStatusesDeterministicOrder(t *testing.T) {
	require := require.New(t)

	makeResult := func(name string, summaryStatus v1beta1.ApplicationsSummaryStatusType) AppStatusResult {
		return AppStatusResult{
			Status:  v1beta1.DeviceApplicationStatus{Name: name},
			Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: summaryStatus},
		}
	}

	// Same logical set of apps in two different input orders (simulating non-deterministic map iteration).
	resultsOrder1 := []AppStatusResult{
		makeResult("nginx-multi-port-server", v1beta1.ApplicationsSummaryStatusError),
		makeResult("app-multi-file-artifact-with-image-ref", v1beta1.ApplicationsSummaryStatusDegraded),
	}
	resultsOrder2 := []AppStatusResult{
		makeResult("app-multi-file-artifact-with-image-ref", v1beta1.ApplicationsSummaryStatusDegraded),
		makeResult("nginx-multi-port-server", v1beta1.ApplicationsSummaryStatusError),
	}

	statuses1, summary1 := aggregateAppStatuses(resultsOrder1)
	statuses2, summary2 := aggregateAppStatuses(resultsOrder2)

	require.Len(statuses1, 2)
	require.Len(statuses2, 2)
	require.Equal(summary1.Status, summary2.Status)
	require.NotNil(summary1.Info)
	require.NotNil(summary2.Info)
	require.Equal(*summary1.Info, *summary2.Info, "summary Info must be identical regardless of input order")

	// Summary lists errored apps first (sorted by name), then degraded apps (sorted by name).
	// Here: one error (nginx-multi-port-server), one degraded (app-multi-file-artifact-with-image-ref).
	expectedInfo := "nginx-multi-port-server is in status Error, app-multi-file-artifact-with-image-ref is in status Degraded"
	require.Equal(expectedInfo, *summary1.Info, "info must be deterministic: errored then degraded, each sorted by app name")
}

func TestReduceActions(t *testing.T) {
	tests := []struct {
		name     string
		actions  []lifecycle.Action
		expected []lifecycle.Action
	}{
		{
			name:     "empty actions returns empty",
			actions:  []lifecycle.Action{},
			expected: []lifecycle.Action{},
		},
		{
			name: "single add action is preserved",
			actions: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1"},
			},
			expected: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1"},
			},
		},
		{
			name: "add followed by remove keeps remove",
			actions: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1"},
				{ID: "app1", Type: lifecycle.ActionRemove, Name: "App 1"},
			},
			expected: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionRemove, Name: "App 1"},
			},
		},
		{
			name: "remove followed by add keeps the add",
			actions: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionRemove, Name: "App 1"},
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1"},
			},
			expected: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1"},
			},
		},
		{
			name: "multiple apps with mixed operations preserves order",
			actions: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1"},
				{ID: "app2", Type: lifecycle.ActionAdd, Name: "App 2"},
				{ID: "app1", Type: lifecycle.ActionRemove, Name: "App 1"},
				{ID: "app3", Type: lifecycle.ActionAdd, Name: "App 3"},
			},
			expected: []lifecycle.Action{
				{ID: "app2", Type: lifecycle.ActionAdd, Name: "App 2"},
				{ID: "app1", Type: lifecycle.ActionRemove, Name: "App 1"},
				{ID: "app3", Type: lifecycle.ActionAdd, Name: "App 3"},
			},
		},
		{
			name: "results are sorted by original queue order not ID",
			actions: []lifecycle.Action{
				{ID: "zebra", Type: lifecycle.ActionAdd, Name: "Zebra"},
				{ID: "alpha", Type: lifecycle.ActionAdd, Name: "Alpha"},
				{ID: "mike", Type: lifecycle.ActionAdd, Name: "Mike"},
			},
			expected: []lifecycle.Action{
				{ID: "zebra", Type: lifecycle.ActionAdd, Name: "Zebra"},
				{ID: "alpha", Type: lifecycle.ActionAdd, Name: "Alpha"},
				{ID: "mike", Type: lifecycle.ActionAdd, Name: "Mike"},
			},
		},
		{
			name: "update action is preserved",
			actions: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionUpdate, Name: "App 1"},
			},
			expected: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionUpdate, Name: "App 1"},
			},
		},
		{
			name: "later action of same type replaces earlier",
			actions: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1 v1"},
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1 v2"},
			},
			expected: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1 v2"},
			},
		},
		{
			name: "complex rollback scenario keeps removes",
			actions: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionAdd, Name: "App 1"},
				{ID: "app2", Type: lifecycle.ActionAdd, Name: "App 2"},
				{ID: "app1", Type: lifecycle.ActionRemove, Name: "App 1"},
				{ID: "app2", Type: lifecycle.ActionRemove, Name: "App 2"},
			},
			expected: []lifecycle.Action{
				{ID: "app1", Type: lifecycle.ActionRemove, Name: "App 1"},
				{ID: "app2", Type: lifecycle.ActionRemove, Name: "App 2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			result := reduceActions(tt.actions)

			require.Equal(len(tt.expected), len(result), "unexpected number of actions")

			for i, expected := range tt.expected {
				require.Equal(expected.ID, result[i].ID, "unexpected ID at index %d", i)
				require.Equal(expected.Type, result[i].Type, "unexpected Type at index %d", i)
				require.Equal(expected.Name, result[i].Name, "unexpected Name at index %d", i)
			}
		})
	}
}

func TestReduceActions_Consistency(t *testing.T) {
	require := require.New(t)

	actions := []lifecycle.Action{
		{ID: "z", Type: lifecycle.ActionAdd, Name: "Z"},
		{ID: "m", Type: lifecycle.ActionAdd, Name: "M"},
		{ID: "a", Type: lifecycle.ActionAdd, Name: "A"},
		{ID: "f", Type: lifecycle.ActionAdd, Name: "F"},
	}

	for i := 0; i < 100; i++ {
		result := reduceActions(actions)

		ids := make([]string, len(result))
		for j, action := range result {
			ids[j] = action.ID
		}
		require.Equal([]string{"z", "m", "a", "f"}, ids,
			"ordering should be consistent on iteration %d", i)
	}
}
