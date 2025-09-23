package applications

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
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
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"stop", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"rm", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),

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
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"stop", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"rm", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),

					// start desired app
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).AnyTimes(),
					mockExecPodmanComposeUp(mockExec, "app-update", true, true),
					mockExecPodmanEvents(mockExec),
				)
			},
			wantAppNames: []string{"app-update"},
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
				podmanMonitor: NewPodmanMonitor(log, mockPodmanClient, "", readWriter),
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
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"stop", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"rm", "--filter", "label=com.docker.compose.project=" + id}).Return("", "", 0),
		// Monitor stops during second AfterUpdate when no apps remain (no mock needed)
	)

	manager := &manager{
		readWriter:    readWriter,
		podmanMonitor: NewPodmanMonitor(log, mockPodmanClient, "", readWriter),
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

func TestManagerDrain(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name              string
		setupMocks        func(*systeminfo.MockManager, *executer.MockExecuter, *fileio.MockReadWriter)
		expectDrainCalled bool
	}{
		{
			name: "drain called when system is shutting down via scheduled file",
			setupMocks: func(mockSystemInfo *systeminfo.MockManager, mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 3"), nil)
				mockReadWriter.EXPECT().PathExists("/run/systemd/shutdown/scheduled").Return(true, nil)
				mockSystemInfo.EXPECT().BootTime().Return("2024-01-01T00:00:00Z")
			},
			expectDrainCalled: true,
		},
		{
			name: "drain not called when agent is just restarting",
			setupMocks: func(mockSystemInfo *systeminfo.MockManager, mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 3"), nil)
				mockReadWriter.EXPECT().PathExists("/run/systemd/shutdown/scheduled").Return(false, nil)

				// list-jobs returns no shutdown jobs
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-jobs", "--no-pager", "--no-legend").Return("", "", 0)

				mockSystemInfo.EXPECT().BootTime().Return("2024-01-01T00:00:00Z")
			},
			expectDrainCalled: false,
		},
		{
			name: "drain is idempotent - multiple calls only drain once",
			setupMocks: func(mockSystemInfo *systeminfo.MockManager, mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 3"), nil)
				mockReadWriter.EXPECT().PathExists("/run/systemd/shutdown/scheduled").Return(true, nil)

				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 3"), nil)
				mockReadWriter.EXPECT().PathExists("/run/systemd/shutdown/scheduled").Return(true, nil)

				mockSystemInfo.EXPECT().BootTime().Return("2024-01-01T00:00:00Z")
			},
			expectDrainCalled: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockSystemInfo := systeminfo.NewMockManager(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMocks(mockSystemInfo, mockExec, mockReadWriter)

			log := log.NewPrefixLogger("test")
			systemdClient := client.NewSystemd(mockExec)
			podmanClient := client.NewPodman(log, mockExec, mockReadWriter, poll.Config{})

			manager := NewManager(log, mockReadWriter, podmanClient, systemdClient, mockSystemInfo)

			ctx := context.Background()

			err := manager.Drain(ctx)
			require.NoError(err)

			if tc.name == "drain is idempotent - multiple calls only drain once" {
				err = manager.Drain(ctx)
				require.NoError(err)
			}
		})
	}
}

func TestPodmanMonitorStartStop(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	reader, writer := io.Pipe()

	mockReadWriter.EXPECT().PathExists(gomock.Any(), gomock.Any()).DoAndReturn(func(path string, opts ...fileio.PathExistsOption) (bool, error) {
		return path == "/test/path/podman-compose.yaml", nil
	}).AnyTimes()

	mockExec.EXPECT().
		ExecuteWithContextFromDir(gomock.Any(), "/test/path", "podman", []string{"compose", "-p", "test-app", "-f", "podman-compose.yaml", "up", "-d", "--no-recreate"}).
		Return("", "", 0)

	mockExec.EXPECT().
		CommandContext(gomock.Any(), "podman", "events", "--format", "json", "--since", "2024-01-01T00:00:00Z", "--filter", "event=create", "--filter", "event=init", "--filter", "event=start", "--filter", "event=stop", "--filter", "event=die", "--filter", "event=sync", "--filter", "event=remove", "--filter", "event=exited").
		DoAndReturn(func(ctx context.Context, cmd string, args ...string) *exec.Cmd {
			execCmd := exec.CommandContext(ctx, "cat")
			execCmd.Stdin = reader
			return execCmd
		})

	log := log.NewPrefixLogger("test")
	podmanClient := client.NewPodman(log, mockExec, mockReadWriter, poll.Config{})

	monitor := NewPodmanMonitor(log, podmanClient, "2024-01-01T00:00:00Z", mockReadWriter)

	volumeManager, err := provider.NewVolumeManager(log, "test-app", nil)
	require.NoError(err)

	app := &application{
		id:      "test-app",
		appType: v1alpha1.AppTypeCompose,
		path:    "/test/path",
		volume:  volumeManager,
		status: &v1alpha1.DeviceApplicationStatus{
			Name:   "test-app",
			Status: v1alpha1.ApplicationStatusUnknown,
		},
		embedded: false,
	}

	// manage app
	err = monitor.Ensure(app)
	require.NoError(err)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer writer.Close()
		// write a sync event to indicate we're ready
		syncEvent := `{"Type":"sync","timeNano":1704067200000000000}` + "\n"
		_, err := writer.Write([]byte(syncEvent))
		require.NoError(err)

		<-ctx.Done()
	}()

	err = monitor.ExecuteActions(ctx)
	require.NoError(err)

	require.True(monitor.isRunning())

	// wait for listening monitor
	select {
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("Context cancelled unexpectedly")
	}

	// shutdown
	cancel()

	select {
	case <-time.After(100 * time.Millisecond):
	case <-time.After(1 * time.Second):
		t.Fatal("Cleanup took too long")
	}
}

func TestIsSystemShutdown(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name           string
		setupMocks     func(*executer.MockExecuter, *fileio.MockReadWriter)
		expectedResult bool
	}{
		{
			name: "detects shutdown via scheduled file",
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 3"), nil)
				mockReadWriter.EXPECT().PathExists("/run/systemd/shutdown/scheduled").Return(true, nil)
			},
			expectedResult: true,
		},
		{
			name: "detects shutdown via systemd reboot target using list-jobs",
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 3"), nil)
				mockReadWriter.EXPECT().PathExists("/run/systemd/shutdown/scheduled").Return(false, nil)
				// list-jobs returns a job for reboot.target
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-jobs", "--no-pager", "--no-legend").Return("123 reboot.target start waiting", "", 0)
			},
			expectedResult: true,
		},
		{
			name: "detects shutdown via runlevel 0",
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 0"), nil)
			},
			expectedResult: true,
		},
		{
			name: "detects shutdown via runlevel 6",
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 6"), nil)
			},
			expectedResult: true,
		},
		{
			name: "no shutdown detected",
			setupMocks: func(mockExec *executer.MockExecuter, mockReadWriter *fileio.MockReadWriter) {
				mockReadWriter.EXPECT().ReadFile("/run/utmp").Return([]byte("runlevel 3"), nil)
				mockReadWriter.EXPECT().PathExists("/run/systemd/shutdown/scheduled").Return(false, nil)
				// list-jobs returns no jobs
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-jobs", "--no-pager", "--no-legend").Return("", "", 0)
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMocks(mockExec, mockReadWriter)

			systemdClient := client.NewSystemd(mockExec)

			ctx := context.Background()
			log := log.NewPrefixLogger("test")

			result := shutdown.IsSystemShutdown(ctx, systemdClient, mockReadWriter, log)
			require.Equal(tc.expectedResult, result)
		})
	}
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
		Return("", "", 0)
}

type testInlineDetails struct {
	Content string
	Path    string
}

func newTestDeviceWithApplications(t *testing.T, name string, details []testInlineDetails) *v1alpha1.DeviceSpec {
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
		AppType: lo.ToPtr(v1alpha1.AppTypeCompose),
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
