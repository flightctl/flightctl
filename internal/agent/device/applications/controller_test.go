package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
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

type testApp struct {
	name  string
	image string
}

func TestParseAppProviders(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name       string
		setupMocks func(
			mockExecuter *executer.MockExecuter,
			imageConfig string,
		)
		apps         []testApp
		labels       map[string]string
		wantNames    []string
		wantIDPrefix []string
		wantErr      error
	}{
		{
			name: "valid app type",
			setupMocks: func(
				mockExecuter *executer.MockExecuter,
				imageConfig string,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", gomock.Any()).Return("/mount", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", gomock.Any()).Return("", "", 0),
				)
			},
			apps: []testApp{{name: "app1", image: "quay.io/org/app1:latest"}},
			labels: map[string]string{
				AppTypeLabel: string(v1beta1.AppTypeCompose),
			},
			wantNames:    []string{"app1"},
			wantIDPrefix: []string{"app1-"},
		},
		{
			name: "unsupported app type",
			setupMocks: func(
				mockExecuter *executer.MockExecuter,
				imageConfig string,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0),
				)
			},
			apps: []testApp{{name: "app1", image: "quay.io/org/app1:latest"}},
			labels: map[string]string{
				AppTypeLabel: "invalid",
			},
			wantErr: errors.ErrAppLabel,
		},
		{
			name: "missing app type",
			setupMocks: func(
				mockExecuter *executer.MockExecuter,
				imageConfig string,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", gomock.Any()).Return("/mount", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", gomock.Any()).Return("", "", 0),
				)
			},
			apps:   []testApp{{name: "app1", image: "quay.io/org/app1:latest"}},
			labels: map[string]string{},
		},
		{
			name: "missing app name populated by provider image",
			setupMocks: func(
				mockExecuter *executer.MockExecuter,
				imageConfig string,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", gomock.Any()).Return("/mount", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", gomock.Any()).Return("", "", 0),
				)
			},
			apps: []testApp{{name: "", image: "quay.io/org/app1:latest"}},
			labels: map[string]string{
				AppTypeLabel: string(v1beta1.AppTypeCompose),
			},
			wantNames:    []string{"quay.io/org/app1:latest"},
			wantIDPrefix: []string{"quay_io_org_app1_latest-"},
		},
		{
			name: "no apps",
			setupMocks: func(
				mockExecuter *executer.MockExecuter,
				imageConfig string,
			) {
			},
			apps: []testApp{},
		},
		{
			name: "multiple apps",
			setupMocks: func(
				mockExecuter *executer.MockExecuter,
				imageConfig string,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					// inspect all apps first
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0),

					// then verify (mount/unmount) all apps
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", gomock.Any()).Return("/mount", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", gomock.Any()).Return("", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", gomock.Any()).Return("/mount", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", gomock.Any()).Return("", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", gomock.Any()).Return("/mount", "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", gomock.Any()).Return("", "", 0),
				)
			},
			apps: []testApp{
				{name: "app1", image: "quay.io/org/app1:latest"},
				{name: "", image: "quay.io/org/app2:latest"},
				{name: "app2", image: "quay.io/org/app2:latest"},
			},
			labels: map[string]string{
				AppTypeLabel: string(v1beta1.AppTypeCompose),
			},
			wantNames:    []string{"app1", "quay.io/org/app2:latest", "app2"},
			wantIDPrefix: []string{"app1-", "quay_io_org_app2_latest", "app2"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.TraceLevel)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
			err := readWriter.MkdirAll("/mount", fileio.DefaultDirectoryPermissions)
			require.NoError(err)
			err = readWriter.WriteFile("/mount/podman-compose.yaml", []byte(util.NewComposeSpec()), fileio.DefaultFilePermissions)
			require.NoError(err)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			spec, err := newTestDeviceSpec(tc.apps)
			require.NoError(err)

			execMock := executer.NewMockExecuter(ctrl)

			imageConfig, err := newImageConfig(tc.labels)
			require.NoError(err)
			mockPodman := client.NewPodman(log, execMock, readWriter, util.NewPollConfig())

			tc.setupMocks(execMock, imageConfig)

			providers, err := provider.FromDeviceSpec(ctx, log, mockPodman, readWriter, spec, provider.WithProviderTypes(v1beta1.ImageApplicationProviderType))
			if tc.wantErr != nil {
				require.ErrorIs(err, tc.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(len(tc.apps), len(providers))
			// ensure name is populated
			for i, provider := range providers {
				// verify deps
				err := provider.Verify(ctx)
				require.NoError(err)
				require.NotEmpty(provider.Name())
				if len(tc.wantNames) > 0 {
					require.Equal(tc.wantNames[i], provider.Name())
				}
				if len(tc.wantIDPrefix) > 0 {
					require.True(strings.HasPrefix(provider.Spec().ID, tc.wantIDPrefix[i]))
				}
			}
		})
	}
}

func newImageConfig(labels map[string]string) (string, error) {
	inspectData := []client.PodmanInspect{
		{
			Config: client.PodmanContainerConfig{
				Labels: labels,
			},
		},
	}

	imageConfigBytes, err := json.Marshal(inspectData)
	if err != nil {
		return "", err
	}
	return string(imageConfigBytes), nil
}

func newTestDeviceSpec(appSpecs []testApp) (*v1beta1.DeviceSpec, error) {
	var applications []v1beta1.ApplicationProviderSpec
	for _, spec := range appSpecs {
		app := v1beta1.ApplicationProviderSpec{
			Name:    lo.ToPtr(spec.name),
			AppType: v1beta1.AppTypeCompose,
		}
		provider := v1beta1.ImageApplicationProviderSpec{
			Image: spec.image,
		}
		if err := app.FromImageApplicationProviderSpec(provider); err != nil {
			return nil, err
		}
		applications = append(applications, app)
	}
	return &v1beta1.DeviceSpec{
		Applications: &applications,
	}, nil
}

func createTestLabels(labels map[string]string) (string, error) {
	inspectData := []client.PodmanInspect{
		{
			Config: client.PodmanContainerConfig{
				Labels: labels,
			},
		},
	}

	data, err := json.Marshal(inspectData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal ImageConfig: %w", err)
	}
	return string(data), nil
}

func TestControllerSync(t *testing.T) {
	require := require.New(t)

	app1Image := "quay.io/org/app1:v1.1.0"
	app2Image := "quay.io/org/app2:v1.1.0"
	app1LabelMap := map[string]string{
		"com.example.one":   "value1",
		"appType":           "compose",
		"com.example.three": "value3",
	}
	app1Labels, err := createTestLabels(app1LabelMap)
	require.NoError(err)
	require.NotEmpty(app1Labels)

	type testRendered struct {
		version string
		apps    []testApp
	}

	type transitionStep struct {
		current []testRendered
		desired []testRendered
	}
	testCases := []struct {
		name       string
		steps      []transitionStep
		setupMocks func(
			mockAppManager *MockManager,
			mockExecuter *executer.MockExecuter,
		)
	}{
		{
			name: "add 2 apps from none",
			steps: []transitionStep{
				{
					current: []testRendered{
						{version: "1", apps: []testApp{}}, // empty
					},
					desired: []testRendered{
						{version: "2", apps: []testApp{
							{name: "app1", image: app1Image},
							{name: "app2", image: app2Image},
						}},
					},
				},
			},
			setupMocks: func(
				mockAppManager *MockManager,
				mockExecuter *executer.MockExecuter,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					// inspect to get app type from image labels
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0),

					// ensure app1
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),
					// ensure app2
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),
				)
			},
		},
		{
			name: "add 2 apps then remove",
			steps: []transitionStep{
				{
					current: []testRendered{
						{version: "1", apps: []testApp{}}, // empty
					},
					desired: []testRendered{
						{version: "2", apps: []testApp{
							{name: "app1", image: app1Image},
							{name: "app2", image: app2Image},
						}},
					},
				},
				{
					current: []testRendered{
						{version: "2", apps: []testApp{
							{name: "app1", image: app1Image},
							{name: "app2", image: app2Image},
						}},
					},
					desired: []testRendered{
						{version: "3", apps: []testApp{}}, // remove all
					},
				},
			},
			setupMocks: func(
				mockAppManager *MockManager,
				mockExecuter *executer.MockExecuter,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					// rendered version 1 -> 2
					// inspect to get app type from image labels
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0),

					// ensure app1
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),
					// ensure app2
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),

					// rendered version 2 -> 3
					// inspect current providers
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0),
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0),

					// remove app1
					mockAppManager.EXPECT().Remove(gomock.Any(), gomock.Any()).Return(nil),
					// remove app2
					mockAppManager.EXPECT().Remove(gomock.Any(), gomock.Any()).Return(nil),
				)
			},
		},
		{
			name: "removing and adding apps that share the same image",
			steps: []transitionStep{
				{
					current: []testRendered{
						{version: "0", apps: []testApp{}},
					},
					desired: []testRendered{
						{version: "1", apps: []testApp{
							{name: "app1", image: app1Image},
							{name: "app2", image: app2Image},
						}},
					},
				},
				{
					// upgrade to v2
					current: []testRendered{
						{version: "1", apps: []testApp{
							{name: "app1", image: app1Image},
							{name: "app2", image: app2Image},
						}},
					},
					desired: []testRendered{
						{version: "2", apps: []testApp{
							{name: "app2", image: app2Image},
							{name: "app3", image: app1Image}, // reusing app1Image for app3l
						}},
					},
				},
				{
					// rollback to v1
					current: []testRendered{
						{version: "2", apps: []testApp{
							{name: "app2", image: app2Image},
							{name: "app3", image: app1Image},
						}},
					},
					desired: []testRendered{
						{version: "1", apps: []testApp{
							{name: "app1", image: app1Image},
							{name: "app2", image: app2Image},
						}},
					},
				},
			},
			setupMocks: func(
				mockAppManager *MockManager,
				mockExecuter *executer.MockExecuter,
			) {
				mockExecuter.EXPECT().
					ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).
					Return("", "", 0).
					AnyTimes()
				gomock.InOrder(
					// initial deployment app1 and app2
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0), // inspect desired app1
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0), // inspect desired app2
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),                                           // ensure app1
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),                                           // ensure app2

					// replace app1 with app3 using same image, keep app2
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0), // inspect current app1
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0), // inspect current app2
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0), // inspect desired app2
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0), // inspect desired app3 (uses app1Image)
					mockAppManager.EXPECT().Remove(gomock.Any(), gomock.Any()).Return(nil),                                           // remove app1
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),                                           // ensure app3
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),                                           // ensure app2

					// replace app3 with app1 (shared image)
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0), // inspect current app2
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0), // inspect current app3
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app1Image).Return(app1Labels, "", 0), // inspect desired app1 (uses same image as app3)
					mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", app2Image).Return(app1Labels, "", 0), // inspect desired app2
					mockAppManager.EXPECT().Remove(gomock.Any(), gomock.Any()).Return(nil),                                           // remove app3
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),                                           // ensure app1 (reusing app1Image from app3)
					mockAppManager.EXPECT().Ensure(gomock.Any(), gomock.Any()).Return(nil),                                           // ensure app2
				)
			},
		},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExecuter := executer.NewMockExecuter(ctrl)
			mockAppManager := NewMockManager(ctrl)
			podmanClient := client.NewPodman(log, mockExecuter, readWriter, util.NewPollConfig())

			controller := NewController(podmanClient, mockAppManager, readWriter, log, "2025-01-01T00:00:00Z")

			countainerMountDir := "/mount"
			err = readWriter.MkdirAll(countainerMountDir, fileio.DefaultDirectoryPermissions)
			require.NoError(err)
			embeddedAppPath := filepath.Join(countainerMountDir, "podman-compose.yaml")
			err = readWriter.WriteFile(embeddedAppPath, []byte(util.NewComposeSpec()), fileio.DefaultFilePermissions)
			require.NoError(err)

			tt.setupMocks(mockAppManager, mockExecuter)

			for i, step := range tt.steps {
				t.Logf("Applying %d...", i+1)

				var currentApps []testApp
				for _, version := range step.current {
					currentApps = append(currentApps, version.apps...)
				}

				var desiredApps []testApp
				for _, version := range step.desired {
					desiredApps = append(desiredApps, version.apps...)
				}

				// generate current and desired for each step
				current, err := newTestDeviceSpec(currentApps)
				require.NoError(err)
				desired, err := newTestDeviceSpec(desiredApps)
				require.NoError(err)

				err = controller.Sync(ctx, current, desired)
				require.NoError(err)
			}
		})
	}
}
