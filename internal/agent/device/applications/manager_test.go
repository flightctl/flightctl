package applications

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
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
					mockExecComposePodmanVolumeList(mockExec, "app-remove"),
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
					mockExecComposePodmanVolumeList(mockExec, "app-update"),

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
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5", "", 0).AnyTimes()
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
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5", "", 0).AnyTimes()
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
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{target}).Return(
						[]client.SystemDUnitListEntry{{Unit: target, LoadState: "loaded"}}, nil),
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
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5", "", 0).AnyTimes()
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
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{target}).Return(
						[]client.SystemDUnitListEntry{{Unit: target, LoadState: "loaded"}}, nil),
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
			mockPodmanEventConfigFiles(mockReadWriter)
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
				kubernetesMonitor: NewKubernetesMonitor(log, cliClients, rwFactory, nil),
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

func TestManagerUpdateAfterRestart(t *testing.T) {
	testCases := []struct {
		name    string
		current string
		desired string
	}{
		{
			name:    "When a Compose image changes while the agent is stopped it should update the running application",
			current: compose1,
			desired: compose2,
		},
		{
			name:    "When a Compose update is rolled back after restart it should restore the previous application",
			current: compose2,
			desired: compose1,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			ctx := t.Context()
			logger := log.NewPrefixLogger("test")
			bootTime := time.Now()
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockPodmanEventConfigFiles(mockReadWriter)
			podman := client.NewPodman(logger, mockExec, mockReadWriter, testutil.NewPollConfig())
			podmanFactory := func(v1beta1.Username) (*client.Podman, error) { return podman, nil }
			tempDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
			)
			rwFactory := func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil }
			rwMockFactory := func(v1beta1.Username) (fileio.ReadWriter, error) { return mockReadWriter, nil }
			const appName = "app-restart"
			current := newTestDeviceWithApplications(t, appName, []testInlineDetails{
				{Content: tc.current, Path: "podman-compose.yaml"},
			})
			desired := newTestDeviceWithApplications(t, appName, []testInlineDetails{
				{Content: tc.desired, Path: "podman-compose.yaml"},
			})
			currentProviders, err := provider.FromDeviceSpec(ctx, logger, podmanFactory, nil, rwFactory, current)
			require.NoError(err)
			require.Len(currentProviders, 1)
			// Keep the installed files, but start with a fresh monitor as on agent restart.
			require.NoError(currentProviders[0].Install(ctx))
			desiredProviders, err := provider.FromDeviceSpec(ctx, logger, podmanFactory, nil, rwFactory, desired)
			require.NoError(err)
			m := &manager{
				podmanMonitor:     NewPodmanMonitor(logger, podmanFactory, nil, bootTime.Format(time.RFC3339), rwMockFactory),
				kubernetesMonitor: NewKubernetesMonitor(logger, client.NewCLIClients(), rwFactory, nil),
				log:               logger,
			}
			t.Cleanup(func() { require.NoError(m.podmanMonitor.Stop()) })
			id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
			require.False(m.podmanMonitor.Has(id))
			mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes()
			gomock.InOrder(
				mockExecPodmanNetworkList(mockExec, appName),
				mockExecPodmanPodList(mockExec, appName),
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "stop", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "rm", "--filter", "label=com.docker.compose.project="+id).Return("", "", 0),
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pod", "rm", "pod123").Return("", "", 0),
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "network", "rm", "network123").Return("", "", 0),
				mockExecComposePodmanVolumeList(mockExec, appName),
				mockExecPodmanComposeUp(mockExec, appName, true, true),
				mockExecPodmanEvents(mockExec, bootTime),
			)

			require.NoError(syncProviders(ctx, logger, m, currentProviders, desiredProviders))
			require.True(m.podmanMonitor.Has(id))
			require.NoError(m.AfterUpdate(ctx))
			require.True(m.podmanMonitor.isRunning(v1beta1.CurrentProcessUsername))
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
	mockPodmanEventConfigFiles(mockReadWriter)
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
		mockExecComposePodmanVolumeList(mockExec, "app-remove"),
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
		kubernetesMonitor: NewKubernetesMonitor(log, cliClients, rwFactory, nil),
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

func TestWhenDeltaStatusPersistenceFailsItShouldStillRemoveTheApplication(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	logger := log.NewPrefixLogger("test")
	rw := fileio.NewMockReadWriter(ctrl)
	rw.EXPECT().ReadFile("/var/lib/flightctl/delta-status.json").Return(nil, os.ErrNotExist)
	rw.EXPECT().MkdirAll("/var/lib/flightctl", gomock.Any()).Return(nil).AnyTimes()
	rw.EXPECT().WriteFile("/var/lib/flightctl/delta-status.json", gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("disk full")).AnyTimes()
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)

	appSpec := &provider.ApplicationSpec{Name: "app", ID: "app-id", AppType: v1beta1.AppTypeContainer}
	appProvider := provider.NewMockProvider(ctrl)
	appProvider.EXPECT().Spec().Return(appSpec).AnyTimes()
	appProvider.EXPECT().Name().Return("app").AnyTimes()
	appProvider.EXPECT().Remove(gomock.Any()).Return(nil)
	appProvider.EXPECT().EnsureDependencies(gomock.Any()).Return(errors.ErrAppDependency)

	manager := &manager{log: logger, deltaStatusStore: store}
	require.NoError(t, manager.Remove(context.Background(), appProvider))
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
			"--filter", "event=health_status",
		},
	).Return(exec.CommandContext(context.Background(), "echo", `{}`))
}

func mockPodmanEventConfigFiles(mockReadWriter *fileio.MockReadWriter) {
	const configDir = "/tmp/podman-events"
	configPath := filepath.Join(configDir, client.PodmanEventsConfigFilename)
	mockReadWriter.EXPECT().WriteFile(gomock.Any(), []byte(client.PodmanEventsConfigContents), fileio.DefaultFilePermissions).Return(nil).AnyTimes()
	mockReadWriter.EXPECT().MkdirTemp("flightctl-podman-events-").Return(configDir, nil).AnyTimes()
	mockReadWriter.EXPECT().PathFor(configPath).Return(configPath).AnyTimes()
	mockReadWriter.EXPECT().RemoveAll(configDir).Return(nil).AnyTimes()
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
	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", args).Return(exec.Command("podman")).AnyTimes()
	return mockExec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), workDir, "podman", args, gomock.Any()).Return("", "", 0)
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

func mockExecComposePodmanVolumeList(mockExec *executer.MockExecuter, name string) *gomock.Call {
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	return mockExec.
		EXPECT().
		ExecuteWithContext(
			gomock.Any(),
			"podman",
			[]string{
				"volume", "ls",
				"--format", "json",
				"--filter", "label=com.docker.compose.project=" + id,
			},
		).
		Return("[]", "", 0)
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

func mockExecQuadletPodmanVolumeList(mockExec *executer.MockExecuter, name string) *gomock.Call {
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	return mockExec.
		EXPECT().
		ExecuteWithContext(
			gomock.Any(),
			"podman",
			[]string{
				"volume", "ls",
				"--format", "json",
				"--filter", "label=io.flightctl.quadlet.project=" + id,
				"--filter", "name=" + id + "-*",
			},
		).
		Return("[]", "", 0)
}

func mockExecQuadletCleanup(mockExec *executer.MockExecuter, name string) {
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	mockExecQuadletPodmanNetworkList(mockExec, name)
	mockExecQuadletPodmanPodList(mockExec, name)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "stop", "--filter", "label=io.flightctl.quadlet.project="+id).Return("", "", 0)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "rm", "--filter", "label=io.flightctl.quadlet.project="+id).Return("", "", 0)
	mockExecQuadletPodmanVolumeList(mockExec, name)
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

func TestWhenRetainedParentExtractionIsMissingItShouldRebuildNestedTargets(t *testing.T) {
	const (
		application  = "compose-app"
		image        = "quay.io/acme/compose:latest"
		imageID      = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		parentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "mount"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "mount", "docker-compose.yaml"), []byte("services:\n  web:\n    image: quay.io/acme/web:v1\n"), 0o600))
	logger := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	var mounts int
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, args ...string) (string, string, int) {
			if len(args) >= 2 && args[0] == "image" && args[1] == "exists" {
				return "", "", 0
			}
			if len(args) >= 3 && args[0] == "image" && args[1] == "inspect" {
				switch args[2] {
				case "--format":
					if len(args) == 5 && args[3] == "{{.Digest}}" {
						return parentDigest, "", 0
					}
					if len(args) == 5 && args[3] == "{{.Id}}" {
						return imageID, "", 0
					}
				}
			}
			if len(args) >= 5 && args[0] == "unshare" && args[1] == "podman" && args[2] == "image" && args[3] == "mount" {
				mounts++
				return "/mount", "", 0
			}
			return "", "", 0
		},
	).AnyTimes()
	podman := client.NewPodman(logger, mockExec, rw, testutil.NewPollConfig())
	var app v1beta1.ComposeApplication
	app.Name = lo.ToPtr(application)
	app.AppType = v1beta1.AppTypeCompose
	require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
	var appSpec v1beta1.ApplicationProviderSpec
	require.NoError(t, appSpec.FromComposeApplication(app))
	desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{appSpec}}
	manager := &manager{
		log:            logger,
		podmanFactory:  func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		rwFactory:      func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil },
		clients:        client.NewCLIClients(),
		ociTargetCache: provider.NewOCITargetCache(),
		appDataCache:   provider.NewAppDataCache(),
	}

	first, err := manager.CollectOCITargets(context.Background(), nil, desired)
	require.NoError(t, err)
	var firstReferences []string
	for _, target := range first.Targets.Iter() {
		firstReferences = append(firstReferences, target.Reference)
	}
	require.Contains(t, firstReferences, "quay.io/acme/web:v1")
	require.Len(t, manager.appDataCache, 1)
	for _, appData := range manager.appDataCache {
		require.NoError(t, rw.RemoveAll(appData.TmpPath))
	}

	second, err := manager.CollectOCITargets(context.Background(), desired, desired)
	require.NoError(t, err)
	var secondReferences []string
	for _, target := range second.Targets.Iter() {
		secondReferences = append(secondReferences, target.Reference)
	}
	require.Contains(t, secondReferences, "quay.io/acme/web:v1")
	require.Equal(t, 2, mounts)
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
				mockPullConfigResolver := dependency.NewMockPullConfigResolver(ctrl)
				mockPullConfigResolver.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption { return nil }).AnyTimes()
				return &manager{
					rwFactory:          rwFactory,
					podmanMonitor:      NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwMockFactory),
					podmanFactory:      podmanFactory,
					clients:            mockClients,
					log:                log,
					ociTargetCache:     provider.NewOCITargetCache(),
					appDataCache:       provider.NewAppDataCache(),
					pullConfigResolver: mockPullConfigResolver,
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
				mockPullConfigResolver := dependency.NewMockPullConfigResolver(ctrl)
				mockPullConfigResolver.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption { return nil }).AnyTimes()
				return &manager{
					rwFactory:          rwFactory,
					podmanMonitor:      NewPodmanMonitor(log, podmanFactory, systemdFactory, "", rwMockFactory),
					podmanFactory:      podmanFactory,
					log:                log,
					ociTargetCache:     provider.NewOCITargetCache(),
					appDataCache:       provider.NewAppDataCache(),
					clients:            mockClients,
					pullConfigResolver: mockPullConfigResolver,
				}
			},
			expectError:   true,
			errorContains: "extracting nested targets",
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

func TestVerifyProvidersDeferredDependencies(t *testing.T) {
	tests := []struct {
		name            string
		osUpdatePending bool
		setupProviders  func(ctrl *gomock.Controller) []provider.Provider
		wantErr         bool
		wantErrContains string
	}{
		{
			name:            "os update pending - defers ErrAppDependency from Verify",
			osUpdatePending: true,
			setupProviders: func(ctrl *gomock.Controller) []provider.Provider {
				mockProvider := provider.NewMockProvider(ctrl)
				mockProvider.EXPECT().Name().Return("helm-app").AnyTimes()
				mockProvider.EXPECT().Verify(gomock.Any()).Return(
					fmt.Errorf("%w: helm binary not found", errors.ErrAppDependency))
				return []provider.Provider{mockProvider}
			},
			wantErr: false,
		},
		{
			name:            "no os update pending - ErrAppDependency from Verify returns error",
			osUpdatePending: false,
			setupProviders: func(ctrl *gomock.Controller) []provider.Provider {
				mockProvider := provider.NewMockProvider(ctrl)
				mockProvider.EXPECT().Name().Return("helm-app").AnyTimes()
				mockProvider.EXPECT().Verify(gomock.Any()).Return(
					fmt.Errorf("%w: helm binary not found", errors.ErrAppDependency))
				return []provider.Provider{mockProvider}
			},
			wantErr:         true,
			wantErrContains: "helm binary not found",
		},
		{
			name:            "os update pending - one deferred, one succeeds",
			osUpdatePending: true,
			setupProviders: func(ctrl *gomock.Controller) []provider.Provider {
				helmProvider := provider.NewMockProvider(ctrl)
				helmProvider.EXPECT().Name().Return("helm-app").AnyTimes()
				helmProvider.EXPECT().Verify(gomock.Any()).Return(
					fmt.Errorf("%w: helm binary not found", errors.ErrAppDependency))

				containerProvider := provider.NewMockProvider(ctrl)
				containerProvider.EXPECT().Name().Return("container-app").AnyTimes()
				containerProvider.EXPECT().Verify(gomock.Any()).Return(nil)

				return []provider.Provider{helmProvider, containerProvider}
			},
			wantErr: false,
		},
		{
			name:            "non-deferrable error - returns immediately",
			osUpdatePending: true,
			setupProviders: func(ctrl *gomock.Controller) []provider.Provider {
				mockProvider := provider.NewMockProvider(ctrl)
				mockProvider.EXPECT().Name().Return("helm-app").AnyTimes()
				mockProvider.EXPECT().Verify(gomock.Any()).Return(
					fmt.Errorf("critical error: invalid spec"))
				return []provider.Provider{mockProvider}
			},
			wantErr:         true,
			wantErrContains: "invalid spec",
		},
		{
			name:            "all providers succeed",
			osUpdatePending: false,
			setupProviders: func(ctrl *gomock.Controller) []provider.Provider {
				p1 := provider.NewMockProvider(ctrl)
				p1.EXPECT().Name().Return("app1").AnyTimes()
				p1.EXPECT().Verify(gomock.Any()).Return(nil)

				p2 := provider.NewMockProvider(ctrl)
				p2.EXPECT().Name().Return("app2").AnyTimes()
				p2.EXPECT().Verify(gomock.Any()).Return(nil)

				return []provider.Provider{p1, p2}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			providers := tt.setupProviders(ctrl)

			m := &manager{
				log:             log,
				osUpdatePending: tt.osUpdatePending,
			}

			err := m.verifyProviders(ctx, providers)

			if tt.wantErr {
				require.Error(err)
				if tt.wantErrContains != "" {
					require.Contains(err.Error(), tt.wantErrContains)
				}
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestCollectOCITargetsDeferredDependencies(t *testing.T) {
	tests := []struct {
		name            string
		osUpdatePending bool
		setupMocks      func(ctrl *gomock.Controller, mockExec *executer.MockExecuter)
		wantErr         error
	}{
		{
			name:            "os update pending - defers ErrAppDependency",
			osUpdatePending: true,
			setupMocks: func(ctrl *gomock.Controller, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "--version",
				).Return("podman version 4.0.0", "", 0).AnyTimes()
			},
			wantErr: nil,
		},
		{
			name:            "no os update pending - ErrAppDependency returns error",
			osUpdatePending: false,
			setupMocks: func(ctrl *gomock.Controller, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "--version",
				).Return("podman version 4.0.0", "", 0).AnyTimes()
			},
			wantErr: errors.ErrAppDependency,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			tempDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
			)

			mockExec := executer.NewMockExecuter(ctrl)
			mockPodmanClient := client.NewPodman(log, mockExec, readWriter, testutil.NewPollConfig())

			if tt.setupMocks != nil {
				tt.setupMocks(ctrl, mockExec)
			}

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return mockPodmanClient, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}

			cliClients := client.NewCLIClients()
			m := &manager{
				log:            log,
				podmanFactory:  podmanFactory,
				rwFactory:      rwFactory,
				clients:        cliClients,
				ociTargetCache: provider.NewOCITargetCache(),
				appDataCache:   provider.NewAppDataCache(),
			}

			var quadletApp v1beta1.QuadletApplication
			err := quadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content: lo.ToPtr(quadlet1),
						Path:    "test-app.container",
					},
				},
			})
			require.NoError(err)
			quadletApp.Name = lo.ToPtr("test-quadlet")
			quadletApp.AppType = v1beta1.AppTypeQuadlet

			var providerSpec v1beta1.ApplicationProviderSpec
			err = providerSpec.FromQuadletApplication(quadletApp)
			require.NoError(err)

			desired := &v1beta1.DeviceSpec{
				Applications: &[]v1beta1.ApplicationProviderSpec{providerSpec},
			}

			result, err := m.CollectOCITargets(ctx, &v1beta1.DeviceSpec{}, desired,
				dependency.WithOSUpdatePending(tt.osUpdatePending))

			if tt.wantErr != nil {
				require.Error(err)
				require.True(errors.Is(err, tt.wantErr))
			} else {
				require.NoError(err)
				require.NotNil(result)
			}
		})
	}
}

func TestManagerResolveConsole(t *testing.T) {
	newTestManager := func(t *testing.T) (*manager, *gomock.Controller) {
		t.Helper()
		ctrl := gomock.NewController(t)
		testLog := log.NewPrefixLogger("test")
		tmpDir := t.TempDir()
		readWriter := fileio.NewReadWriter(
			fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
			fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
		)
		execMock := executer.NewMockExecuter(ctrl)
		podman := client.NewPodman(testLog, execMock, readWriter, testutil.NewPollConfig())
		systemdMgr := systemd.NewMockManager(ctrl)
		systemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
		systemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
		var podmanFactory client.PodmanFactory = func(_ v1beta1.Username) (*client.Podman, error) { return podman, nil }
		var systemdFactory systemd.ManagerFactory = func(_ v1beta1.Username) (systemd.Manager, error) { return systemdMgr, nil }
		var rwFactory fileio.ReadWriterFactory = func(_ v1beta1.Username) (fileio.ReadWriter, error) { return readWriter, nil }
		m := &manager{
			podmanMonitor:     NewPodmanMonitor(testLog, podmanFactory, systemdFactory, "", rwFactory),
			kubernetesMonitor: NewKubernetesMonitor(testLog, client.NewCLIClients(), rwFactory, nil),
			log:               testLog,
		}
		return m, ctrl
	}

	t.Run("When the app is tracked by podmanMonitor it should return a session from podmanMonitor", func(t *testing.T) {
		require := require.New(t)
		m, ctrl := newTestManager(t)
		defer ctrl.Finish()
		app := createTestVMApplication(require, "my-vm", v1beta1.ApplicationStatusRunning, v1beta1.CurrentProcessUsername)
		err := m.podmanMonitor.Ensure(t.Context(), app)
		require.NoError(err)
		// The workload name comes from podman events and is the authoritative runtime name.
		app.AddWorkload(&Workload{Name: "systemd-my-vm-compute", Status: StatusRunning})
		sess, err := m.resolveConsole("my-vm", "serial")
		require.NoError(err)
		require.NotNil(sess)
	})

	t.Run("When the app is not found in any monitor it should return an error", func(t *testing.T) {
		require := require.New(t)
		m, ctrl := newTestManager(t)
		defer ctrl.Finish()
		_, err := m.resolveConsole("ghost-app", "serial")
		require.Error(err)
		require.Contains(err.Error(), "not found in any monitor")
	})
}

func mockExecPodmanComposeStop(mockExec *executer.MockExecuter, name string) *gomock.Call {
	workDir := fmt.Sprintf("%s/%s", lifecycle.ComposeAppPath, name)
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	args := []string{"compose", "-p", id, "stop"}
	return mockExec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), workDir, "podman", args).Return("", "", 0)
}

func mockExecPodmanComposeStart(mockExec *executer.MockExecuter, name string) *gomock.Call {
	return mockExecPodmanComposeUp(mockExec, name, true, true)
}

func mockExecPodmanComposeStartService(mockExec *executer.MockExecuter, name, service string) *gomock.Call {
	workDir := fmt.Sprintf("%s/%s", lifecycle.ComposeAppPath, name)
	id := lifecycle.GenerateAppID(name, v1beta1.CurrentProcessUsername)
	args := []string{
		"compose", "-p", id,
		"-f", "docker-compose.yaml",
		"-f", "docker-compose.override.yaml",
		"-f", "99-compose-flightctl-agent.override.yaml",
		"up", "-d", "--no-recreate", service,
	}
	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", args).Return(exec.Command("podman")).AnyTimes()
	return mockExec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), workDir, "podman", args, gomock.Any()).Return("", "", 0)
}

func TestQueueLifecycle(t *testing.T) {
	bootTime := time.Now()
	const appName = "my-app"

	type lifecycleIntent struct {
		desiredState v1beta1.ApplicationDesiredState
		restartGen   int
	}

	testCases := []struct {
		name            string
		stored          lifecycleIntent
		first           lifecycleIntent
		second          *lifecycleIntent
		stoppedWorkload StatusType
		stoppedService  string
		workloads       []Workload
		setupFirst      func(*executer.MockExecuter)
		setupSecond     func(*executer.MockExecuter)
	}{
		{
			name:       "When desiredState transitions from running to stopped it should call podman compose stop",
			stored:     lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:      lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			setupFirst: func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStop(mockExec, appName) },
		},
		{
			name:   "When desiredState is unchanged (running) it should not issue any call",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			// no mock expectation
		},
		{
			name:        "When desiredState transitions from stopped to running it should call podman compose up",
			stored:      lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:       lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			second:      &lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			setupFirst:  func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStop(mockExec, appName) },
			setupSecond: func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStart(mockExec, appName) },
		},
		{
			name:   "When restartGeneration increments it should restart the app via compose up",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning, restartGen: 0},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning, restartGen: 0},
			second: &lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning, restartGen: 1},
			// no action on first call (same gen)
			setupSecond: func(mockExec *executer.MockExecuter) {
				gomock.InOrder(
					mockExecPodmanComposeStop(mockExec, appName),
					mockExecPodmanComposeStart(mockExec, appName),
				)
			},
		},
		{
			name:       "When restartGeneration increments but desiredState is stopped it should not restart",
			stored:     lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:      lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			second:     &lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped, restartGen: 1},
			setupFirst: func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStop(mockExec, appName) },
			// no second call: restart must not fire when desiredState is stopped
		},
		{
			name:            "When a running app has a stopped workload it should call podman compose up",
			stored:          lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:           lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			stoppedWorkload: StatusStop,
			setupFirst:      func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStart(mockExec, appName) },
		},
		{
			name:            "When a running app has a workload with die status it should call podman compose up",
			stored:          lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:           lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			stoppedWorkload: StatusDie,
			setupFirst:      func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStart(mockExec, appName) },
		},
		{
			name:            "When a running app has a workload with died status it should call podman compose up",
			stored:          lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:           lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			stoppedWorkload: StatusDied,
			setupFirst:      func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStart(mockExec, appName) },
		},
		{
			name:   "When a running app has a failed Compose service it should not restart a completed one-shot service",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			workloads: []Workload{
				{Name: "init-container", ServiceName: "init", Status: StatusExited},
				{Name: "server-container", ServiceName: "server", Status: StatusDied},
			},
			setupFirst: func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStartService(mockExec, appName, "server") },
		},
		{
			name:            "When a running app has a removed workload it should call podman compose up",
			stored:          lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:           lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			stoppedWorkload: StatusRemove,
			setupFirst:      func(mockExec *executer.MockExecuter) { mockExecPodmanComposeStart(mockExec, appName) },
		},
		{
			name:            "When a running app has an exited workload it should not restart",
			stored:          lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:           lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			stoppedWorkload: StatusExited,
		},
		{
			name:            "When a stopped app has a terminated workload it should not restart",
			stored:          lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			first:           lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			stoppedWorkload: StatusDied,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()
			testLog := log.NewPrefixLogger("test")
			testLog.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockPodmanEventConfigFiles(mockReadWriter)
			mockExec := executer.NewMockExecuter(ctrl)
			mockPodmanClient := client.NewPodman(testLog, mockExec, mockReadWriter, testutil.NewPollConfig())
			mockSystemdMgr := systemd.NewMockManager(ctrl)
			mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()

			// PathExists is called during compose Up for start/restart
			mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes()

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return mockPodmanClient, nil
			}
			var systemdFactory systemd.ManagerFactory = func(user v1beta1.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}
			var rwMockFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return mockReadWriter, nil
			}

			monitor := NewPodmanMonitor(testLog, podmanFactory, systemdFactory, bootTime.Format(time.RFC3339), rwMockFactory)

			id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
			volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeCompose, v1beta1.CurrentProcessUsername, nil)
			require.NoError(err)

			// Pre-register the app with the "stored" (previously converged) lifecycle intent.
			monitor.apps[id] = &application{
				id:   id,
				path: fmt.Sprintf("%s/%s", lifecycle.ComposeAppPath, appName),
				status: &v1beta1.DeviceApplicationStatus{
					Name:    appName,
					AppType: v1beta1.AppTypeCompose,
				},
				volume:            volumeManager,
				desiredState:      tc.stored.desiredState,
				restartGeneration: tc.stored.restartGen,
			}
			if len(tc.workloads) > 0 {
				for i := range tc.workloads {
					monitor.apps[id].AddWorkload(&tc.workloads[i])
				}
			} else if tc.stoppedWorkload != "" {
				monitor.apps[id].AddWorkload(&Workload{Name: "container", ServiceName: tc.stoppedService, Status: tc.stoppedWorkload})
			}

			// First spec sync: queue lifecycle action based on the delta from stored state.
			if tc.setupFirst != nil {
				tc.setupFirst(mockExec)
			}
			monitor.QueueLifecycle(id, tc.first.desiredState, tc.first.restartGen)
			if tc.stoppedService != "" {
				require.Equal([]string{tc.stoppedService}, monitor.actions[0].StartServices)
			}
			err = monitor.ExecuteActions(ctx)
			require.NoError(err)

			if tc.second == nil {
				return
			}

			// Second spec sync: queue lifecycle action based on the delta from the first.
			if tc.setupSecond != nil {
				tc.setupSecond(mockExec)
			}
			monitor.QueueLifecycle(id, tc.second.desiredState, tc.second.restartGen)
			err = monitor.ExecuteActions(ctx)
			require.NoError(err)
		})
	}
}

func TestWhenDeltaLifetimePersistenceFailsItShouldStillInstallApplications(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update bool
	}{
		{name: "When lifetime persistence fails during Ensure it should install the application"},
		{name: "When lifetime persistence fails during Update it should install the application", update: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			logger := log.NewPrefixLogger("test")
			stateRW := fileio.NewMockReadWriter(ctrl)
			stateRW.EXPECT().ReadFile(gomock.Any()).Return(nil, os.ErrNotExist)
			stateRW.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			stateRW.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("state directory is read only")).AnyTimes()
			store := deltastatus.New(stateRW, "/state", logger)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			binDir := t.TempDir()
			for _, name := range []string{"podman", "podman-compose", "skopeo"} {
				require.NoError(os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o600))
				require.NoError(os.Chmod(filepath.Join(binDir, name), 0o700))
			}
			t.Setenv("PATH", binDir)
			podman := client.NewPodman(logger, executer.NewMockExecuter(ctrl), rw, testutil.NewPollConfig())
			podmanFactory := func(v1beta1.Username) (*client.Podman, error) { return podman, nil }
			rwFactory := func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil }
			desired := newTestDeviceWithApplications(t, "app", []testInlineDetails{{Content: compose1, Path: "podman-compose.yaml"}})
			providers, err := provider.FromDeviceSpec(t.Context(), logger, podmanFactory, nil, rwFactory, desired)
			require.NoError(err)
			require.Len(providers, 1)
			clients := client.NewCLIClients()
			manager := &manager{log: logger, deltaStatusStore: store,
				podmanMonitor:     NewPodmanMonitor(logger, podmanFactory, nil, time.Now().Format(time.RFC3339), rwFactory),
				kubernetesMonitor: NewKubernetesMonitor(logger, clients, rwFactory, nil)}
			if tc.update {
				require.NoError(manager.Update(t.Context(), providers[0]))
			} else {
				require.NoError(manager.Ensure(t.Context(), providers[0]))
			}
			exists, err := rw.PathExists(filepath.Join(providers[0].Spec().Path, "podman-compose.yaml"))
			require.NoError(err)
			require.True(exists)
			require.Empty(providers[0].Spec().Lifetime, "unpersisted identity must not be used as evidence")
		})
	}
}

func TestWhenInstalledParentDiffersFromCachedParentItShouldReportTheInstalledDigest(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	logger := log.NewPrefixLogger("test")
	binDir := t.TempDir()
	for _, name := range []string{"podman", "podman-compose", "skopeo"} {
		require.NoError(os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o600))
		require.NoError(os.Chmod(filepath.Join(binDir, name), 0o700))
	}
	t.Setenv("PATH", binDir)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	require.NoError(rw.MkdirAll("/parent", 0o700))
	require.NoError(rw.WriteFile("/parent/compose.yaml", []byte("services: {}\n"), 0o600))
	const parent = "quay.io/acme/bundle:latest"
	const installedDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	app := v1beta1.ComposeApplication{Name: lo.ToPtr("app"), AppType: v1beta1.AppTypeCompose}
	require.NoError(app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: parent}))
	var apiSpec v1beta1.ApplicationProviderSpec
	require.NoError(apiSpec.FromComposeApplication(app))
	podman := client.NewPodman(logger, executer.NewMockExecuter(ctrl), rw, testutil.NewPollConfig())
	providers, err := provider.FromDeviceSpec(t.Context(), logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil }, nil,
		func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil },
		&v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{apiSpec}},
		provider.WithAppDataCache(map[string]*provider.AppData{"app-337945": {
			TmpPath: "/parent", ParentReference: parent, ParentDigest: installedDigest, ParentOCIType: dependency.OCITypePodmanArtifact,
		}}))
	require.NoError(err)
	cache := provider.NewOCITargetCache()
	cache.Set(provider.CacheEntry{Name: providers[0].ID(), Parent: dependency.OCIPullTarget{
		Reference: parent, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Type: dependency.OCITypePodmanArtifact,
	}})
	m := &manager{log: logger, ociTargetCache: cache,
		podmanMonitor:     &PodmanMonitor{apps: make(map[string]Application)},
		kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(logger, "kubernetes")},
	}
	require.NoError(m.Ensure(t.Context(), providers[0]))
	status := &v1beta1.DeviceStatus{}
	require.NoError(m.Status(t.Context(), status))
	require.Len(status.Applications, 1)
	require.NotNil(status.Applications[0].ImageDigests)
	require.Contains(*status.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: parent, Digest: installedDigest})
}
