package applications

import (
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestAggregateAppStatuses(t *testing.T) {
	type testCase struct {
		name                string
		results             []AppStatusResult
		expectedSummary     v1beta1.ApplicationsSummaryStatusType
		expectedNumStatuses int
	}

	testCases := []testCase{
		{
			name:                "no applications",
			results:             []AppStatusResult{},
			expectedSummary:     v1beta1.ApplicationsSummaryStatusNoApplications,
			expectedNumStatuses: 0,
		},
		{
			name: "all healthy",
			results: []AppStatusResult{
				{Status: v1beta1.DeviceApplicationStatus{Name: "app1"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusHealthy}},
				{Status: v1beta1.DeviceApplicationStatus{Name: "app2"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusHealthy}},
			},
			expectedSummary:     v1beta1.ApplicationsSummaryStatusHealthy,
			expectedNumStatuses: 2,
		},
		{
			name: "one degraded",
			results: []AppStatusResult{
				{Status: v1beta1.DeviceApplicationStatus{Name: "app1"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusHealthy}},
				{Status: v1beta1.DeviceApplicationStatus{Name: "app2"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusDegraded}},
			},
			expectedSummary:     v1beta1.ApplicationsSummaryStatusDegraded,
			expectedNumStatuses: 2,
		},
		{
			name: "one errored",
			results: []AppStatusResult{
				{Status: v1beta1.DeviceApplicationStatus{Name: "app1"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusHealthy}},
				{Status: v1beta1.DeviceApplicationStatus{Name: "app2"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusError}},
			},
			expectedSummary:     v1beta1.ApplicationsSummaryStatusError,
			expectedNumStatuses: 2,
		},
		{
			name: "degraded and errored",
			results: []AppStatusResult{
				{Status: v1beta1.DeviceApplicationStatus{Name: "app1"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusDegraded}},
				{Status: v1beta1.DeviceApplicationStatus{Name: "app2"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusError}},
			},
			expectedSummary:     v1beta1.ApplicationsSummaryStatusError,
			expectedNumStatuses: 2,
		},
		{
			name: "all stopped",
			results: []AppStatusResult{
				{Status: v1beta1.DeviceApplicationStatus{Name: "app1", Status: v1beta1.ApplicationStatusStopped}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusStopped}},
				{Status: v1beta1.DeviceApplicationStatus{Name: "app2", Status: v1beta1.ApplicationStatusStopped}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusStopped}},
			},
			expectedSummary:     v1beta1.ApplicationsSummaryStatusStopped,
			expectedNumStatuses: 2,
		},
		{
			name: "some stopped",
			results: []AppStatusResult{
				{Status: v1beta1.DeviceApplicationStatus{Name: "app1"}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusHealthy}},
				{Status: v1beta1.DeviceApplicationStatus{Name: "app2", Status: v1beta1.ApplicationStatusStopped}, Summary: v1beta1.DeviceApplicationsSummaryStatus{Status: v1beta1.ApplicationsSummaryStatusStopped}},
			},
			expectedSummary:     v1beta1.ApplicationsSummaryStatusDegraded,
			expectedNumStatuses: 2,
		},
	}

	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := require.New(t)

			statuses, summary := aggregateAppStatuses(tc.results)
			assert.Equal(tc.expectedSummary, summary.Status)
			assert.Len(statuses, tc.expectedNumStatuses)
		})
	}
}

func TestBuildAppSummaryInfo(t *testing.T) {
	testCases := []struct {
		name           string
		erroredApps    []string
		degradedApps   []string
		maxLength      int
		expectedOutput *string
	}{
		{
			name:           "No apps",
			erroredApps:    []string{},
			degradedApps:   []string{},
			maxLength:      100,
			expectedOutput: nil,
		},
		{
			name:           "Only errored apps, within limit",
			erroredApps:    []string{"app1 is in status Error", "app2 is in status Error"},
			degradedApps:   []string{},
			maxLength:      100,
			expectedOutput: lo.ToPtr("app1 is in status Error, app2 is in status Error"),
		},
		{
			name:           "Only degraded apps, within limit",
			erroredApps:    []string{},
			degradedApps:   []string{"app3 is in status Degraded", "app4 is in status Degraded"},
			maxLength:      100,
			expectedOutput: lo.ToPtr("app3 is in status Degraded, app4 is in status Degraded"),
		},
		{
			name:           "Both errored and degraded apps, within limit",
			erroredApps:    []string{"app1 is in status Error"},
			degradedApps:   []string{"app3 is in status Degraded"},
			maxLength:      100,
			expectedOutput: lo.ToPtr("app1 is in status Error, app3 is in status Degraded"),
		},
		{
			name:           "Message exceeds maxLength, truncation required",
			erroredApps:    []string{"a", "b", "c", "d", "e"},
			degradedApps:   []string{"f", "g", "h", "i", "j"},
			maxLength:      30,
			expectedOutput: lo.ToPtr("a, b, c, d, e, f, g, h, i, j..."),
		},
		{
			name:           "Errored apps message exceeds maxLength, degraded apps not included",
			erroredApps:    []string{"this is a very long error message for app1", "and another one for app2"},
			degradedApps:   []string{"degraded app message"},
			maxLength:      50,
			expectedOutput: lo.ToPtr("this is a very long error message for app1, and..."),
		},
		{
			name:           "Degraded apps message exceeds maxLength",
			erroredApps:    []string{},
			degradedApps:   []string{"this is a very long degraded message for app3", "and another one for app4"},
			maxLength:      50,
			expectedOutput: lo.ToPtr("this is a very long degraded message for app3, a..."),
		},
		{
			name:           "Exact maxLength fit",
			erroredApps:    []string{"short error"},
			degradedApps:   []string{"degraded"},
			maxLength:      25,
			expectedOutput: lo.ToPtr("short error, degraded"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			output := buildAppSummaryInfo(tc.erroredApps, tc.degradedApps, tc.maxLength)
			if tc.expectedOutput == nil {
				require.Nil(t, output)
				return
			}
			require.NotNil(t, output)
			require.Equal(t, *tc.expectedOutput, *output)
			require.LessOrEqual(t, len(*output), tc.maxLength, fmt.Sprintf("Output '%s' is longer than maxLength %d", *output, tc.maxLength))
		})
	}
}
