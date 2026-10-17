package provider

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWhenAContainerIsInstalledItShouldUseSharedEventConfiguration(t *testing.T) {
	require := require.New(t)
	logger, podman, rw, _ := setupTestEnv(t)
	app := v1beta1.ContainerApplication{AppType: v1beta1.AppTypeContainer, Name: lo.ToPtr("container")}
	require.NoError(app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/flightctl-tests/alpine:v1"}))
	var spec v1beta1.ApplicationProviderSpec
	require.NoError(spec.FromContainerApplication(app))
	p, err := newContainerProvider(t.Context(), logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil }, &spec,
		func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil })
	require.NoError(err)
	require.NoError(p.Install(t.Context()))
	config, err := rw.ReadFile("/etc/containers/systemd/.flightctl-podman-events.conf")
	require.NoError(err)
	require.Equal("[engine]\nevents_container_create_inspect_data = true\n", string(config))
	dropIn, err := rw.ReadFile(filepath.Join(p.Spec().Path, p.ID()+"-.container.d", quadletDropInFile))
	require.NoError(err)
	require.Contains(string(dropIn), "CONTAINERS_CONF_OVERRIDE=/etc/containers/systemd/.flightctl-podman-events.conf")
	require.NoError(p.Remove(t.Context()))
	config, err = rw.ReadFile("/etc/containers/systemd/.flightctl-podman-events.conf")
	require.NoError(err)
	require.Equal("[engine]\nevents_container_create_inspect_data = true\n", string(config))
}

func TestContainerEnsureDependencies(t *testing.T) {
	tests := []struct {
		name           string
		appName        string
		volumes        *[]v1beta1.ApplicationVolume
		commandChecker commandChecker
		setupMocks     func(*executer.MockExecuter)
		wantErr        error
	}{
		{
			name:           "missing podman binary returns ErrAppDependency",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return false },
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "podman binary available with valid version",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return cmd == "podman" || cmd == "skopeo" },
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "--version").
					Return("podman version 5.4.0", "", 0)
			},
			wantErr: nil,
		},
		{
			name:           "podman version below minimum",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return cmd == "podman" || cmd == "skopeo" },
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "--version").
					Return("podman version 4.9.0", "", 0)
			},
			wantErr: errors.ErrAppDependency,
		},
		{
			name:           "missing skopeo binary returns ErrAppDependency",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return cmd == "podman" },
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:    "volume dependency with podman version below minimum",
			appName: "app",
			volumes: &[]v1beta1.ApplicationVolume{
				makeImageMountVolume("data", "quay.io/data:latest", "/data"),
			},
			commandChecker: func(cmd string) bool { return true },
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "--version").
					Return("podman version 5.4.0", "", 0).
					Times(2)
			},
			wantErr: errors.ErrAppDependency,
		},
		{
			name:    "volume dependency with podman version at minimum",
			appName: "app",
			volumes: &[]v1beta1.ApplicationVolume{
				makeImageMountVolume("data", "quay.io/data:latest", "/data"),
			},
			commandChecker: func(cmd string) bool { return true },
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "--version").
					Return("podman version 5.5.0", "", 0).
					Times(2)
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := log.NewPrefixLogger("test")

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			podman := client.NewPodman(log, mockExec, rw, testutil.NewPollConfig())

			if tt.setupMocks != nil {
				tt.setupMocks(mockExec)
			}

			containerApp := v1beta1.ContainerApplication{
				AppType: v1beta1.AppTypeContainer,
				Name:    lo.ToPtr(tt.appName),
				Volumes: tt.volumes,
			}
			err := containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "nginx:latest"})
			require.NoError(err)

			var appSpec v1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(containerApp)
			require.NoError(err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(user v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}

			containerProvider, err := newContainerProvider(ctx, log, podmanFactory, &appSpec, rwFactory)
			require.NoError(err)

			if tt.commandChecker != nil {
				containerProvider.commandChecker = tt.commandChecker
			}

			err = containerProvider.EnsureDependencies(ctx)
			if tt.wantErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

func TestWhenAnImageMountContainsAnArtifactItShouldUseArtifactVolumePreparation(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const artifact = "quay.io/acme/data:v1"
	var volume v1beta1.ApplicationVolume
	volume.Name = "data"
	require.NoError(volume.FromImageMountVolumeProviderSpec(v1beta1.ImageMountVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: artifact}, Mount: v1beta1.VolumeMount{Path: "/data"},
	}))
	app := v1beta1.ContainerApplication{Name: lo.ToPtr("app"), AppType: v1beta1.AppTypeContainer, Volumes: &[]v1beta1.ApplicationVolume{volume}}
	require.NoError(app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/acme/web:v1"}))
	var appSpec v1beta1.ApplicationProviderSpec
	require.NoError(appSpec.FromContainerApplication(app))
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", artifact).Return("", "", 1).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", artifact).Return("{}", "", 0).AnyTimes()
	podman := client.NewPodman(logger, exec, rw, testutil.NewPollConfig())
	providers, err := FromDeviceSpec(t.Context(), logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil }, nil,
		func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil },
		&v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{appSpec}})
	require.NoError(err)
	require.Len(providers, 1)
	require.NoError(providers[0].Install(t.Context()))
	volumes := providers[0].Spec().Volume.List()
	require.Len(volumes, 1)
	require.Equal(dependency.OCITypePodmanArtifact, volumes[0].OCIType)

	appID := lifecycle.GenerateAppID("app", v1beta1.RootUsername)
	expectedVolumeID := appID + "-data"
	require.Equal(expectedVolumeID, volumes[0].ID)
	container, err := rw.ReadFile(filepath.Join(providers[0].Spec().Path, providers[0].ID()+"-app.container"))
	require.NoError(err)
	require.Contains(string(container), "Volume="+expectedVolumeID+":/data")
}
