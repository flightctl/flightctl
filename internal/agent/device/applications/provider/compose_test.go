package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/opencontainers/go-digest"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestComposeImageProvider(t *testing.T) {
	appImage := "quay.io/flightctl-tests/alpine:v1"
	imageID := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
				calls := []any{
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
				}
				calls = append(calls, expectComposeImageExtraction(mockExec, appImage, imageID, imageDigest)...)
				calls = append(calls, expectComposeImageExtraction(mockExec, appImage, imageID, imageDigest)...)
				gomock.InOrder(calls...)
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
				calls := []any{
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
				}
				calls = append(calls, expectComposeImageExtraction(mockExec, appImage, imageID, imageDigest)...)
				calls = append(calls, expectComposeImageExtraction(mockExec, appImage, imageID, imageDigest)...)
				gomock.InOrder(calls...)
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
				calls := []any{
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
				}
				calls = append(calls, expectComposeImageExtraction(mockExec, appImage, imageID, imageDigest)...)
				gomock.InOrder(calls...)
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
				calls := []any{
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", appImage).Return(appLabels, "", 0),
				}
				calls = append(calls, expectComposeImageExtraction(mockExec, appImage, imageID, imageDigest)...)
				gomock.InOrder(calls...)
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
					require.Contains(string(envFile), fmt.Sprintf("%s=\"%s\"", k, v))
				}
			}
		})
	}
}

func expectComposeImageExtraction(mockExec *executer.MockExecuter, imageRef, imageID, imageDigest string) []any {
	return []any{
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Id}}", imageRef).Return(imageID, "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", imageID).Return(imageDigest, "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", imageID).Return("/mount", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", imageID).Return("", "", 0),
	}
}

func TestWhenProvidersInstallCollectedParentsItShouldPreserveTheExtractedIdentity(t *testing.T) {
	const (
		imageRef       = "quay.io/acme/compose-bundle:v1"
		artifactDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	for _, tc := range []struct {
		name, digest string
		appType      v1beta1.AppType
	}{
		{name: "When Compose parent contents have a verified digest it should keep that identity", digest: artifactDigest, appType: v1beta1.AppTypeCompose},
		{name: "When Compose parent contents have no digest it should install without identity evidence", appType: v1beta1.AppTypeCompose},
		{name: "When Quadlet parent contents have a verified digest it should keep that identity", digest: artifactDigest, appType: v1beta1.AppTypeQuadlet},
		{name: "When Quadlet parent contents have no digest it should install without identity evidence", appType: v1beta1.AppTypeQuadlet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			const (
				extractedPath = "/extracted-parent"
				installedPath = "/installed-app"
			)
			require.NoError(t, rw.MkdirAll(extractedPath, fileio.DefaultDirectoryPermissions))
			require.NoError(t, rw.WriteFile(filepath.Join(extractedPath, "web.container"), []byte("[Container]\nImage=quay.io/acme/web:v1\n"), fileio.DefaultFilePermissions))
			require.NoError(t, rw.WriteFile(filepath.Join(extractedPath, "compose.yaml"), []byte("services: {}\n"), fileio.DefaultFilePermissions))
			binDir := t.TempDir()
			for _, name := range []string{"podman", "podman-compose", "skopeo"} {
				require.NoError(t, os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o600))
				require.NoError(t, os.Chmod(filepath.Join(binDir, name), 0o700))
			}
			t.Setenv("PATH", binDir)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, executer.NewMockExecuter(ctrl), rw, util.NewPollConfig())
			var apiSpec v1beta1.ApplicationProviderSpec
			if tc.appType == v1beta1.AppTypeCompose {
				app := v1beta1.ComposeApplication{Name: lo.ToPtr("app"), AppType: tc.appType}
				require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: imageRef}))
				require.NoError(t, apiSpec.FromComposeApplication(app))
			} else {
				app := v1beta1.QuadletApplication{Name: lo.ToPtr("app"), AppType: tc.appType}
				require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: imageRef}))
				require.NoError(t, apiSpec.FromQuadletApplication(app))
			}
			cache := map[string]*AppData{"app-337945": {
				TmpPath: extractedPath, ParentReference: imageRef, ParentDigest: tc.digest,
				ParentOCIType: dependency.OCITypePodmanArtifact,
			}}
			providers, err := FromDeviceSpec(t.Context(), logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil }, nil,
				func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil },
				&v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{apiSpec}}, WithAppDataCache(cache))
			require.NoError(t, err)
			require.Len(t, providers, 1)
			compose := providers[0]
			spec := compose.Spec()
			spec.Path = installedPath
			spec.ImageDigest = "stale-digest"
			spec.ImageOCIType = "stale-type"
			require.NoError(t, compose.Install(context.Background()))
			require.Equal(t, tc.digest, spec.ImageDigest)
			require.Equal(t, string(dependency.OCITypePodmanArtifact), spec.ImageOCIType)
			installed, err := rw.ReadFile(filepath.Join(installedPath, "compose.yaml"))
			require.NoError(t, err)
			require.Equal(t, "services: {}\n", string(installed))

		})
	}
}

func TestWhenCollectedParentExtractionIsMissingItShouldExtractFromTheImageAgain(t *testing.T) {
	const (
		imageRef    = "quay.io/acme/compose-bundle:v1"
		imageID     = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		digestValue = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	for _, tc := range []struct{ name, digest, expectedDigest string }{
		{name: "When the retained extraction is missing it should extract the verified image", digest: digestValue, expectedDigest: digestValue},
		{name: "When a local parent has no registry digest it should still install its contents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			require.NoError(t, os.MkdirAll(filepath.Join(root, "mount"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "mount", "compose.yaml"), []byte("services: {}\n"), 0o600))
			volumeManager, err := NewVolumeManager(log.NewPrefixLogger("test"), "app", v1beta1.AppTypeCompose, v1beta1.RootUsername, nil)
			require.NoError(t, err)
			spec := &ApplicationSpec{
				Name: "app", ID: "app-id", Path: "/installed-app", Image: imageRef,
				Volume: volumeManager,
			}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", imageRef).Return("", "", 0)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Id}}", imageRef).Return(imageID, "", 0)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", imageID).Return(tc.digest, "", 0)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", imageID).Return("/mount", "", 0)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", imageID).Return("", "", 0)
			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, rw, util.NewPollConfig())
			compose := &composeProvider{
				imageRef: imageRef, spec: spec, readWriter: rw, podman: podman,
				log: log.NewPrefixLogger("test"),
				AppData: &AppData{
					TmpPath: "/missing-collected-parent", ParentReference: imageRef,
					ParentDigest: digestValue, ParentOCIType: dependency.OCITypePodmanImage,
				},
			}

			require.NoError(t, compose.Install(context.Background()))
			require.Equal(t, tc.expectedDigest, spec.ImageDigest)
			installed, err := rw.ReadFile(filepath.Join(spec.Path, "compose.yaml"))
			require.NoError(t, err)
			require.Equal(t, "services: {}\n", string(installed))

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
					require.Contains(string(envFile), fmt.Sprintf("%s=\"%s\"", k, v))
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

func TestComposeEnsureDependencies(t *testing.T) {
	tests := []struct {
		name           string
		appName        string
		volumes        *[]v1beta1.ApplicationVolume
		commandChecker commandChecker
		setupMocks     func(*executer.MockExecuter)
		wantErr        error
	}{
		{
			name:           "missing all binaries returns ErrAppDependency",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return false },
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "missing podman binary returns ErrAppDependency",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return cmd == "docker-compose" || cmd == "skopeo" },
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "missing compose binary returns ErrAppDependency",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return cmd == "podman" || cmd == "skopeo" },
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "missing skopeo binary returns ErrAppDependency",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return cmd == "podman" || cmd == "docker-compose" },
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "all binaries available",
			appName:        "app",
			volumes:        nil,
			commandChecker: func(cmd string) bool { return cmd == "podman" || cmd == "docker-compose" || cmd == "skopeo" },
			wantErr:        nil,
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
					Return("podman version 5.4.0", "", 0)
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
					Return("podman version 5.5.0", "", 0)
			},
			wantErr: nil,
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

			composeApp := v1beta1.ComposeApplication{
				AppType: v1beta1.AppTypeCompose,
				Name:    lo.ToPtr(tt.appName),
				Volumes: tt.volumes,
			}
			err := composeApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content: lo.ToPtr(util.NewComposeSpec()),
						Path:    "docker-compose.yml",
					},
				},
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

			if tt.commandChecker != nil {
				composeProvider.commandChecker = tt.commandChecker
			}

			err = composeProvider.EnsureDependencies(ctx)
			if tt.wantErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

func TestWhenArtifactDigestIsObservedItShouldExtractDigestPinnedReference(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const (
		imageRef    = "quay.io/acme/compose-bundle:v1"
		digestValue = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	observedDigest, err := digest.Parse(digestValue)
	require.NoError(err)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	volumes, err := NewVolumeManager(logger, "app", v1beta1.AppTypeCompose, v1beta1.RootUsername, nil)
	require.NoError(err)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", imageRef).Return("", "", 1)
	gomock.InOrder(
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", imageRef).Return("{}", "", 0),
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", imageRef).Return(`{"Digest":"`+digestValue+`"}`, "", 0),
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "extract", observedDigest.Encoded(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, args ...string) (string, string, int) {
				require.NoError(rw.WriteFile(filepath.Join(args[len(args)-1], "compose.yaml"), []byte("services: {}\n"), fileio.DefaultFilePermissions))
				return "", "", 0
			}),
	)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0).AnyTimes()
	spec := &ApplicationSpec{Name: "app", ID: "app-id", Path: "/installed-app", Image: imageRef, Volume: volumes}
	compose := &composeProvider{imageRef: imageRef, spec: spec, readWriter: rw, log: logger,
		podman: client.NewPodman(logger, exec, rw, util.NewPollConfig())}
	require.NoError(compose.Install(t.Context()))
	require.Equal(digestValue, spec.ImageDigest)
	require.Equal(string(dependency.OCITypePodmanArtifact), spec.ImageOCIType)
	contents, err := rw.ReadFile(filepath.Join(spec.Path, "compose.yaml"))
	require.NoError(err)
	require.Equal("services: {}\n", string(contents))
}

func TestWhenArtifactDigestInspectionFailsItShouldStillInstallParentContents(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const imageRef = "quay.io/acme/compose-bundle:v1"
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	volumes, err := NewVolumeManager(logger, "app", v1beta1.AppTypeCompose, v1beta1.RootUsername, nil)
	require.NoError(err)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", imageRef).Return("", "", 1)
	gomock.InOrder(
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", imageRef).Return("{}", "", 0),
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", imageRef).Return("", "digest inspection unavailable", 1),
	)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "extract", imageRef, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, args ...string) (string, string, int) {
			require.NoError(rw.WriteFile(filepath.Join(args[len(args)-1], "compose.yaml"), []byte("services: {}\n"), fileio.DefaultFilePermissions))
			return "", "", 0
		})
	spec := &ApplicationSpec{Name: "app", ID: "app-id", Path: "/installed-app", Image: imageRef, Volume: volumes}
	compose := &composeProvider{imageRef: imageRef, spec: spec, readWriter: rw, log: logger,
		podman: client.NewPodman(logger, exec, rw, util.NewPollConfig())}
	require.NoError(compose.Install(t.Context()))
	require.Empty(spec.ImageDigest)
	require.Equal(string(dependency.OCITypePodmanArtifact), spec.ImageOCIType)
	contents, err := rw.ReadFile(filepath.Join(spec.Path, "compose.yaml"))
	require.NoError(err)
	require.Equal("services: {}\n", string(contents))
}
