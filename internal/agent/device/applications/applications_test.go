package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
		expectedStatus        v1alpha1.ApplicationStatusType
		expectedSummaryStatus v1alpha1.ApplicationsSummaryStatusType
		expected              v1alpha1.AppType
	}{
		{
			name:                  "app created no workloads",
			expectedReady:         "0/0",
			expectedStatus:        v1alpha1.ApplicationStatusUnknown,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              v1alpha1.AppTypeCompose,
		},
		{
			name: "app single container preparing to start init",
			workloads: []Workload{
				{
					Status: StatusInit,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1alpha1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              v1alpha1.AppTypeCompose,
		},
		{
			name: "app single container preparing to start created",
			workloads: []Workload{
				{
					Status: StatusCreated,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1alpha1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              v1alpha1.AppTypeCompose,
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
			expectedStatus:        v1alpha1.ApplicationStatusStarting,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              v1alpha1.AppTypeCompose,
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
			expectedStatus:        v1alpha1.ApplicationStatusStarting,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              v1alpha1.AppTypeCompose,
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
			expectedStatus:        v1alpha1.ApplicationStatusError,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusError,
			expected:              v1alpha1.AppTypeCompose,
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
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              v1alpha1.AppTypeCompose,
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
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              v1alpha1.AppTypeCompose,
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
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
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
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
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
			expectedStatus:        v1alpha1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
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
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
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
			expectedStatus:        v1alpha1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)

			mockExec := executer.NewMockExecuter(ctrl)
			podman := client.NewPodman(log, mockExec, readWriter, util.NewBackoff())

			appImage := "quay.io/flightctl-tests/alpine:v1"
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", appImage}).Return("", "", 0)

			spec := v1alpha1.InlineApplicationProviderSpec{
				Inline: []v1alpha1.ApplicationContent{
					{
						Content: lo.ToPtr(util.NewComposeSpec()),
						Path:    "docker-compose.yml",
					},
				},
			}

			providerSpec := v1alpha1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: lo.ToPtr(v1alpha1.AppTypeCompose),
			}
			err := providerSpec.FromInlineApplicationProviderSpec(spec)
			require.NoError(err)
			desired := v1alpha1.DeviceSpec{
				Applications: &[]v1alpha1.ApplicationProviderSpec{
					providerSpec,
				},
			}
			providers, err := provider.FromDeviceSpec(context.Background(), log, podman, readWriter, &desired)
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
