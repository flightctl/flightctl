package applications

import (
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManager(t *testing.T) {
	bootTime := time.Now()

	require := require.New(t)
	testCases := []struct {
		name         string
		setupMocks   func(*executer.MockExecuter, *fileio.MockReadWriter, *systemd.MockManager)
		current      *v1beta1.DeviceSpec
		desired      *v1beta1.DeviceSpec
		wantAppNames []string
	}{
		{
			name:    "no applications",
			current: &v1beta1.DeviceSpec{},
			desired: &v1beta1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter, mockSystemdMgr *systemd.MockManager) {
				// No mock expectations - monitor should not start with no applications
			},
		},
		{
			name:    "add new application",
			current: &v1beta1.DeviceSpec{},
			desired: newTestDeviceWithApplications(t, "app-new", []testInlineDetails{
				{Content: compose1, Path: "podman-compose.yaml"},
			}),
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter, mockSystemdMgr *systemd.MockManager) {
				gomock.InOrder(
					// start new app
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-new", true, true),
					mockExecPodmanEvents(mockExec, bootTime),
				)
			},
			wantAppNames: []string{"app-new"},
		},
		{
			name: "remove existing application",
			current: newTestDeviceWithApplications(t, "app-remove", []testInlineDetails{
				{Content: compose1, Path: "podman-compose.yaml"},
			}),
			desired: &v1beta1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter, mockSystemdMgr *systemd.MockManager) {
				id := lifecycle.GenerateAppID("app-remove", v1beta1.CurrentProcessUsername)
				gomock.InOrder(
					// start current app (first AfterUpdate)
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-remove", true, true),
					mockExecPodmanEvents(mockExec, bootTime),

					// remove current app (second AfterUpdate after syncProviders)
					mockExecPodmanNetworkList(mockExec, "app-remove"),
					mockExecPodmanPodList(mockExec, "app-remove"),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "stop", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "rm", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pod", "rm", "pod123").Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "network", "rm", "network123").Return("", "", 0),
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
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter, mockSystemdMgr *systemd.MockManager) {
				id := lifecycle.GenerateAppID("app-update", v1beta1.CurrentProcessUsername)
				gomock.InOrder(
					// start current app (first AfterUpdate)
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-update", true, true),
					mockExecPodmanEvents(mockExec, bootTime),

					// stop and remove current app (second AfterUpdate after syncProviders)
					mockExecPodmanNetworkList(mockExec, "app-update"),
					mockExecPodmanPodList(mockExec, "app-update"),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "stop", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "rm", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pod", "rm", "pod123").Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "network", "rm", "network123").Return("", "", 0),

					// start desired app (monitor already running, no new podman events command)
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-update", true, true),
				)
			},
			wantAppNames: []string{"app-update"},
		},
		{
			name:    "add new quadlet application",
			current: &v1beta1.DeviceSpec{},
			desired: newTestDeviceWithApplicationType(t, "quadlet-new", []testInlineDetails{
				{Content: quadlet1, Path: "test-app.container"},
			}, v1beta1.AppTypeQuadlet),
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter, mockSystemdMgr *systemd.MockManager) {
				mockReadQuadletFiles(mockReadWriter, quadlet1)
				appID := lifecycle.GenerateAppID("quadlet-new", v1beta1.CurrentProcessUsername)
				target := appID + "-flightctl-quadlet-app.target"
				services := []string{appID + "-test-app.service"}

				gomock.InOrder(
					mockExecSystemdDaemonReload(mockSystemdMgr),
					mockExecSystemdListDependencies(mockSystemdMgr, appID, services),
					mockExecSystemdListUnitsWithResults(mockSystemdMgr, services...),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), target).Return(nil),
					mockExecPodmanEvents(mockExec, bootTime),
				)
			},
			wantAppNames: []string{"quadlet-new"},
		},
		{
			name: "remove existing quadlet application",
			current: newTestDeviceWithApplicationType(t, "quadlet-remove", []testInlineDetails{
				{Content: quadlet1, Path: "test-app.container"},
			}, v1beta1.AppTypeQuadlet),
			desired: &v1beta1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter, mockSystemdMgr *systemd.MockManager) {
				mockReadQuadletFiles(mockReadWriter, quadlet1)
				appID := lifecycle.GenerateAppID("quadlet-remove", v1beta1.CurrentProcessUsername)
				target := appID + "-flightctl-quadlet-app.target"
				services := []string{appID + "-test-app.service"}

				gomock.InOrder(
					// start current quadlet app (first AfterUpdate)
					mockExecSystemdDaemonReload(mockSystemdMgr),
					mockExecSystemdListDependencies(mockSystemdMgr, appID, services),
					mockExecSystemdListUnitsWithResults(mockSystemdMgr, services...),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), target).Return(nil),
					mockExecPodmanEvents(mockExec, bootTime),

					// remove quadlet app (second AfterUpdate after syncProviders)
					mockExecSystemdListDependencies(mockSystemdMgr, appID, services),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), target).Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), services).Return([]client.SystemDUnitListEntry{{Unit: services[0], LoadState: "loaded"}}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), services[0]).Return(nil),
					mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), services[0]).Return(nil),
				)
				mockExecQuadletCleanup(mockExec, "quadlet-remove")
				mockExecSystemdDaemonReload(mockSystemdMgr)
			},
		},
		{
			name: "update existing quadlet application",
			current: newTestDeviceWithApplicationType(t, "quadlet-update", []testInlineDetails{
				{Content: quadlet1, Path: "test-app.container"},
			}, v1beta1.AppTypeQuadlet),
			desired: newTestDeviceWithApplicationType(t, "quadlet-update", []testInlineDetails{
				{Content: quadlet2, Path: "test-app.container"},
			}, v1beta1.AppTypeQuadlet),
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter, mockSystemdMgr *systemd.MockManager) {
				mockReadQuadletFiles(mockReadWriter, quadlet1)
				mockReadQuadletFiles(mockReadWriter, quadlet2)
				appID := lifecycle.GenerateAppID("quadlet-update", v1beta1.CurrentProcessUsername)
				target := appID + "-flightctl-quadlet-app.target"
				services := []string{appID + "-test-app.service"}

				gomock.InOrder(
					// start current quadlet app (first AfterUpdate)
					mockExecSystemdDaemonReload(mockSystemdMgr),
					mockExecSystemdListDependencies(mockSystemdMgr, appID, services),
					mockExecSystemdListUnitsWithResults(mockSystemdMgr, services...),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), target).Return(nil),
					mockExecPodmanEvents(mockExec, bootTime),

					// update: stop current quadlet app (remove phase)
					mockExecSystemdListDependencies(mockSystemdMgr, appID, services),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), target).Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), services).Return([]client.SystemDUnitListEntry{{Unit: services[0], LoadState: "loaded"}}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), services[0]).Return(nil),
					mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), services[0]).Return(nil),
				)
				mockExecQuadletCleanup(mockExec, "quadlet-update")
				gomock.InOrder(
					// start updated quadlet app (add phase after daemon reload)
					mockExecSystemdDaemonReload(mockSystemdMgr),
					mockExecSystemdListDependencies(mockSystemdMgr, appID, services),
					mockExecSystemdListUnitsWithResults(mockSystemdMgr, services...),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), target).Return(nil),
				)
			},
			wantAppNames: []string{"quadlet-update"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
			)

			ctx := context.Background()
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, testutil.NewPollConfig())
			mockSystemdMgr := systemd.NewMockManager(ctrl)
			mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()

			tc.setupMocks(
				mockExec,
				mockReadWriter,
				mockSystemdMgr,
			)

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return mockPodmanClient, nil
			}
			var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}
			var rwMockFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return mockReadWriter, nil
			}

			currentProviders, err := provider.FromDeviceSpec(ctx, log, podmanFactory, nil, rwFactory, tc.current)
			require.NoError(err)

			cliClients := client.NewCLIClients()
			manager := &manager{
				rwFactory:         rwFactory,
				podmanMonitor:     NewPodmanMonitor(log, podmanFactory, systemdFactory, bootTime.Format(time.RFC3339), rwMockFactory),
				kubernetesMonitor: NewKubernetesMonitor(log, cliClients, rwFactory),
				clients:           cliClients,
				log:               log,
			}

			// ensure the current applications are installed
			for _, provider := range currentProviders {
				err := manager.Ensure(ctx, provider)
				require.NoError(err)
			}

			// execute actions to install the current applications before syncing
			err = manager.AfterUpdate(ctx)
			require.NoError(err)

			desiredProviders, err := provider.FromDeviceSpec(ctx, log, podmanFactory, nil, rwFactory, tc.desired)
			require.NoError(err)

			err = syncProviders(ctx, log, manager, currentProviders, desiredProviders)
			require.NoError(err)

			err = manager.AfterUpdate(ctx)
			require.NoError(err)

			for _, appName := range tc.wantAppNames {
				id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
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
	bootTime := time.Now()

	require := require.New(t)

	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, testutil.NewPollConfig())
	mockSystemdMgr := systemd.NewMockManager(ctrl)
	mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
	mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()

	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	current := newTestDeviceWithApplications(t, "app-remove", []testInlineDetails{
		{Content: compose1, Path: "podman-compose.yaml"},
	})
	desired := &v1beta1.DeviceSpec{}

	id := lifecycle.GenerateAppID("app-remove", v1beta1.CurrentProcessUsername)
	gomock.InOrder(
		// start current app
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
		mockExecPodmanComposeUp(mockExec, "app-remove", true, true),

		// Monitor starts when AfterUpdate is called with apps
		mockExecPodmanEvents(mockExec, bootTime),

		// remove current app during syncProviders
		mockExecPodmanNetworkList(mockExec, "app-remove"),
		mockExecPodmanPodList(mockExec, "app-remove"),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "stop", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "rm", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pod", "rm", "pod123").Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "network", "rm", "network123").Return("", "", 0),
		// Monitor stops during second AfterUpdate when no apps remain (no mock needed)
	)

	var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
		return mockPodmanClient, nil
	}
	var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
		return mockSystemdMgr, nil
	}
	var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
		return readWriter, nil
	}
	var rwMockFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
		return mockReadWriter, nil
	}
	cliClients := client.NewCLIClients()
	manager := &manager{
		rwFactory:         rwFactory,
		podmanMonitor:     NewPodmanMonitor(log, podmanFactory, systemdFactory, bootTime.Format(time.RFC3339), rwMockFactory),
		kubernetesMonitor: NewKubernetesMonitor(log, cliClients, rwFactory),
		log:               log,
	}

	// Ensure current applications
	currentProviders, err := provider.FromDeviceSpec(ctx, log, podmanFactory, nil, rwFactory, current)
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
	require.True(manager.podmanMonitor.isRunning(v1beta1.CurrentProcessUsername))

	// Remove applications
	desiredProviders, err := provider.FromDeviceSpec(ctx, log, podmanFactory, nil, rwFactory, desired)
	require.NoError(err)
	err = syncProviders(ctx, log, manager, currentProviders, desiredProviders)
	require.NoError(err)

	// Stop monitor since no apps remain
	err = manager.AfterUpdate(ctx)
	require.NoError(err)

	// Verify app is removed and monitor is stopped
	require.False(manager.podmanMonitor.Has(id))
	require.False(manager.podmanMonitor.isRunning(v1beta1.CurrentProcessUsername))
}

func mockExecPodmanEvents(mockExec *executer.MockExecuter, sinceTime time.Time) *gomock.Call {
	return mockExec.EXPECT().CommandContext(
		gomock.Any(),
		"podman",
		[]string{
			"events",
			"--format", "json",
			"--since", sinceTime.Format(time.RFC3339),
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
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
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
				"--filter", "label=com.docker.compose.project=" + lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername),
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
				"--filter", "label=com.docker.compose.project=" + lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername),
			},
		).
		Return("pod123", "", 0)
}

type testInlineDetails struct {
	Content string
	Path    string
}

func newTestDeviceWithApplications(t *testing.T, name string, details []testInlineDetails) *v1beta1.DeviceSpec {
	return newTestDeviceWithApplicationType(t, name, details, v1beta1.AppTypeCompose)
}

func newTestDeviceWithApplicationType(t *testing.T, name string, details []testInlineDetails, appType v1beta1.AppType) *v1beta1.DeviceSpec {
	t.Helper()

	inlineSpec := v1beta1.InlineApplicationProviderSpec{
		Inline: make([]v1beta1.ApplicationContent, len(details)),
	}

	for i, d := range details {
		inlineSpec.Inline[i] = v1beta1.ApplicationContent{
			Content: lo.ToPtr(d.Content),
			Path:    d.Path,
		}
	}

	var providerSpec v1beta1.ApplicationProviderSpec

	var err error
	switch appType {
	case v1beta1.AppTypeCompose:
		var composeApp v1beta1.ComposeApplication
		err = composeApp.FromInlineApplicationProviderSpec(inlineSpec)
		require.NoError(t, err)
		composeApp.AppType = appType
		composeApp.Name = lo.ToPtr(name)
		err = providerSpec.FromComposeApplication(composeApp)
	case v1beta1.AppTypeQuadlet:
		var quadletApp v1beta1.QuadletApplication
		err = quadletApp.FromInlineApplicationProviderSpec(inlineSpec)
		require.NoError(t, err)
		quadletApp.AppType = appType
		quadletApp.Name = lo.ToPtr(name)
		require.NoError(t, err)
		err = providerSpec.FromQuadletApplication(quadletApp)
		require.NoError(t, err)
	default:
		t.Fatalf("unsupported app type for inline: %s", appType)
	}
	require.NoError(t, err)

	applications := []v1beta1.ApplicationProviderSpec{providerSpec}

	return &v1beta1.DeviceSpec{
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

func mockExecSystemdDaemonReload(mockSystemdMgr *systemd.MockManager) *gomock.Call {
	return mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)
}

func mockExecSystemdListUnitsWithResults(mockSystemdMgr *systemd.MockManager, services ...string) *gomock.Call {
	units := make([]client.SystemDUnitListEntry, len(services))
	for i, svc := range services {
		units[i] = client.SystemDUnitListEntry{Unit: svc, LoadState: "loaded"}
	}
	return mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), services).Return(units, nil)
}

func mockExecSystemdListDependencies(mockSystemdMgr *systemd.MockManager, appID string, services []string) *gomock.Call {
	target := fmt.Sprintf("%s-flightctl-quadlet-app.target", appID)
	return mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), target).Return(services, nil)
}

func mockExecQuadletPodmanNetworkList(mockExec *executer.MockExecuter, name string) *gomock.Call {
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	return mockExec.
		EXPECT().
		ExecuteWithContext(
			gomock.Any(),
			"podman",
			[]string{
				"network", "ls",
				"--format", "{{.Network.ID}}",
				"--filter", "label=io.flightctl.quadlet.project=" + id,
				"--filter", "name=" + id + "-*",
			},
		).
		Return("", "", 0)
}

func mockExecQuadletPodmanPodList(mockExec *executer.MockExecuter, name string) *gomock.Call {
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	return mockExec.
		EXPECT().
		ExecuteWithContext(
			gomock.Any(),
			"podman",
			[]string{
				"ps", "-a",
				"--format", "{{.Pod}}",
				"--filter", "label=io.flightctl.quadlet.project=" + id,
			},
		).
		Return("", "", 0)
}

func mockExecQuadletCleanup(mockExec *executer.MockExecuter, name string) {
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	mockExecQuadletPodmanNetworkList(mockExec, name)
	mockExecQuadletPodmanPodList(mockExec, name)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "stop", "--filter", "label=io.flightctl.quadlet.project="+id).Return("", "", 0)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "rm", "--filter", "label=io.flightctl.quadlet.project="+id).Return("", "", 0)
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

func TestCollectOCITargetsCache(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	cache := provider.NewOCITargetCache()

	// populate cache with nested targets for two applications
	nestedTargets := []dependency.OCIPullTarget{
		{Type: dependency.OCITypePodmanImage, Reference: "quay.io/nested/image1:v1"},
		{Type: dependency.OCITypePodmanImage, Reference: "quay.io/nested/image2:v1"},
	}

	entry1 := provider.CacheEntry{
		Name:     "app1",
		Owner:    "flightctl",
		Parent:   dependency.OCIPullTarget{Reference: "quay.io/parent:v1", Digest: "sha256:digest1"},
		Children: nestedTargets,
	}
	entry2 := provider.CacheEntry{
		Name:     "app2",
		Parent:   dependency.OCIPullTarget{Reference: "quay.io/parent:v2", Digest: "sha256:digest2"},
		Children: nestedTargets,
	}

	cache.Set(entry1)
	cache.Set(entry2)

	// verify cache retrieval
	require.Equal(2, cache.Len())

	cachedEntry1, found := cache.Get("app1")
	require.True(found)
	require.Len(cachedEntry1.Children, 2)
	require.Equal("sha256:digest1", cachedEntry1.Parent.Digest)

	cachedEntry2, found := cache.Get("app2")
	require.True(found)
	require.Len(cachedEntry2.Children, 2)
	require.Equal("sha256:digest2", cachedEntry2.Parent.Digest)

	// test GC removes unreferenced applications
	cache.GC([]string{"app1"})
	require.Equal(1, cache.Len())

	_, found = cache.Get("app1")
	require.True(found, "app1 should remain")

	_, found = cache.Get("app2")
	require.False(found, "app2 should be removed by GC")

	// test clear
	cache.Clear()
	require.Equal(0, cache.Len())
}

func TestCollectOCITargetsErrorHandling(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	testCases := []struct {
		name          string
		setupManager  func(*testing.T) *manager
		expectError   bool
		errorContains string
		isRetryable   bool
		expectRequeue bool
	}{
		{
			name: "base image not available - returns base targets with Requeue=true",
			setupManager: func(t *testing.T) *manager {
				ctrl := gomock.NewController(t)
				mockReadWriter := fileio.NewMockReadWriter(ctrl)
				mockExec := executer.NewMockExecuter(ctrl)

				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"podman",
					"image", "exists", "quay.io/test/image:v1",
				).Return("", "", 1).AnyTimes() // exit code 1 = does not exist
				// artifact check should also fail when base image is missing locally
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"podman",
					"artifact", "inspect", "quay.io/test/image:v1",
				).Return("", "", 1).AnyTimes()

				mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, testutil.NewPollConfig())
				mockSystemdMgr := systemd.NewMockManager(ctrl)
				mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
				mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
				tempDir := t.TempDir()
				readWriter := fileio.NewReadWriter(
					fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
					fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
				)

				var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
					return mockPodmanClient, nil
				}
				var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
					return mockSystemdMgr, nil
				}

				var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
					return readWriter, nil
				}
				var rwMockFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
					return mockReadWriter, nil
				}
				mockClients := client.NewCLIClients()
				return &manager{
					rwFactory:      rwFactory,
					podmanMonitor:  NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwMockFactory),
					podmanFactory:  podmanFactory,
					clients:        mockClients,
					log:            log,
					ociTargetCache: provider.NewOCITargetCache(),
					appDataCache:   provider.NewAppDataCache(),
				}
			},
			expectError:   false,
			expectRequeue: true,
		},
		{
			name: "hard failure during extraction - fails immediately",
			setupManager: func(t *testing.T) *manager {
				ctrl := gomock.NewController(t)
				mockReadWriter := fileio.NewMockReadWriter(ctrl)
				mockExec := executer.NewMockExecuter(ctrl)

				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"podman",
					"image", "exists", "quay.io/test/image:v1",
				).Return("", "", 0).AnyTimes() // exit code 0 = exists

				// expect ImageDigest call (which will fail in this test)
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"podman",
					"image", "inspect", "--format", "{{.Digest}}", "quay.io/test/image:v1",
				).Return("", "fatal error: disk full", 1).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"podman",
					gomock.Any(),
				).Return("", "fatal error: disk full", 1).AnyTimes()

				mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, testutil.NewPollConfig())
				mockSystemdMgr := systemd.NewMockManager(ctrl)
				mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
				mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
				tempDir := t.TempDir()
				readWriter := fileio.NewReadWriter(
					fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
					fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
				)

				var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
					return mockPodmanClient, nil
				}
				var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
					return mockSystemdMgr, nil
				}
				var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
					return readWriter, nil
				}
				var rwMockFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
					return mockReadWriter, nil
				}
				mockClients := client.NewCLIClients()
				return &manager{
					rwFactory:      rwFactory,
					podmanMonitor:  NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwMockFactory),
					podmanFactory:  podmanFactory,
					log:            log,
					ociTargetCache: provider.NewOCITargetCache(),
					appDataCache:   provider.NewAppDataCache(),
					clients:        mockClients,
				}
			},
			expectError:   true,
			errorContains: "collecting nested OCI targets",
			isRetryable:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			manager := tc.setupManager(t)

			var composeApp v1beta1.ComposeApplication
			_ = composeApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{
				Image: "quay.io/test/image:v1",
			})
			composeApp.Name = lo.ToPtr("test-app")
			composeApp.AppType = v1beta1.AppTypeCompose
			var providerSpec v1beta1.ApplicationProviderSpec
			_ = providerSpec.FromComposeApplication(composeApp)
			spec := &v1beta1.DeviceSpec{
				Applications: &[]v1beta1.ApplicationProviderSpec{providerSpec},
			}

			result, err := manager.CollectOCITargets(ctx, &v1beta1.DeviceSpec{}, spec)

			if tc.expectError {
				require.Error(err)
				require.Nil(result)
				if tc.errorContains != "" {
					require.Contains(err.Error(), tc.errorContains)
				}
				if tc.isRetryable {
					require.True(errors.IsRetryable(err), "Error should be retryable, got: %v", err)
				} else {
					require.False(errors.IsRetryable(err), "Error should NOT be retryable, got: %v", err)
				}
			} else {
				require.NoError(err)
				require.NotNil(result)
				if tc.expectRequeue {
					require.True(result.Requeue, "Expected Requeue=true, got false")
					require.NotEmpty(result.Targets, "Expected base targets to be returned")
				}
			}
		})
	}
}
