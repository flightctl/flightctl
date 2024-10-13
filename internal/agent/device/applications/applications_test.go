package applications

import (
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/stretchr/testify/require"
)

func TestApplicationStatus(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name                  string
		containers            map[string]*Container
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
			name: "app preparing to start with containers",
			containers: map[string]*Container{
				"container1": {
					Status: podmanEventInitName,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1alpha1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              AppCompose,
		},
		{
			name: "app starting",
			containers: map[string]*Container{
				"container1": {
					Status: podmanEventInitName,
				},
				"container2": {
					Status: podmanEventRunningName,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1alpha1.ApplicationStatusStarting,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusUnknown,
			expected:              AppCompose,
		},
		{
			name: "app errored",
			containers: map[string]*Container{
				"container1": {
					Status: podmanEventDieName,
				},
				"container2": {
					Status: podmanEventDieName,
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1alpha1.ApplicationStatusError,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusError,
			expected:              AppCompose,
		},
		{
			name: "app running degraded",
			containers: map[string]*Container{
				"container1": {
					Status: podmanEventDieName,
				},
				"container2": {
					Status: podmanEventRunningName,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusDegraded,
			expected:              AppCompose,
		},
		{
			name: "app running healthy",
			containers: map[string]*Container{
				"container1": {
					Status: podmanEventRunningName,
				},
				"container2": {
					Status: podmanEventRunningName,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app running healthy with restarts",
			containers: map[string]*Container{
				"container1": {
					Status:   podmanEventRunningName,
					Restarts: 1,
				},
				"container2": {
					Status:   podmanEventRunningName,
					Restarts: 2,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1alpha1.ApplicationStatusRunning,
			expectedSummaryStatus: v1alpha1.ApplicationsSummaryStatusHealthy,
			expectedRestarts:      3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := v1alpha1.ImageApplicationProvider{
				Image: "image",
			}
			application := NewApplication("testApp", provider, AppCompose)
			if len(tt.containers) > 0 {
				application.containers = tt.containers
			}
			status, summary := application.Status()

			require.Equal(status.Ready, tt.expectedReady)
			require.Equal(status.Restarts, tt.expectedRestarts)
			require.Equal(status.Status, tt.expectedStatus)
			require.Equal(summary, tt.expectedSummaryStatus)
		})
	}

}
