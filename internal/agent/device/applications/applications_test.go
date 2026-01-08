package applications

import (
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/stretchr/testify/require"
)

func TestApplicationStatus(t *testing.T) {
	testCases := []struct {
		name           string
		workloads      []Workload
		expectedStatus v1beta1.ApplicationStatusType
		expectedSummary v1beta1.ApplicationsSummaryStatusType
	}{
		{
			name: "single container completed",
			workloads: []Workload{
				{Name: "c1", Status: StatusExited},
			},
			expectedStatus: v1beta1.ApplicationStatusCompleted,
			expectedSummary: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "multi container all exited is error",
			workloads: []Workload{
				{Name: "c1", Status: StatusExited},
				{Name: "c2", Status: StatusExited},
			},
			expectedStatus: v1beta1.ApplicationStatusError,
			expectedSummary: v1beta1.ApplicationsSummaryStatusError,
		},
		{
			name: "multi container running healthy",
			workloads: []Workload{
				{Name: "c1", Status: StatusRunning},
				{Name: "c2", Status: StatusRunning},
			},
			expectedStatus: v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "multi container one running one exited is healthy",
			workloads: []Workload{
				{Name: "c1", Status: StatusRunning},
				{Name: "c2", Status: StatusExited},
			},
			expectedStatus: v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "multi container starting",
			workloads: []Workload{
				{Name: "c1", Status: StatusRunning},
				{Name: "c2", Status: StatusInit},
			},
			expectedStatus: v1beta1.ApplicationStatusStarting,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "multi container preparing",
			workloads: []Workload{
				{Name: "c1", Status: StatusInit},
				{Name: "c2", Status: StatusCreated},
			},
			expectedStatus: v1beta1.ApplicationStatusPreparing,
			expectedSummary: v1beta1.ApplicationsSummaryStatusUnknown,
		},
		{
			name: "multi container running degraded",
			workloads: []Workload{
				{Name: "c1", Status: StatusRunning},
				{Name: "c2", Status: StatusDied}, // Assuming Died is not a clean exit
			},
			expectedStatus: v1beta1.ApplicationStatusRunning,
			expectedSummary: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "no workloads is unknown",
			workloads: []Workload{},
			expectedStatus: v1beta1.ApplicationStatusUnknown,
			expectedSummary: v1beta1.ApplicationsSummaryStatusUnknown,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			app := NewApplication(&provider.FakeProvider{
				ProviderSpec: &provider.Spec{
					Name: "test-app",
				},
			})
			app.workloads = tc.workloads

			status, summary, err := app.Status()
			require.NoError(err)
			require.Equal(tc.expectedStatus, status.Status)
			require.Equal(tc.expectedSummary, summary.Status)
		})
	}
}