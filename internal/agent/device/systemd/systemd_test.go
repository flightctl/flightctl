package systemd

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestStatus(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		matchPatterns []string
		units         []SystemDUnitListEntry
		expected      []v1alpha1.DeviceApplicationStatus
	}{
		{
			name: "running unit",
			units: []SystemDUnitListEntry{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", Sub: "running", Description: "A test service"},
			},
			expected: []v1alpha1.DeviceApplicationStatus{
				{Name: "test.service", Status: v1alpha1.ApplicationStatusRunning, Ready: "1/1"},
			},
			matchPatterns: []string{"test.service"},
		},
		{
			name: "failed unit",
			units: []SystemDUnitListEntry{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "failed", Sub: "failed", Description: "A failed service"},
			},
			expected: []v1alpha1.DeviceApplicationStatus{
				{Name: "test.service", Status: v1alpha1.ApplicationStatusError, Ready: "0/1"},
			},
			matchPatterns: []string{"test.service"},
		},
		{
			name: "completed one-shot unit",
			units: []SystemDUnitListEntry{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", Sub: "exited", Description: "A one-shot service"},
			},
			expected: []v1alpha1.DeviceApplicationStatus{
				{Name: "test.service", Status: v1alpha1.ApplicationStatusCompleted, Ready: "0/1"},
			},
			matchPatterns: []string{"test.service"},
		},
		{
			name: "starting unit",
			units: []SystemDUnitListEntry{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "activating", Sub: "start-pre", Description: "A starting service"},
			},
			expected: []v1alpha1.DeviceApplicationStatus{
				{Name: "test.service", Status: v1alpha1.ApplicationStatusStarting, Ready: "0/1"},
			},
			matchPatterns: []string{"test.service"},
		},
		{
			name: "unknown unit",
			units: []SystemDUnitListEntry{
				{Unit: "test.service", LoadState: "", ActiveState: "", Sub: "", Description: "An unknown service"},
			},
			expected: []v1alpha1.DeviceApplicationStatus{
				{Name: "test.service", Status: v1alpha1.ApplicationStatusUnknown, Ready: "0/1"},
			},
			matchPatterns: []string{"test.service"},
		},
		{
			name:          "no units",
			expected:      []v1alpha1.DeviceApplicationStatus{},
			matchPatterns: []string{"test.service"},
		},
		{
			name:     "no match patterns",
			expected: []v1alpha1.DeviceApplicationStatus{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			execMock := executer.NewMockExecuter(ctrl)

			log := log.NewPrefixLogger("test")
			client := client.NewSystemd(execMock)
			m := &manager{
				log:      log,
				client:   client,
				patterns: tt.matchPatterns,
			}

			if len(tt.matchPatterns) > 0 {
				unitBytes, err := json.Marshal(tt.units)
				require.NoError(err)
				args := append([]string{"list-units", "--all", "--output", "json"}, tt.matchPatterns...)
				execMock.EXPECT().ExecuteWithContext(gomock.Any(), gomock.Any(), args).Return(string(unitBytes), "", 0)
			}
			status := v1alpha1.NewDeviceStatus()
			err := m.Status(context.Background(), &status)
			require.NoError(err)
			require.Equal(tt.expected, status.Applications)
		})
	}
}
