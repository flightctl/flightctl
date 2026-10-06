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
	"github.com/flightctl/flightctl/pkg/poll"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestAddVolumeImageDigests(t *testing.T) {
	testCases := []struct {
		name         string
		nilFactory   bool
		factoryError bool
		volumes      []v1beta1.ApplicationVolumeStatus
		existing     []v1beta1.ApplicationImageDigest
		setupMocks   func(*executer.MockExecuter)
		want         []v1beta1.ApplicationImageDigest
	}{
		{
			name:       "When the factory is nil it should preserve existing digests",
			nilFactory: true,
			volumes:    []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}},
			existing:   []v1beta1.ApplicationImageDigest{{Image: "app:v1", Digest: "sha256:app"}},
			want:       []v1beta1.ApplicationImageDigest{{Image: "app:v1", Digest: "sha256:app"}},
		},
		{
			name:         "When the factory fails it should skip volume digests",
			factoryError: true,
			volumes:      []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}, {Reference: "other:v1"}},
		},
		{
			name:    "When ImageDigest fails it should skip the failed volume",
			volumes: []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}, {Reference: "volume:v1"}},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("", "inspect failed", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", "volume:v1").Return("", "artifact inspection unavailable", 125)
			},
		},
		{
			name:     "When volumes repeat an existing digest it should deduplicate and inspect once",
			volumes:  []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}, {Reference: "volume:v1"}},
			existing: []v1beta1.ApplicationImageDigest{{Image: "volume:v1", Digest: "sha256:volume"}},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:volume", "", 0)
			},
			want: []v1beta1.ApplicationImageDigest{{Image: "volume:v1", Digest: "sha256:volume"}},
		},
		{
			name:     "When images and digests are unsorted it should sort by image then digest",
			volumes:  []v1beta1.ApplicationVolumeStatus{{Reference: "z:v1"}, {Reference: "a:v1"}},
			existing: []v1beta1.ApplicationImageDigest{{Image: "a:v1", Digest: "sha256:z"}},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "z:v1").Return("sha256:z", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "a:v1").Return("sha256:a", "", 0)
			},
			want: []v1beta1.ApplicationImageDigest{{Image: "a:v1", Digest: "sha256:a"}, {Image: "a:v1", Digest: "sha256:z"}, {Image: "z:v1", Digest: "sha256:z"}},
		},
		{
			name:    "When a volume image is present it should add its digest and skip empty references",
			volumes: []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}, {}},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:volume", "", 0)
			},
			want: []v1beta1.ApplicationImageDigest{{Image: "volume:v1", Digest: "sha256:volume"}},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			if testCase.setupMocks != nil {
				testCase.setupMocks(mockExec)
			}
			logger := log.NewPrefixLogger("test")
			manager := &manager{log: logger}
			factoryCalls := 0
			if !testCase.nilFactory {
				manager.podmanFactory = func(v1beta1.Username) (*client.Podman, error) {
					factoryCalls++
					if testCase.factoryError {
						return nil, fmt.Errorf("factory failed")
					}
					return client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{}), nil
				}
			}
			results := []AppStatusResult{{Status: v1beta1.DeviceApplicationStatus{Volumes: &testCase.volumes}}}
			if testCase.existing != nil {
				results[0].Status.ImageDigests = &testCase.existing
			}
			manager.addVolumeImageDigests(context.Background(), results)
			if testCase.want == nil {
				require.Nil(results[0].Status.ImageDigests)
			} else {
				require.NotNil(results[0].Status.ImageDigests)
				require.Equal(testCase.want, *results[0].Status.ImageDigests)
			}
			if !testCase.nilFactory {
				require.Equal(1, factoryCalls)
			}
		})
	}
}

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
				kubernetesMonitor: NewKubernetesMonitor(logger, client.NewCLIClients(), rwFactory),
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
			"--filter", "event=health_status",
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
			kubernetesMonitor: NewKubernetesMonitor(testLog, client.NewCLIClients(), rwFactory),
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

func TestQueueLifecycle(t *testing.T) {
	bootTime := time.Now()
	const appName = "my-app"

	type lifecycleIntent struct {
		desiredState v1beta1.ApplicationDesiredState
		restartGen   int
	}

	testCases := []struct {
		name        string
		stored      lifecycleIntent
		first       lifecycleIntent
		second      *lifecycleIntent
		setupFirst  func(*executer.MockExecuter)
		setupSecond func(*executer.MockExecuter)
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

			// First spec sync: queue lifecycle action based on the delta from stored state.
			if tc.setupFirst != nil {
				tc.setupFirst(mockExec)
			}
			monitor.QueueLifecycle(id, tc.first.desiredState, tc.first.restartGen)
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

func TestVolumeImageDigestCacheLifecycle(t *testing.T) {
	testCases := []struct {
		name      string
		reconcile func(context.Context, *manager) error
	}{
		{name: "When status repeats it should reuse the volume digest"},
		{name: "When steady-state BeforeUpdate repeats it should reuse the volume digest", reconcile: func(ctx context.Context, m *manager) error { return m.BeforeUpdate(ctx, &v1beta1.DeviceSpec{}) }},
		{name: "When steady-state AfterUpdate repeats it should reuse the volume digest", reconcile: func(ctx context.Context, m *manager) error { return m.AfterUpdate(ctx) }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			m := &manager{
				log:               logger,
				podmanMonitor:     NewPodmanMonitor(logger, nil, nil, time.Now().Format(time.RFC3339), nil),
				kubernetesMonitor: NewKubernetesMonitor(logger, nil, nil),
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
					return client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{}), nil
				},
			}
			p := provider.NewMockProvider(ctrl)
			p.EXPECT().Spec().Return(&provider.ApplicationSpec{ID: "existing-app", Name: "existing-app", AppType: v1beta1.AppTypeCompose}).AnyTimes()
			p.EXPECT().EnsureDependencies(gomock.Any()).Return(nil).AnyTimes()
			m.podmanMonitor.apps["existing-app"] = NewApplication(p)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:old", "", 0)
			ctx := context.Background()
			collect := func(want string) {
				volumes := []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}}
				results := []AppStatusResult{{Status: v1beta1.DeviceApplicationStatus{Volumes: &volumes}}}
				m.addVolumeImageDigests(ctx, results)
				require.Equal([]v1beta1.ApplicationImageDigest{{Image: "volume:v1", Digest: want}}, *results[0].Status.ImageDigests)
			}
			collect("sha256:old")
			collect("sha256:old")
			if tc.reconcile != nil {
				for range 3 {
					require.NoError(tc.reconcile(ctx, m))
					require.NoError(m.Ensure(ctx, p))
					collect("sha256:old")
				}
			}
		})
	}
}

func TestVolumeImageDigestCacheRetries(t *testing.T) {
	testCases := []struct {
		name, stdout, stderr string
		exitCode             int
	}{
		{name: "When inspecting fails it should retry on the next status tick", stderr: "inspect failed", exitCode: 1},
		{name: "When a digest is empty it should retry on the next status tick"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			m := &manager{log: logger, podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
				return client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{}), nil
			}}
			first := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return(tc.stdout, tc.stderr, tc.exitCode)
			if tc.exitCode != 0 {
				first = mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", "volume:v1").Return("", "artifact inspection unavailable", 125).After(first)
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:volume", "", 0).After(first)
			volumes := []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}}
			results := []AppStatusResult{{Status: v1beta1.DeviceApplicationStatus{Volumes: &volumes}}}
			m.addVolumeImageDigests(context.Background(), results)
			require.Nil(results[0].Status.ImageDigests)
			m.addVolumeImageDigests(context.Background(), results)
			require.Equal([]v1beta1.ApplicationImageDigest{{Image: "volume:v1", Digest: "sha256:volume"}}, *results[0].Status.ImageDigests)
		})
	}
}

func TestVolumeImageDigestCacheRunAs(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	logger := log.NewPrefixLogger("test")
	factoryCalls := 0
	m := &manager{log: logger, podmanFactory: func(user v1beta1.Username) (*client.Podman, error) {
		factoryCalls++
		mockExec := executer.NewMockExecuter(ctrl)
		if user == "alice" {
			gomock.InOrder(
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("", "no such image", 125),
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", "volume:v1").Return("{}", "", 0),
			)
		} else {
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:"+string(user), "", 0)
		}
		return client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{}), nil
	}}
	volumes := []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}}
	for range 2 {
		results := []AppStatusResult{
			{Status: v1beta1.DeviceApplicationStatus{RunAs: "alice", Volumes: &volumes}},
			{Status: v1beta1.DeviceApplicationStatus{RunAs: "bob", Volumes: &volumes}},
		}
		m.addVolumeImageDigests(context.Background(), results)
		require.Nil(results[0].Status.ImageDigests)
		require.Equal([]v1beta1.ApplicationImageDigest{{Image: "volume:v1", Digest: "sha256:bob"}}, *results[1].Status.ImageDigests)
	}
	require.Equal(2, factoryCalls)
}

func TestVolumeImageDigestCacheArtifacts(t *testing.T) {
	testCases := []struct {
		name      string
		reconcile func(context.Context, *manager) error
	}{
		{name: "When a volume is an artifact it should skip inspection on later status ticks"},
		{name: "When steady-state BeforeUpdate repeats it should retain the cached artifact", reconcile: func(ctx context.Context, m *manager) error { return m.BeforeUpdate(ctx, &v1beta1.DeviceSpec{}) }},
		{name: "When steady-state AfterUpdate repeats it should retain the cached artifact", reconcile: func(ctx context.Context, m *manager) error { return m.AfterUpdate(ctx) }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			m := &manager{
				log:               logger,
				podmanMonitor:     NewPodmanMonitor(logger, nil, nil, time.Now().Format(time.RFC3339), nil),
				kubernetesMonitor: NewKubernetesMonitor(logger, nil, nil),
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
					return client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{}), nil
				},
			}
			gomock.InOrder(
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("", "no such image", 125),
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", "volume:v1").Return("{}", "", 0),
			)
			ctx := context.Background()
			collect := func() []AppStatusResult {
				volumes := []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}, {Reference: "volume:v1"}}
				results := []AppStatusResult{{Status: v1beta1.DeviceApplicationStatus{Volumes: &volumes}}}
				m.addVolumeImageDigests(ctx, results)
				return results
			}
			require.Nil(collect()[0].Status.ImageDigests)
			require.Nil(collect()[0].Status.ImageDigests)
			if tc.reconcile != nil {
				for range 3 {
					require.NoError(tc.reconcile(ctx, m))
					require.Nil(collect()[0].Status.ImageDigests)
				}
			}
		})
	}
}

func TestVolumeImageDigestCacheApplicationChanges(t *testing.T) {
	for _, operation := range []string{"install", "update", "remove"} {
		t.Run("When an application "+operation+" changes images it should refresh cached digests", func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			m := &manager{
				log:               logger,
				podmanMonitor:     NewPodmanMonitor(logger, nil, nil, time.Now().Format(time.RFC3339), nil),
				kubernetesMonitor: NewKubernetesMonitor(logger, nil, nil),
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
					return client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{}), nil
				},
			}
			volumes, err := provider.NewVolumeManager(logger, "app", v1beta1.AppTypeCompose, "", nil)
			require.NoError(err)
			p := provider.NewMockProvider(ctrl)
			p.EXPECT().Spec().Return(&provider.ApplicationSpec{ID: "app", Name: "app", AppType: v1beta1.AppTypeCompose, Volume: volumes}).AnyTimes()
			p.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
			handler := lifecycle.NewMockActionHandler(ctrl)
			m.podmanMonitor.handlers[v1beta1.AppTypeCompose] = handler
			ctx := context.Background()
			collect := func(digest string) {
				volumes := []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}}
				results := []AppStatusResult{{Status: v1beta1.DeviceApplicationStatus{Volumes: &volumes}}}
				m.addVolumeImageDigests(ctx, results)
				require.Equal([]v1beta1.ApplicationImageDigest{{Image: "volume:v1", Digest: digest}}, *results[0].Status.ImageDigests)
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:old", "", 0)
			collect("sha256:old")
			switch operation {
			case "install":
				p.EXPECT().Install(gomock.Any()).Return(fmt.Errorf("install failed"))
				require.Error(m.Ensure(ctx, p))
			case "update":
				p.EXPECT().Remove(gomock.Any()).Return(nil)
				p.EXPECT().Install(gomock.Any()).Return(nil)
				require.NoError(m.Update(ctx, p))
				handler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(fmt.Errorf("action failed"))
				// The Podman failure must not drop invalidation for Helm actions
				// that remain queued until the next AfterUpdate call.
				pending := provider.NewMockProvider(ctrl)
				pending.EXPECT().Spec().Return(&provider.ApplicationSpec{ID: "pending-app", Name: "pending-app", AppType: v1beta1.AppTypeHelm, Volume: volumes}).AnyTimes()
				pending.EXPECT().Remove(gomock.Any()).Return(nil)
				pending.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
				m.kubernetesMonitor.apps["pending-app"] = NewHelmApplication(pending)
				require.NoError(m.Remove(ctx, pending))
				pendingHandler := lifecycle.NewMockActionHandler(ctrl)
				m.kubernetesMonitor.handlers[v1beta1.AppTypeHelm] = pendingHandler
				pendingHandler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil)

			case "remove":
				p.EXPECT().Remove(gomock.Any()).Return(nil)
				require.NoError(m.Remove(ctx, p))
				handler.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil)
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:during", "", 0)
			collect("sha256:during")
			err = m.AfterUpdate(ctx)
			if operation == "update" {
				require.Error(err)
			} else {
				require.NoError(err)
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:new", "", 0)
			collect("sha256:new")
			require.NoError(m.BeforeUpdate(ctx, &v1beta1.DeviceSpec{}))
			require.NoError(m.AfterUpdate(ctx))
			if operation == "update" {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:after-retry", "", 0)
				collect("sha256:after-retry")
				// Once the pending batch completes, steady-state hooks retain the cache.
				require.NoError(m.BeforeUpdate(ctx, &v1beta1.DeviceSpec{}))
				require.NoError(m.AfterUpdate(ctx))
				collect("sha256:after-retry")
			} else {
				collect("sha256:new")
			}
		})
	}
}

func TestVolumeImageDigestCacheEvictsChangedReferences(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	logger := log.NewPrefixLogger("test")
	m := &manager{log: logger, podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
		return client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{}), nil
	}}
	collect := func(reference, digest string) {
		volumes := []v1beta1.ApplicationVolumeStatus{{Reference: reference}}
		results := []AppStatusResult{{Status: v1beta1.DeviceApplicationStatus{Volumes: &volumes}}}
		m.addVolumeImageDigests(context.Background(), results)
		require.Equal([]v1beta1.ApplicationImageDigest{{Image: reference, Digest: digest}}, *results[0].Status.ImageDigests)
	}
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:old", "", 0)
	collect("volume:v1", "sha256:old")
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v2").Return("sha256:v2", "", 0)
	collect("volume:v2", "sha256:v2")
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:new", "", 0)
	collect("volume:v1", "sha256:new")
	require.Len(m.volumeImageDigests, 1)
}

func TestVolumeImageDigestCacheConcurrentInvalidation(t *testing.T) {
	for _, inspection := range []string{"image", "artifact"} {
		for _, update := range []string{"remove", "after update"} {
			t.Run("When "+inspection+" inspection overlaps "+update+" it should allow updates and discard stale cache results", func(t *testing.T) {
				require := require.New(t)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockExec := executer.NewMockExecuter(ctrl)
				logger := log.NewPrefixLogger("test")
				podman := client.NewPodman(logger, mockExec, fileio.NewMockReadWriter(ctrl), poll.Config{})
				m := &manager{
					log:               logger,
					podmanMonitor:     NewPodmanMonitor(logger, nil, nil, time.Now().Format(time.RFC3339), nil),
					kubernetesMonitor: NewKubernetesMonitor(logger, nil, nil),
					podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
						return podman, nil
					},
				}
				if update == "after update" {
					m.invalidateVolumeImageDigests()
				}
				started := make(chan struct{})
				release := make(chan struct{}, 1)
				collected := make(chan struct{})
				block := func(context.Context, string, ...string) (string, string, int) {
					close(started)
					<-release
					if inspection == "artifact" {
						return "{}", "", 0
					}
					return "sha256:old", "", 0
				}
				if inspection == "artifact" {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("", "no such image", 125)
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", "volume:v1").DoAndReturn(block)
				} else {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").DoAndReturn(block)
				}
				collect := func() []AppStatusResult {
					volumes := []v1beta1.ApplicationVolumeStatus{{Reference: "volume:v1"}}
					results := []AppStatusResult{{Status: v1beta1.DeviceApplicationStatus{Volumes: &volumes}}}
					m.addVolumeImageDigests(context.Background(), results)
					return results
				}
				go func() {
					collect()
					close(collected)
				}()
				// Release the blocked inspection even if an assertion fails.
				defer func() {
					close(release)
					<-collected
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("volume inspection did not start")
				}
				updateFinished := make(chan error, 1)
				if update == "remove" {
					p := provider.NewMockProvider(ctrl)
					p.EXPECT().Remove(gomock.Any()).Return(fmt.Errorf("remove failed"))
					go func() { updateFinished <- m.Remove(context.Background(), p) }()
				} else {
					go func() { updateFinished <- m.AfterUpdate(context.Background()) }()
				}
				select {
				case err := <-updateFinished:
					if update == "remove" {
						require.Error(err)
					} else {
						require.NoError(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("application update waited for volume inspection")
				}
				// Populate the new generation before the old inspection finishes.
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "volume:v1").Return("sha256:new", "", 0)
				require.Equal("sha256:new", (*collect()[0].Status.ImageDigests)[0].Digest)
				release <- struct{}{}
				<-collected
				require.Equal("sha256:new", (*collect()[0].Status.ImageDigests)[0].Digest)
			})
		}
	}
}
