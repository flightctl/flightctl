package applications

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	testImageV1 = "quay.io/flightctl-tests/alpine:v1"
	testImageV2 = "quay.io/flightctl-tests/alpine:v2"
	testImageV3 = "quay.io/flightctl-tests/alpine:v3"
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
				gomock.InOrder(
					mockExecPodmanEvents(mockExec),
				)
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

					mockExecPodmanEvents(mockExec),
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
			mockPrefetchManager := dependency.NewMockPrefetchManager(ctrl)

			tmpDir := t.TempDir()
			readWriter.SetRootdir(tmpDir)

			tc.setupMocks(
				mockExec,
				mockReadWriter,
			)

			currentProviders, err := provider.FromDeviceSpec(ctx, log, mockPodmanClient, readWriter, tc.current)
			require.NoError(err)

			manager := &manager{
				readWriter:      readWriter,
				podmanMonitor:   NewPodmanMonitor(log, mockPodmanClient, "", readWriter),
				prefetchManager: mockPrefetchManager,
				log:             log,
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

func TestBeforeUpdate(t *testing.T) {
	tests := []struct {
		name       string
		deviceSpec *v1alpha1.DeviceSpec
		setupMocks func(
			mockExec *executer.MockExecuter)
		wantErr error
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
			deviceSpec: newTestDeviceWithApplications(t, "test-app", []testInlineDetails{
				{Content: compose1, Path: "docker-compose.yml"},
			}),
			setupMocks: func(mockExec *executer.MockExecuter) {
				// prefetch manager calls to check image exists (once per unique image)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV1).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV2).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV3).Return("", "", 0)
			},
			wantErr: errors.ErrImagePrefetchNotReady,
		},
		{
			name: "inline application all images exist",
			deviceSpec: newTestDeviceWithApplications(t, "test-app-wait", []testInlineDetails{
				{Content: compose1, Path: "docker-compose.yml"},
			}),
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV1).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV2).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV3).Return("", "", 0)
			},
		},
		{
			name: "inline application with pull secret config no images exist",
			deviceSpec: func() *v1alpha1.DeviceSpec {
				spec := newTestDeviceWithApplications(t, "test-secret", []testInlineDetails{
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
				require.NoError(t, err)
				spec.Config = &[]v1alpha1.ConfigProviderSpec{pullSecretConfig}
				return spec
			}(),
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV1).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV2).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testImageV3).Return("", "", 1)
			},
			wantErr: errors.ErrImagePrefetchNotReady,
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
			podmanClient := client.NewPodman(log, mockExec, rw, testutil.NewPollConfig())
			prefetchManager := dependency.NewPrefetchManager(log, podmanClient, util.Duration(1*time.Second))

			manager := &manager{
				readWriter:      rw,
				podmanMonitor:   NewPodmanMonitor(log, podmanClient, "", rw),
				podmanClient:    podmanClient,
				prefetchManager: prefetchManager,
				log:             log,
			}

			tt.setupMocks(mockExec)

			err := manager.BeforeUpdate(ctx, tt.deviceSpec)
			if tt.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
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
