package applications

import (
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManager(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name         string
		setupMocks   func(*executer.MockExecuter, *fileio.MockReadWriter)
		current      *v1alpha1.DeviceSpec
		desired      *v1alpha1.DeviceSpec
		wantAppNames []string
	}{
		{
			name:    "no applications",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				// No mock expectations - monitor should not start with no applications
			},
		},
		{
			name:    "add new application",
			current: &v1alpha1.DeviceSpec{},
			desired: newTestDeviceWithApplications(t, "app-new", []testInlineDetails{
				{Content: compose1, Path: "podman-compose.yaml"},
			}),
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				gomock.InOrder(
					// start new app
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-new", true, true),
					mockExecPodmanEvents(mockExec),
				)
			},
			wantAppNames: []string{"app-new"},
		},
		{
			name: "remove existing application",
			current: newTestDeviceWithApplications(t, "app-remove", []testInlineDetails{
				{Content: compose1, Path: "podman-compose.yaml"},
			}),
			desired: &v1alpha1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				id := client.NewComposeID("app-remove")
				gomock.InOrder(
					// start current app
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-remove", true, true),

					// remove current app
					mockExecPodmanNetworkList(mockExec, "app-remove"),
					mockExecPodmanPodList(mockExec, "app-remove"),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"stop", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"rm", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"pod", "rm", "pod123"}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"network", "rm", "network123"}).Return("", "", 0),

					// AfterUpdate call should NOT trigger podman events since there are no applications
					// mockExecPodmanEvents(mockExec),
				)
			},
		},
		{
			name: "update existing application",
			current: newTestDeviceWithApplications(t, "app-update", []testInlineDetails{
				{Content: compose1, Path: "podman-compose.yaml"},
			}),
			desired: newTestDeviceWithApplications(t, "app-update", []testInlineDetails{
				{Content: compose2, Path: "podman-compose.yaml"},
			}),
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				id := client.NewComposeID("app-update")
				gomock.InOrder(
					// start current app
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-update", true, true),

					// stop and remove current app
					mockExecPodmanNetworkList(mockExec, "app-update"),
					mockExecPodmanPodList(mockExec, "app-update"),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"stop", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"rm", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"pod", "rm", "pod123"}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"network", "rm", "network123"}).Return("", "", 0),

					// start desired app
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-update", true, true),
					mockExecPodmanEvents(mockExec),
				)
			},
			wantAppNames: []string{"app-update"},
		},
		{
			name:    "add new quadlet application",
			current: &v1alpha1.DeviceSpec{},
			desired: newTestDeviceWithApplicationType(t, "quadlet-new", []testInlineDetails{
				{Content: quadlet1, Path: "test-app.container"},
			}, v1alpha1.AppTypeQuadlet),
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				// Set up quadlet file mocks
				mockReadQuadletFiles(mockReadWriter, quadlet1)

				gomock.InOrder(
					// start new quadlet app
					mockExecSystemdDaemonReload(mockExec),
					mockExecSystemdStart(mockExec, "test-app.service"),
					mockExecPodmanEvents(mockExec),
				)
			},
			wantAppNames: []string{"quadlet-new"},
		},
		{
			name: "remove existing quadlet application",
			current: newTestDeviceWithApplicationType(t, "quadlet-remove", []testInlineDetails{
				{Content: quadlet1, Path: "test-app.container"},
			}, v1alpha1.AppTypeQuadlet),
			desired: &v1alpha1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				// Set up quadlet file mocks
				mockReadQuadletFiles(mockReadWriter, quadlet1)

				gomock.InOrder(
					// start current quadlet app (Ensure call)
					mockExecSystemdDaemonReload(mockExec),
					mockExecSystemdStart(mockExec, "test-app.service"),

					// remove quadlet app (syncProviders call)
					mockExecSystemdStop(mockExec, "test-app.service"),
					mockExecSystemdDaemonReload(mockExec),

					// no podman events mock needed since no apps remain after removal
				)
			},
		},
		{
			name: "update existing quadlet application",
			current: newTestDeviceWithApplicationType(t, "quadlet-update", []testInlineDetails{
				{Content: quadlet1, Path: "test-app.container"},
			}, v1alpha1.AppTypeQuadlet),
			desired: newTestDeviceWithApplicationType(t, "quadlet-update", []testInlineDetails{
				{Content: quadlet2, Path: "test-app.container"},
			}, v1alpha1.AppTypeQuadlet),
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				// Set up quadlet file mocks - will return different content as needed
				mockReadQuadletFiles(mockReadWriter, quadlet1)
				mockReadQuadletFiles(mockReadWriter, quadlet2)

				gomock.InOrder(
					// start current quadlet app
					mockExecSystemdDaemonReload(mockExec),
					mockExecSystemdStart(mockExec, "test-app.service"),

					// stop current quadlet app
					mockExecSystemdStop(mockExec, "test-app.service"),
					mockExecSystemdDaemonReload(mockExec),

					// start updated quadlet app
					mockExecSystemdDaemonReload(mockExec),
					mockExecSystemdStart(mockExec, "test-app.service"),
					mockExecPodmanEvents(mockExec),
				)
			},
			wantAppNames: []string{"quadlet-update"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			readWriter := fileio.NewReadWriter()
			ctx := context.Background()
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, testutil.NewPollConfig())
			mockSystemdClient := client.NewSystemd(mockExec)

			tmpDir := t.TempDir()
			readWriter.SetRootdir(tmpDir)

			tc.setupMocks(
				mockExec,
				mockReadWriter,
			)

			currentProviders, err := provider.FromDeviceSpec(ctx, log, mockPodmanClient, readWriter, tc.current)
			require.NoError(err)

			manager := &manager{
				readWriter:    readWriter,
				podmanMonitor: NewPodmanMonitor(log, mockPodmanClient, mockSystemdClient, "", mockReadWriter),
				log:           log,
			}

			// ensure the current applications are installed
			for _, provider := range currentProviders {
				err := manager.Ensure(ctx, provider)
				require.NoError(err)
			}

			desiredProviders, err := provider.FromDeviceSpec(ctx, log, mockPodmanClient, readWriter, tc.desired)
			require.NoError(err)

			err = syncProviders(ctx, log, manager, currentProviders, desiredProviders)
			require.NoError(err)

			err = manager.AfterUpdate(ctx)
			require.NoError(err)

			for _, appName := range tc.wantAppNames {
				id := client.NewComposeID(appName)
				log.Debugf("Checking for app: %v", manager.podmanMonitor.apps)
				_, ok := manager.podmanMonitor.apps[id]
				require.True(ok)
			}
			if len(tc.wantAppNames) == 0 {
				require.Empty(manager.podmanMonitor.apps)
			}
		})
	}
}

func TestManagerRemoveApplication(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, testutil.NewPollConfig())
	mockSystemdClient := client.NewSystemd(mockExec)

	readWriter := fileio.NewReadWriter()
	tmpDir := t.TempDir()
	readWriter.SetRootdir(tmpDir)

	current := newTestDeviceWithApplications(t, "app-remove", []testInlineDetails{
		{Content: compose1, Path: "podman-compose.yaml"},
	})
	desired := &v1alpha1.DeviceSpec{}

	id := client.NewComposeID("app-remove")
	gomock.InOrder(
		// start current app
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
		mockExecPodmanComposeUp(mockExec, "app-remove", true, true),

		// Monitor starts when AfterUpdate is called with apps
		mockExecPodmanEvents(mockExec),

		// remove current app during syncProviders
		mockExecPodmanNetworkList(mockExec, "app-remove"),
		mockExecPodmanPodList(mockExec, "app-remove"),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"stop", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"rm", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"pod", "rm", "pod123"}).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"network", "rm", "network123"}).Return("", "", 0),
		// Monitor stops during second AfterUpdate when no apps remain (no mock needed)
	)

	manager := &manager{
		readWriter:    readWriter,
		podmanMonitor: NewPodmanMonitor(log, mockPodmanClient, mockSystemdClient, "", mockReadWriter),
		log:           log,
	}

	// Ensure current applications
	currentProviders, err := provider.FromDeviceSpec(ctx, log, mockPodmanClient, readWriter, current)
	require.NoError(err)
	for _, provider := range currentProviders {
		err := manager.Ensure(ctx, provider)
		require.NoError(err)
	}

	// Start monitor for current apps
	err = manager.AfterUpdate(ctx)
	require.NoError(err)

	// Verify app exists and monitor is running
	require.True(manager.podmanMonitor.Has(id))
	require.True(manager.podmanMonitor.isRunning())

	// Remove applications
	desiredProviders, err := provider.FromDeviceSpec(ctx, log, mockPodmanClient, readWriter, desired)
	require.NoError(err)
	err = syncProviders(ctx, log, manager, currentProviders, desiredProviders)
	require.NoError(err)

	// Stop monitor since no apps remain
	err = manager.AfterUpdate(ctx)
	require.NoError(err)

	// Verify app is removed and monitor is stopped
	require.False(manager.podmanMonitor.Has(id))
	require.False(manager.podmanMonitor.isRunning())
}

func mockExecPodmanEvents(mockExec *executer.MockExecuter) *gomock.Call {
	return mockExec.EXPECT().CommandContext(
		gomock.Any(),
		"podman",
		[]string{
			"events",
			"--format", "json",
			"--since", "", // replace with actual value if needed
			"--filter", "event=create",
			"--filter", "event=init",
			"--filter", "event=start",
			"--filter", "event=stop",
			"--filter", "event=die",
			"--filter", "event=sync",
			"--filter", "event=remove",
			"--filter", "event=exited",
		},
	).Return(exec.CommandContext(context.Background(), "echo", `{}`))
}

func mockExecPodmanComposeUp(mockExec *executer.MockExecuter, name string, hasOverride, hasAgentOverride bool) *gomock.Call {
	workDir := fmt.Sprintf("/etc/compose/manifests/%s", name)
	id := client.NewComposeID(name)
	args := []string{"compose", "-p", id, "-f", "docker-compose.yaml"}
	if hasOverride {
		args = append(args, "-f", "docker-compose.override.yaml")
	}
	if hasAgentOverride {
		args = append(args, "-f", "99-compose-flightctl-agent.override.yaml")
	}
	args = append(args, "up", "-d", "--no-recreate")
	return mockExec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), workDir, "podman", args).Return("", "", 0)
}

func mockExecPodmanNetworkList(mockExec *executer.MockExecuter, name string) *gomock.Call {
	return mockExec.
		EXPECT().
		ExecuteWithContext(
			gomock.Any(),
			"podman",
			[]string{
				"network", "ls",
				"--format", "{{.Network.ID}}",
				"--filter", "label=com.docker.compose.project=" + client.NewComposeID(name),
			},
		).
		Return("network123", "", 0)
}

func mockExecPodmanPodList(mockExec *executer.MockExecuter, name string) *gomock.Call {
	return mockExec.
		EXPECT().
		ExecuteWithContext(
			gomock.Any(),
			"podman",
			[]string{
				"ps", "-a",
				"--format", "{{.Pod}}",
				"--filter", "label=com.docker.compose.project=" + client.NewComposeID(name),
			},
		).
		Return("pod123", "", 0)
}

type testInlineDetails struct {
	Content string
	Path    string
}

func newTestDeviceWithApplications(t *testing.T, name string, details []testInlineDetails) *v1alpha1.DeviceSpec {
	return newTestDeviceWithApplicationType(t, name, details, v1alpha1.AppTypeCompose)
}

func newTestDeviceWithApplicationType(t *testing.T, name string, details []testInlineDetails, appType v1alpha1.AppType) *v1alpha1.DeviceSpec {
	t.Helper()

	inline := v1alpha1.InlineApplicationProviderSpec{
		Inline: make([]v1alpha1.ApplicationContent, len(details)),
	}

	for i, d := range details {
		inline.Inline[i] = v1alpha1.ApplicationContent{
			Content: lo.ToPtr(d.Content),
			Path:    d.Path,
		}
	}

	providerSpec := v1alpha1.ApplicationProviderSpec{
		AppType: lo.ToPtr(appType),
		Name:    lo.ToPtr(name),
	}
	err := providerSpec.FromInlineApplicationProviderSpec(inline)
	require.NoError(t, err)

	applications := []v1alpha1.ApplicationProviderSpec{providerSpec}

	return &v1alpha1.DeviceSpec{
		Applications: &applications,
	}
}

var compose1 = `version: "3.8"
services:
  service1:
    image: quay.io/flightctl-tests/alpine:v1
    command: ["sleep", "infinity"]
  service2:
    image: quay.io/flightctl-tests/alpine:v2
    command: ["sleep", "infinity"]
  service3:
    image: quay.io/flightctl-tests/alpine:v3
    command: ["sleep", "infinity"]
`

var compose2 = `version: "3.8"
services:
  service1:
    image: quay.io/flightctl-tests/alpine:v1
    command: ["sleep", "infinity"]
`

var quadlet1 = `[Container]
Image=quay.io/flightctl-tests/alpine:v1
Exec=sleep infinity

[Service]
Restart=always
`

var quadlet2 = `[Container]
Image=quay.io/flightctl-tests/alpine:v2
Exec=sleep infinity

[Service]
Restart=always
`

func mockExecSystemdDaemonReload(mockExec *executer.MockExecuter) *gomock.Call {
	return mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(),
		"/usr/bin/systemctl",
		[]string{"daemon-reload"},
	).Return("", "", 0)
}

func mockExecSystemdStart(mockExec *executer.MockExecuter, services ...string) *gomock.Call {
	args := append([]string{"start"}, services...)
	return mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(),
		"/usr/bin/systemctl",
		args,
	).Return("", "", 0)
}

func mockExecSystemdStop(mockExec *executer.MockExecuter, services ...string) *gomock.Call {
	args := append([]string{"stop"}, services...)
	return mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(),
		"/usr/bin/systemctl",
		args,
	).Return("", "", 0)
}

func mockReadQuadletFiles(mockReadWriter *fileio.MockReadWriter, quadletContent string) {
	// Mock ReadDir to return .container file
	mockReadWriter.EXPECT().ReadDir(gomock.Any()).Return([]fs.DirEntry{
		&mockDirEntry{name: "test-app.container", isDir: false},
	}, nil).AnyTimes()

	// Mock ReadFile to return quadlet content
	mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return([]byte(quadletContent), nil).AnyTimes()
}

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m *mockDirEntry) Name() string {
	return m.name
}

func (m *mockDirEntry) IsDir() bool {
	return m.isDir
}

func (m *mockDirEntry) Type() fs.FileMode {
	return 0
}

func (m *mockDirEntry) Info() (fs.FileInfo, error) {
	return nil, nil
}
