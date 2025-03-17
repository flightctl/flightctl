package applications

import (
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/stretchr/testify/require"
)

func TestApplicationStatus(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name                  string
		containers            []Container
		expectedReady         string
		expectedRestarts      int
		expectedStatus        v1alpha1.ApplicationStatusType
		expectedSummaryStatus v1alpha1.ApplicationsSummaryStatusType
		expected              AppType
	}{
		{
			name:                  "app created no containers",
			expectedReady:         "0/0",
			expectedStatus:        v1alpha1.ApplicationStatusUnknown,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              AppCompose,
		},
		{
			name: "app single container preparing to start init",
			containers: []Container{
				{
					Status: ContainerStatusInit,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1alpha1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              AppCompose,
		},
		{
			name: "app single container preparing to start created",
			containers: []Container{
				{
					Status: ContainerStatusCreated,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1alpha1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              AppCompose,
		},
		{
			name: "app multiple containers starting init",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusInit,
				},
				{
					Name:   "container2",
					Status: ContainerStatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1alpha1.ApplicationStatusStarting,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              AppCompose,
		},
		{
			name: "app multiple containers starting created",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusCreated,
				},
				{
					Name:   "container2",
					Status: ContainerStatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1alpha1.ApplicationStatusStarting,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              AppCompose,
		},
		{
			name: "app errored",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusDie,
				},
				{
					Name:   "container2",
					Status: ContainerStatusDie,
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1alpha1.ApplicationStatusError,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusError,
			expected:              AppCompose,
		},
		{
			name: "app running degraded",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusDie,
				},
				{
					Name:   "container2",
					Status: ContainerStatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              AppCompose,
		},
		{
			name: "app running degraded",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusDied,
				},
				{
					Name:   "container2",
					Status: ContainerStatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              AppCompose,
		},
		{
			name: "app running healthy",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusRunning,
				},
				{
					Name:   "container2",
					Status: ContainerStatusRunning,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app running healthy with restarts",
			containers: []Container{
				{
					Name:     "container1",
					Status:   ContainerStatusRunning,
					Restarts: 1,
				},
				{
					Name:     "container2",
					Status:   ContainerStatusRunning,
					Restarts: 2,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
			expectedRestarts:      3,
		},
		{
			name: "app has all containers exited",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusExited,
				},
				{
					Name:   "container2",
					Status: ContainerStatusExited,
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1alpha1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app has one containers exited",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusRunning,
				},
				{
					Name:   "container2",
					Status: ContainerStatusExited,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app with single container has exited",
			containers: []Container{
				{
					Name:   "container1",
					Status: ContainerStatusExited,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1alpha1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := v1alpha1.ImageApplicationProviderSpec{
				Image: "image",
			}
			name := "testApp"
			id := client.SanitizePodmanLabel(name)
			application := NewApplication(name, id, provider, AppCompose)
			if len(tt.containers) > 0 {
				application.containers = tt.containers
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
