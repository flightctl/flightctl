package provider

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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
		spec          *v1beta1.ApplicationProviderSpec
		content       []v1beta1.ApplicationContent
		setupMocks    func(*executer.MockExecuter)
		wantVerifyErr error
	}{
		{
			name:  "happy path",
			image: appImage,
			spec: &v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: v1beta1.AppTypeCompose,
				EnvVars: lo.ToPtr(map[string]string{
					"FOO": "bar",
					"BAZ": "qux",
				}),
			},
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "docker-compose.yml",
				},
			},
		},
		{
			name:  "invalid compose path",
			image: appImage,
			spec: &v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: v1beta1.AppTypeCompose,
			},
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "invalid-compose.yml",
				},
			},
			wantVerifyErr: errors.ErrNoComposeFile,
		},
		{
			name:  "invalid env vars",
			image: appImage,
			spec: &v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: v1beta1.AppTypeCompose,
				EnvVars: lo.ToPtr(map[string]string{
					"1NVALID": "bar",
				}),
			},
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "docker-compose.yml",
				},
			},
			wantVerifyErr: errors.ErrInvalidSpec,
		},
		{
			name:  "valid overide",
			image: appImage,
			spec: &v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: v1beta1.AppTypeCompose,
			},
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "podman-compose.yml",
				},
				{
					Content: lo.ToPtr(util.NewComposeSpec("docker.io/override:latest")),
					Path:    "podman-compose.override.yml",
				},
			},
		},
		{
			name:  "quadlet with valid podman version",
			image: appImage,
			spec: &v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("quadlet-app"),
				AppType: v1beta1.AppTypeQuadlet,
			},
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr("[Container]\nImage=quay.io/flightctl-tests/nginx:latest\n"),
					Path:    "web.container",
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "--version").
					Return("podman version 5.4.2", "", 0)
			},
		},
		{
			name:  "quadlet with podman version below minimum",
			image: appImage,
			spec: &v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("quadlet-app"),
				AppType: v1beta1.AppTypeQuadlet,
			},
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr("[Container]\nImage=quay.io/flightctl-tests/nginx:latest\n"),
					Path:    "web.container",
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "--version").
					Return("podman version 4.9.0", "", 0)
			},
			wantVerifyErr: errors.ErrNoRetry,
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
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			podman := client.NewPodman(log, mockExec, rw, util.NewPollConfig())

			if tt.setupMocks != nil {
				tt.setupMocks(mockExec)
			}

			spec := v1beta1.InlineApplicationProviderSpec{
				Inline: tt.content,
			}

			provider := tt.spec
			err := provider.FromInlineApplicationProviderSpec(spec)
			require.NoError(err)
			inlineProvider, err := newInline(log, podman, provider, rw)
			require.NoError(err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

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
				appPath := inlineProvider.handler.AppPath()
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
