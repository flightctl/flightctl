package provider

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestImageProvider(t *testing.T) {
	require := require.New(t)
	appImage := "quay.io/flightctl-tests/alpine:v1"
	tests := []struct {
		name          string
		image         string
		spec          *v1alpha1.ApplicationProviderSpec
		composeSpec   string
		labels        map[string]string
		setupMocks    func(*executer.MockExecuter, string)
		wantVerifyErr error
	}{
		{
			name:   "missing appType label",
			image:  appImage,
			labels: map[string]string{},
			spec: &v1alpha1.ApplicationProviderSpec{
				Name: lo.ToPtr("app"),
			},
			composeSpec: util.NewComposeSpec(),
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", appImage}).Return(appLabels, "", 0),
				)
			},
			wantVerifyErr: errors.ErrAppLabel,
		},
		{
			name:  "appType label set to invalid value",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: "invalid",
			},
			spec: &v1alpha1.ApplicationProviderSpec{
				Name: lo.ToPtr("app"),
			},
			composeSpec: util.NewComposeSpec(),
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", appImage}).Return(appLabels, "", 0),
				)
			},
			wantVerifyErr: errors.ErrUnsupportedAppType,
		},
		{
			name:  "appType compose with valid env",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1alpha1.AppTypeCompose),
			},
			spec: &v1alpha1.ApplicationProviderSpec{
				Name: lo.ToPtr("app"),
				EnvVars: lo.ToPtr(map[string]string{
					"FOO": "bar",
				}),
			},
			composeSpec: util.NewComposeSpec(),
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", appImage}).Return(appLabels, "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", appImage}).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", appImage}).Return("", "", 0),

					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", appImage}).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", appImage}).Return("", "", 0),
				)
			},
		},
		{
			name:  "appType compose with invalid env",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1alpha1.AppTypeCompose),
			},
			spec: &v1alpha1.ApplicationProviderSpec{
				Name: lo.ToPtr("app"),
				EnvVars: lo.ToPtr(map[string]string{
					"!nvalid": "bar",
				}),
			},
			composeSpec:   util.NewComposeSpec(),
			setupMocks:    func(mockExec *executer.MockExecuter, appLabels string) {},
			wantVerifyErr: errors.ErrInvalidSpec,
		},
		{
			name:  "appType compose with invalid hardcoded container name",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1alpha1.AppTypeCompose),
			},
			spec: &v1alpha1.ApplicationProviderSpec{
				Name: lo.ToPtr("app"),
			},
			composeSpec: `version: "3.8"
services:
  service1:
    container_name: app #invalid hardcoded container name
    image: quay.io/flightctl-tests/alpine:v1`,
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", appImage}).Return(appLabels, "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", appImage}).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", appImage}).Return("", "", 0),
				)
			},
			wantVerifyErr: validation.ErrHardCodedContainerName,
		},
		{
			name:  "appType compose with no services",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1alpha1.AppTypeCompose),
			},
			spec: &v1alpha1.ApplicationProviderSpec{
				Name: lo.ToPtr("app"),
			},
			composeSpec: `version: "3.8"
services:
image: quay.io/flightctl-tests/alpine:v1`,
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", appImage}).Return(appLabels, "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", appImage}).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", appImage}).Return("", "", 0),
				)
			},
			wantVerifyErr: errors.ErrNoComposeServices,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)
			err := rw.MkdirAll("/mount", fileio.DefaultDirectoryPermissions)
			require.NoError(err)
			err = rw.WriteFile("/mount/podman-compose.yaml", []byte(tt.composeSpec), fileio.DefaultFilePermissions)
			require.NoError(err)
			podman := client.NewPodman(log, mockExec, rw, util.NewBackoff())

			spec := v1alpha1.ImageApplicationProviderSpec{
				Image: tt.image,
			}
			provider := tt.spec
			err = provider.FromImageApplicationProviderSpec(spec)
			require.NoError(err)

			imageProvider, err := newImage(log, podman, provider, rw)
			require.NoError(err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			inspect := mockPodmanInspect(tt.labels)
			inspectBytes, err := json.Marshal(inspect)
			require.NoError(err)

			tt.setupMocks(mockExec, string(inspectBytes))
			err = imageProvider.Verify(ctx)
			if tt.wantVerifyErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantVerifyErr)
				return
			}
			require.NoError(err)
			err = imageProvider.Install(ctx)
			require.NoError(err)
			// verify env file
			if tt.spec.EnvVars != nil {
				appPath, err := pathFromAppType(imageProvider.spec.AppType, imageProvider.spec.Name, imageProvider.spec.Embedded)
				require.NoError(err)
				require.True(rw.PathExists(filepath.Join(appPath, ".env")))
				envFile, err := rw.ReadFile(filepath.Join(appPath, ".env"))
				require.NoError(err)
				for k, v := range lo.FromPtr(tt.spec.EnvVars) {
					require.Contains(string(envFile), k+"="+v)
				}
			}
		})
	}
}

func mockPodmanInspect(labels map[string]string) []client.PodmanInspect {
	inspect := client.PodmanInspect{
		Config: client.PodmanContainerConfig{
			Labels: labels,
		},
	}
	return []client.PodmanInspect{inspect}
}
