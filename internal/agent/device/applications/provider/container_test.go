package provider

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

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
				Image:   "nginx:latest",
				Volumes: tt.volumes,
			}

			var appSpec v1beta1.ApplicationProviderSpec
			err := appSpec.FromContainerApplication(containerApp)
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
