package provider

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestInlineProvider(t *testing.T) {
	require := require.New(t)
	appImage := "quay.io/flightctl-tests/alpine:v1"
	tests := []struct {
		name          string
		image         string
		spec          *v1alpha1.ApplicationProviderSpec
		content       []v1alpha1.ApplicationContent
		setupMocks    func(*executer.MockExecuter)
		wantVerifyErr error
	}{
		{
			name:  "happy path",
			image: appImage,
			spec: &v1alpha1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: lo.ToPtr(v1alpha1.AppTypeCompose),
				EnvVars: lo.ToPtr(map[string]string{
					"FOO": "bar",
					"BAZ": "qux",
				}),
			},
			content: []v1alpha1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "docker-compose.yml",
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0),
				)
			},
		},
		{
			name:  "invalid compose path",
			image: appImage,
			spec: &v1alpha1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: lo.ToPtr(v1alpha1.AppTypeCompose),
			},
			content: []v1alpha1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "invalid-compose.yml",
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				gomock.InOrder()
			},
			wantVerifyErr: errors.ErrNoComposeFile,
		},
		{
			name:  "invalid env vars",
			image: appImage,
			spec: &v1alpha1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: lo.ToPtr(v1alpha1.AppTypeCompose),
				EnvVars: lo.ToPtr(map[string]string{
					"1NVALID": "bar",
				}),
			},
			content: []v1alpha1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "docker-compose.yml",
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				gomock.InOrder()
			},
			wantVerifyErr: errors.ErrInvalidSpec,
		},
		{
			name:  "valid overide",
			image: appImage,
			spec: &v1alpha1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: lo.ToPtr(v1alpha1.AppTypeCompose),
			},
			content: []v1alpha1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "podman-compose.yml",
				},
				{
					Content: lo.ToPtr(util.NewComposeSpec("docker.io/override:latest")),
					Path:    "podman-compose.override.yml",
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "docker.io/override:latest"}).Return("", "", 0),
				)
			},
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
			podman := client.NewPodman(log, mockExec, rw, util.NewBackoff())

			spec := v1alpha1.InlineApplicationProviderSpec{
				Inline: tt.content,
			}

			provider := tt.spec
			err := provider.FromInlineApplicationProviderSpec(spec)
			require.NoError(err)
			inlineProvider, err := newInline(log, podman, provider, rw)
			require.NoError(err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tt.setupMocks(mockExec)
			err = inlineProvider.Verify(ctx)
			if tt.wantVerifyErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantVerifyErr)
				return
			}
			require.NoError(err)
			err = inlineProvider.Install(ctx)
			require.NoError(err)
			// verify env file
			if tt.spec.EnvVars != nil {
				appPath, err := pathFromAppType(inlineProvider.spec.AppType, inlineProvider.spec.Name, inlineProvider.spec.Embedded)
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
