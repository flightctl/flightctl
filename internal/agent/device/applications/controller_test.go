package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type testApp struct {
	name  string
	image string
}

func TestParseApps(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name         string
		apps         []testApp
		labels       map[string]string
		wantNames    []string
		wantIDPrefix []string
		wantErr      error
	}{
		{
			name: "valid app type",
			apps: []testApp{{name: "app1", image: "quay.io/org/app1:latest"}},
			labels: map[string]string{
				AppTypeLabel: string(AppCompose),
			},
			wantNames:    []string{"app1"},
			wantIDPrefix: []string{"app1-"},
		},
		{
			name: "unsupported app type",
			apps: []testApp{{name: "app1", image: "quay.io/org/app1:latest"}},
			labels: map[string]string{
				AppTypeLabel: "invalid",
			},
			wantErr: errors.ErrParseAppType,
		},
		{
			name:    "missing app type",
			apps:    []testApp{{name: "app1", image: "quay.io/org/app1:latest"}},
			labels:  map[string]string{},
			wantErr: errors.ErrParseAppType,
		},
		{
			name: "missing app name populated by provider image",
			apps: []testApp{{name: "", image: "quay.io/org/app1:latest"}},
			labels: map[string]string{
				AppTypeLabel: string(AppCompose),
			},
			wantNames:    []string{"quay.io/org/app1:latest"},
			wantIDPrefix: []string{"quay_io_org_app1_latest-"},
		},
		{
			name: "no apps",
			apps: []testApp{},
		},
		{
			name: "multiple apps",
			apps: []testApp{
				{name: "app1", image: "quay.io/org/app1:latest"},
				{name: "", image: "quay.io/org/app2:latest"},
				{name: "app2", image: "quay.io/org/app2:latest"},
			},
			labels: map[string]string{
				AppTypeLabel: string(AppCompose),
			},
			wantNames:    []string{"app1", "quay.io/org/app2:latest", "app2"},
			wantIDPrefix: []string{"app1-", "quay_io_org_app2_latest", "app2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			log := log.NewPrefixLogger("test")
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			spec, err := newTestDeviceSpec(tc.apps)
			require.NoError(err)
			execMock := executer.NewMockExecuter(ctrl)

			imageConfig, err := newImageConfig(tc.labels)
			t.Logf("imageConfig: %s", imageConfig)
			require.NoError(err)
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(imageConfig, "", 0).Times(len(tc.apps))

			mockPodman := client.NewPodman(log, execMock, newTestBackoff())
			apps, err := parseApps(ctx, mockPodman, spec)
			if tc.wantErr != nil {
				require.ErrorIs(err, tc.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(len(tc.apps), len(apps.ImageBased()))
			// ensure name is populated
			for i, app := range apps.ImageBased() {
				require.NotEmpty(app.Name())
				if len(tc.wantNames) > 0 {
					require.Equal(tc.wantNames[i], app.Name())
				}
				if len(tc.wantIDPrefix) > 0 {
					require.True(strings.HasPrefix(app.ID(), tc.wantIDPrefix[i]))
				}
			}
		})
	}
}

func newImageConfig(labels map[string]string) (string, error) {
	type inspect struct {
		Config client.ImageConfig `json:"Config"`
	}

	inspectData := []inspect{
		{
			Config: client.ImageConfig{
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

func newTestDeviceSpec(appSpecs []testApp) (*v1alpha1.DeviceSpec, error) {
	var applications []v1alpha1.ApplicationProviderSpec
	for _, spec := range appSpecs {
		app := v1alpha1.ApplicationProviderSpec{
			Name: lo.ToPtr(spec.name),
		}
		provider := v1alpha1.ImageApplicationProvider{
			Image: spec.image,
		}
		if err := app.FromImageApplicationProvider(provider); err != nil {
			return nil, err
		}
		applications = append(applications, app)
	}
	return &v1alpha1.DeviceSpec{
		Applications: &applications,
	}, nil
}

func createTestLabels(labels map[string]string) (string, error) {
	type inspect struct {
		Config client.ImageConfig `json:"Config"`
	}

	inspectData := []inspect{
		{
			Config: client.ImageConfig{
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
				// ensure app1
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", app1Image}).Return(app1Labels, "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", app1Image}).Return("/mount", "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", app1Image}).Return("", "", 0)
				mockAppManager.EXPECT().Ensure(gomock.Any()).Return(nil)

				// ensure app2
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", app2Image}).Return(app1Labels, "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", app2Image}).Return("/mount", "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", app2Image}).Return("", "", 0)
				mockAppManager.EXPECT().Ensure(gomock.Any()).Return(nil)
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
				// renderedVersion 1 -> 2 (add apps)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", app1Image}).Return(app1Labels, "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", app2Image}).Return(app1Labels, "", 0)

				// ensure app1
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", app1Image}).Return("/mount", "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", app1Image}).Return("", "", 0)
				mockAppManager.EXPECT().Ensure(gomock.Any()).Return(nil)

				// ensure app2
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"unshare", "podman", "image", "mount", app2Image}).Return("/mount", "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "unmount", app2Image}).Return("", "", 0)
				mockAppManager.EXPECT().Ensure(gomock.Any()).Return(nil)

				// renderedVersion 2 -> 3 (remove apps)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", app1Image}).Return(app1Labels, "", 0)
				mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"inspect", app2Image}).Return(app1Labels, "", 0)

				// remove app1
				mockAppManager.EXPECT().Remove(gomock.Any()).Return(nil)
				// remove app2
				mockAppManager.EXPECT().Remove(gomock.Any()).Return(nil)
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
			podmanClient := client.NewPodman(log, mockExecuter, newTestBackoff())

			controller := NewController(podmanClient, mockAppManager, readWriter, log)

			countainerMountDir := "/mount"
			err = readWriter.MkdirAll(countainerMountDir, fileio.DefaultDirectoryPermissions)
			require.NoError(err)
			embeddedAppPath := filepath.Join(countainerMountDir, "podman-compose.yaml")
			err = readWriter.WriteFile(embeddedAppPath, []byte(composeSpec), fileio.DefaultFilePermissions)
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

const composeSpec = `
version: "3"
services:
  test-service:
    image: alpine
`
