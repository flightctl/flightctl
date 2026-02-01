package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
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

			inlineSpec := v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content: lo.ToPtr(util.NewComposeSpec()),
						Path:    "docker-compose.yml",
					},
				},
			}

			composeApp := v1beta1.ComposeApplication{
				Name:    lo.ToPtr("app"),
				AppType: v1beta1.AppTypeCompose,
			}
			err := composeApp.FromInlineApplicationProviderSpec(inlineSpec)
			require.NoError(err)

			var providerSpec v1beta1.ApplicationProviderSpec
			err = providerSpec.FromComposeApplication(composeApp)
			require.NoError(err)
			desired := v1beta1.DeviceSpec{
				Applications: &[]v1beta1.ApplicationProviderSpec{
					providerSpec,
				},
			}
			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}
			providers, err := provider.FromDeviceSpec(context.Background(), log, podmanFactory, nil, rwFactory, &desired)
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
