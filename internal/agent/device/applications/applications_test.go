package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApplicationStatus(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name                  string
		workloads             []Workload
		expectedReady         string
		expectedRestarts      int
		expectedStatus        v1beta1.ApplicationStatusType
		expectedSummaryStatus v1beta1.ApplicationsSummaryStatusType
		expected              v1beta1.AppType
	}{
		{
			name:                  "app created no workloads",
			expectedReady:         "0/0",
			expectedStatus:        v1beta1.ApplicationStatusUnknown,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app single container preparing to start init",
			workloads: []Workload{
				{
					Status: StatusInit,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app single container preparing to start created",
			workloads: []Workload{
				{
					Status: StatusCreated,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app multiple workloads starting init",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusInit,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app multiple workloads starting created",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusCreated,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app errored",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusDie,
				},
				{
					Name:   "container2",
					Status: StatusDie,
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app running degraded",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusDie,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app running degraded",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusDied,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app running healthy",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusRunning,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app running healthy with restarts",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusRunning,
					Restarts: 1,
				},
				{
					Name:     "container2",
					Status:   StatusRunning,
					Restarts: 2,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
			expectedRestarts:      3,
		},
		{
			name: "app has all workloads exited",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusExited,
				},
				{
					Name:   "container2",
					Status: StatusExited,
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app has one workloads exited",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusRunning,
				},
				{
					Name:   "container2",
					Status: StatusExited,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app with single container has exited",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusExited,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			tempDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
			)

			mockExec := executer.NewMockExecuter(ctrl)
			podman := client.NewPodman(log, mockExec, readWriter, util.NewPollConfig())

			spec := v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content: lo.ToPtr(util.NewComposeSpec()),
						Path:    "docker-compose.yml",
					},
				},
			}

			providerSpec := v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: v1beta1.AppTypeCompose,
			}
			err := providerSpec.FromInlineApplicationProviderSpec(spec)
			require.NoError(err)
			desired := v1beta1.DeviceSpec{
				Applications: &[]v1beta1.ApplicationProviderSpec{
					providerSpec,
				},
			}
			providers, err := provider.FromDeviceSpec(context.Background(), log, podman, nil, readWriter, &desired)
			require.NoError(err)
			require.Len(providers, 1)
			application := NewApplication(providers[0])
			if len(tt.workloads) > 0 {
				application.workloads = tt.workloads
			}
			status, summary, err := application.Status()
			require.NoError(err)

			require.Equal(tt.expectedReady, status.Ready)
			require.Equal(tt.expectedRestarts, status.Restarts)
			require.Equal(tt.expectedStatus, status.Status)
			require.Equal(tt.expectedSummaryStatus, summary.Status)
		})
	}
}

func TestNewHelmApplication(t *testing.T) {
	tests := []struct {
		name                       string
		appName                    string
		namespace                  string
		valuesFiles                []string
		values                     map[string]interface{}
		expectedNamespace          string
		expectedValuesFiles        []string
		expectedProviderValuesPath string
	}{
		{
			name:                       "with namespace and values files",
			appName:                    "my-helm-app",
			namespace:                  "production",
			valuesFiles:                []string{"values.yaml", "values-prod.yaml"},
			values:                     nil,
			expectedNamespace:          "production",
			expectedValuesFiles:        []string{"values.yaml", "values-prod.yaml"},
			expectedProviderValuesPath: "",
		},
		{
			name:                       "with inline values",
			appName:                    "my-helm-app",
			namespace:                  "default",
			valuesFiles:                nil,
			values:                     map[string]interface{}{"replicaCount": 3},
			expectedNamespace:          "default",
			expectedValuesFiles:        nil,
			expectedProviderValuesPath: "/var/lib/flightctl/helm/values/my-helm-app/flightctl-values.yaml",
		},
		{
			name:                       "with all options",
			appName:                    "full-app",
			namespace:                  "staging",
			valuesFiles:                []string{"base.yaml"},
			values:                     map[string]interface{}{"key": "value"},
			expectedNamespace:          "staging",
			expectedValuesFiles:        []string{"base.yaml"},
			expectedProviderValuesPath: "/var/lib/flightctl/helm/values/full-app/flightctl-values.yaml",
		},
		{
			name:                       "empty values does not set provider path",
			appName:                    "empty-app",
			namespace:                  "",
			valuesFiles:                nil,
			values:                     map[string]interface{}{},
			expectedNamespace:          "",
			expectedValuesFiles:        nil,
			expectedProviderValuesPath: "",
		},
		{
			name:                       "nil image provider",
			appName:                    "nil-provider-app",
			namespace:                  "",
			valuesFiles:                nil,
			values:                     nil,
			expectedNamespace:          "",
			expectedValuesFiles:        nil,
			expectedProviderValuesPath: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			mockProvider := &helmMockProvider{
				name:        tc.appName,
				namespace:   tc.namespace,
				valuesFiles: tc.valuesFiles,
				values:      tc.values,
			}

			app := NewHelmApplication(mockProvider)

			require.Equal(tc.appName, app.Name())
			require.Equal(v1beta1.AppTypeHelm, app.AppType())
			require.Equal(v1beta1.ApplicationStatusUnknown, app.status.Status)

			helmSpec, ok := app.ActionSpec().(lifecycle.HelmSpec)
			require.True(ok, "ActionSpec should be HelmSpec")
			require.Equal(tc.expectedNamespace, helmSpec.Namespace)
			require.Equal(tc.expectedValuesFiles, helmSpec.ValuesFiles)
			require.Equal(tc.expectedProviderValuesPath, helmSpec.ProviderValuesPath)
		})
	}
}

type helmMockProvider struct {
	name        string
	namespace   string
	valuesFiles []string
	values      map[string]interface{}
}

func (m *helmMockProvider) Name() string {
	return m.name
}

func (m *helmMockProvider) ID() string {
	return m.namespace + "/" + m.name
}

func (m *helmMockProvider) Spec() *provider.ApplicationSpec {
	volManager, _ := provider.NewVolumeManager(nil, m.name, v1beta1.AppTypeHelm, nil)

	var imageProvider *v1beta1.ImageApplicationProviderSpec
	if m.namespace != "" || m.valuesFiles != nil || m.values != nil {
		imageProvider = &v1beta1.ImageApplicationProviderSpec{}
		if m.namespace != "" {
			imageProvider.Namespace = lo.ToPtr(m.namespace)
		}
		if m.valuesFiles != nil {
			imageProvider.ValuesFiles = lo.ToPtr(m.valuesFiles)
		}
		if m.values != nil {
			imageProvider.Values = lo.ToPtr(m.values)
		}
	}

	return &provider.ApplicationSpec{
		ID:            m.namespace + "/" + m.name,
		Name:          m.name,
		Volume:        volManager,
		AppType:       v1beta1.AppTypeHelm,
		ImageProvider: imageProvider,
	}
}

func (m *helmMockProvider) OCITargets(ctx context.Context, pullSecret *client.PullConfig) ([]dependency.OCIPullTarget, error) {
	return nil, nil
}

func (m *helmMockProvider) Verify(ctx context.Context) error {
	return nil
}

func (m *helmMockProvider) Install(ctx context.Context) error {
	return nil
}

func (m *helmMockProvider) Remove(ctx context.Context) error {
	return nil
}
