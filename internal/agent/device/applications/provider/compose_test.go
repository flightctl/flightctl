package provider

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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

func TestComposeImageProvider(t *testing.T) {
	appImage := "quay.io/flightctl-tests/alpine:v1"
	tests := []struct {
		name          string
		image         string
		appName       string
		envVars       map[string]string
		composeSpec   string
		labels        map[string]string
		setupMocks    func(*executer.MockExecuter, string)
		wantVerifyErr error
	}{
		{
			name:        "missing appType label is allowed",
			image:       appImage,
			appName:     "app",
			labels:      map[string]string{},
			composeSpec: util.NewComposeSpec(),
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", appImage).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", appImage).Return("", "", 0),

					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", appImage).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", appImage).Return("", "", 0),
				)
			},
			// No error expected - missing appType label is now allowed
		},
		{
			name:  "appType label set to invalid value",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: "invalid",
			},
			appName: "app",
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
				)
			},
			wantVerifyErr: errors.ErrAppLabel,
		},
		{
			name:  "appType compose with valid env",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1beta1.AppTypeCompose),
			},
			appName: "app",
			envVars: map[string]string{
				"FOO": "bar",
			},
			composeSpec: util.NewComposeSpec(),
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", appImage).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", appImage).Return("", "", 0),

					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", appImage).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", appImage).Return("", "", 0),
				)
			},
		},
		{
			name:  "appType compose with invalid env",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1beta1.AppTypeCompose),
			},
			appName: "app",
			envVars: map[string]string{
				"!nvalid": "bar",
			},
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
				)
			},
			wantVerifyErr: errors.ErrInvalidSpec,
		},
		{
			name:  "appType compose with invalid hardcoded container name",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1beta1.AppTypeCompose),
			},
			appName: "app",
			composeSpec: `version: "3.8"
services:
  service1:
    container_name: app #invalid hardcoded container name
    image: quay.io/flightctl-tests/alpine:v1`,
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", appImage).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", appImage).Return("", "", 0),
				)
			},
			wantVerifyErr: validation.ErrHardCodedContainerName,
		},
		{
			name:  "appType compose with no services",
			image: appImage,
			labels: map[string]string{
				AppTypeLabel: string(v1beta1.AppTypeCompose),
			},
			appName: "app",
			composeSpec: `version: "3.8"
services:
image: quay.io/flightctl-tests/alpine:v1`,
			setupMocks: func(mockExec *executer.MockExecuter, appLabels string) {
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", appImage).Return("/mount", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", appImage).Return("", "", 0),
				)
			},
			wantVerifyErr: errors.ErrNoComposeServices,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
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

			composeSpec := tt.composeSpec
			if composeSpec == "" {
				composeSpec = util.NewComposeSpec()
			}

			err := rw.MkdirAll("/mount", fileio.DefaultDirectoryPermissions)
			require.NoError(err)
			err = rw.WriteFile("/mount/podman-compose.yaml", []byte(composeSpec), fileio.DefaultFilePermissions)
			require.NoError(err)
			podman := client.NewPodman(log, mockExec, rw, util.NewPollConfig())

			// Build the ComposeApplication with the new schema
			composeApp := v1beta1.ComposeApplication{
				AppType: v1beta1.AppTypeCompose,
				Name:    lo.ToPtr(tt.appName),
			}
			if tt.envVars != nil {
				composeApp.EnvVars = &tt.envVars
			}
			err = composeApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{
				Image: tt.image,
			})
			require.NoError(err)

			var appSpec v1beta1.ApplicationProviderSpec
			err = appSpec.FromComposeApplication(composeApp)
			require.NoError(err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			inspect := mockPodmanInspect(tt.labels)
			inspectBytes, err := json.Marshal(inspect)
			require.NoError(err)

			tt.setupMocks(mockExec, string(inspectBytes))

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(user v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}

			composeProvider, err := newComposeProvider(ctx, log, podmanFactory, &appSpec, rwFactory, nil)
			if tt.wantVerifyErr != nil && err != nil {
				require.ErrorIs(err, tt.wantVerifyErr)
				return
			}
			require.NoError(err)

			err = composeProvider.Verify(ctx)
			if tt.wantVerifyErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantVerifyErr)
				return
			}
			require.NoError(err)
			err = composeProvider.Install(ctx)
			require.NoError(err)

			// verify env file
			if tt.envVars != nil {
				appPath := composeProvider.spec.Path
				exists, err := rw.PathExists(filepath.Join(appPath, ".env"))
				require.NoError(err)
				require.True(exists)
				envFile, err := rw.ReadFile(filepath.Join(appPath, ".env"))
				require.NoError(err)
				for k, v := range tt.envVars {
					require.Contains(string(envFile), k+"="+v)
				}
			}
		})
	}
}

func TestComposeInlineProvider(t *testing.T) {
	tests := []struct {
		name          string
		appName       string
		envVars       map[string]string
		content       []v1beta1.ApplicationContent
		setupMocks    func(*executer.MockExecuter)
		wantVerifyErr error
	}{
		{
			name:    "happy path",
			appName: "app",
			envVars: map[string]string{
				"FOO": "bar",
				"BAZ": "qux",
			},
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "docker-compose.yml",
				},
			},
		},
		{
			name:    "invalid compose path",
			appName: "app",
			content: []v1beta1.ApplicationContent{
				{
					Content: lo.ToPtr(util.NewComposeSpec()),
					Path:    "invalid-compose.yml",
				},
			},
			wantVerifyErr: errors.ErrNoComposeFile,
		},
		{
			name:    "invalid env vars",
			appName: "app",
			envVars: map[string]string{
				"1NVALID": "bar",
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
			name:    "valid override",
			appName: "app",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
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

			// Build the ComposeApplication with inline content
			composeApp := v1beta1.ComposeApplication{
				AppType: v1beta1.AppTypeCompose,
				Name:    lo.ToPtr(tt.appName),
			}
			if tt.envVars != nil {
				composeApp.EnvVars = &tt.envVars
			}
			err := composeApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
				Inline: tt.content,
			})
			require.NoError(err)

			var appSpec v1beta1.ApplicationProviderSpec
			err = appSpec.FromComposeApplication(composeApp)
			require.NoError(err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(user v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}

			composeProvider, err := newComposeProvider(ctx, log, podmanFactory, &appSpec, rwFactory, nil)
			require.NoError(err)

			err = composeProvider.Verify(ctx)
			if tt.wantVerifyErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantVerifyErr)
				return
			}
			require.NoError(err)
			err = composeProvider.Install(ctx)
			require.NoError(err)

			// verify env file
			if tt.envVars != nil {
				appPath := composeProvider.spec.Path
				exists, err := rw.PathExists(filepath.Join(appPath, ".env"))
				require.NoError(err)
				require.True(exists)
				envFile, err := rw.ReadFile(filepath.Join(appPath, ".env"))
				require.NoError(err)
				for k, v := range tt.envVars {
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
