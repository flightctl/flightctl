package applications

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	testImage = "quay.io/flightctl-tests/alpine:v1"
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
			desired: newTestDeviceWithApplications(require, "app-new", []testInlineDetails{
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
			name: "update existing application",
			current: newTestDeviceWithApplications(require, "app-update", []testInlineDetails{
				{Content: compose1, Path: "podman-compose.yaml"},
			}),
			desired: newTestDeviceWithApplications(require, "app-update", []testInlineDetails{
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
			mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, util.NewPollConfig())

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
	mockPodmanClient := client.NewPodman(log, mockExec, mockReadWriter, util.NewPollConfig())

	readWriter := fileio.NewReadWriter()
	tmpDir := t.TempDir()
	readWriter.SetRootdir(tmpDir)

	current := newTestDeviceWithApplications(require, "app-remove", []testInlineDetails{
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

func TestBeforeUpdate(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		deviceSpec  *v1alpha1.DeviceSpec
		setupMocks  func(mockExec *executer.MockExecuter)
		expectedErr error
	}{
		{
			name:       "no applications",
			deviceSpec: &v1alpha1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// no calls expected for empty spec
			},
		},
		{
			name: "inline application some images exist",
			deviceSpec: newTestDeviceWithApplications(require, "test-app", []testInlineDetails{
				{Content: compose1, Path: "docker-compose.yml"},
			}),
			setupMocks: func(mockExec *executer.MockExecuter) {
				gomock.InOrder(
					// not available
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", testImage).Return("", "", 0),
					// available no pull
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 0),
				)
			},
		},
		{
			name: "inline application all images exist",
			deviceSpec: newTestDeviceWithApplications(require, "test-app-wait", []testInlineDetails{
				{Content: compose1, Path: "docker-compose.yml"},
			}),
			setupMocks: func(mockExec *executer.MockExecuter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 0),
				)
			},
		},
		{
			name: "inline application with pull secret config no images exist",
			deviceSpec: func() *v1alpha1.DeviceSpec {
				spec := newTestDeviceWithApplications(require, "test-secret", []testInlineDetails{
					{Content: compose1, Path: "docker-compose.yml"},
				})
				// pull secret via config provider
				pullSecretConfig := v1alpha1.ConfigProviderSpec{}
				err := pullSecretConfig.FromInlineConfigProviderSpec(v1alpha1.InlineConfigProviderSpec{
					Name: "pull-secret",
					Inline: []v1alpha1.FileSpec{
						{
							Path:    "/root/.config/containers/auth.json",
							Content: `{"auths":{"registry.io":{"auth":"dGVzdDp0ZXN0"}}}`,
						},
					},
				})
				require.NoError(err)
				spec.Config = &[]v1alpha1.ConfigProviderSpec{pullSecretConfig}
				return spec
			}(),
			setupMocks: func(mockExec *executer.MockExecuter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", testImage, "--authfile", gomock.Any()).Return("", "", 0),

					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", testImage, "--authfile", gomock.Any()).Return("", "", 0),

					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImage).Return("", "", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", testImage, "--authfile", gomock.Any()).Return("", "", 0),
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			rw := fileio.NewReadWriter()
			tmpDir := t.TempDir()
			rw.SetRootdir(tmpDir)

			mockExec := executer.NewMockExecuter(ctrl)
			podmanClient := client.NewPodman(log, mockExec, rw, util.NewPollConfig())

			manager := &manager{
				readWriter:    rw,
				podmanMonitor: NewPodmanMonitor(log, podmanClient, "", rw),
				podmanClient:  podmanClient,
				log:           log,
			}

			tt.setupMocks(mockExec)

			err := manager.BeforeUpdate(ctx, tt.deviceSpec)
			if tt.expectedErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.expectedErr)
				return
			}
			require.NoError(err)
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

func newTestDeviceWithApplications(require *require.Assertions, name string, details []testInlineDetails) *v1alpha1.DeviceSpec {
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
	require.NoError(err)

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
    image: quay.io/flightctl-tests/alpine:v1
    command: ["sleep", "infinity"]
  service3:
    image: quay.io/flightctl-tests/alpine:v1
    command: ["sleep", "infinity"]
`

var compose2 = `version: "3.8"
services:
  service1:
    image: quay.io/flightctl-tests/alpine:v1
    command: ["sleep", "infinity"]
`
