package resource

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

const (
	retryInterval = 100 * time.Millisecond
	retryTimeout  = 5 * time.Second
)

func TestUpdateMonitor(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name                  string
		monitor               v1alpha1.ResourceMonitor
		alerts                map[v1alpha1.ResourceAlertSeverityType]*Alert
		currentSampleInterval time.Duration
		expectedUpdated       bool
		alertRuleCount        int
	}{
		{
			name:                  "update monitor new alerts",
			monitor:               newMockCPUResourceMonitor(require, 1*time.Second),
			currentSampleInterval: 1 * time.Second,
			expectedUpdated:       true,
			alertRuleCount:        3,
		},
		{
			name:                  "update monitor no change",
			monitor:               newEmptyMonitor(require, 1*time.Second),
			currentSampleInterval: 1 * time.Second,
			expectedUpdated:       false,
			alertRuleCount:        0,
		},
		{
			name:                  "update interval",
			monitor:               newEmptyMonitor(require, 2*time.Second),
			currentSampleInterval: 1 * time.Second,
			expectedUpdated:       true,
			alertRuleCount:        0,
		},
		{
			name:                  "update monitor remove alerts",
			monitor:               newEmptyMonitor(require, 1*time.Second),
			currentSampleInterval: 1 * time.Second,
			alerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{
				v1alpha1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Severity: v1alpha1.ResourceAlertSeverityTypeCritical,
					},
				},
			},
			expectedUpdated: true,
			alertRuleCount:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			updateIntervalCh := make(chan time.Duration, 1)

			var alerts map[v1alpha1.ResourceAlertSeverityType]*Alert
			if tt.alerts == nil {
				alerts = make(map[v1alpha1.ResourceAlertSeverityType]*Alert)
			} else {
				alerts = tt.alerts
			}
			updated, err := updateMonitor(log, &tt.monitor, &tt.currentSampleInterval, alerts, updateIntervalCh)
			require.NoError(err)
			require.Equal(tt.expectedUpdated, updated)
			require.Equal(tt.alertRuleCount, len(alerts))
		})

	}
}

func TestIsAlertFiring(t *testing.T) {
	tests := []struct {
		name            string
		alert           *Alert
		usagePercentage int64
		firingSince     time.Time
		expectedFiring  bool
	}{
		{
			name: "usage percentage is below alert percentage",
			alert: &Alert{
				ResourceAlertRule: v1alpha1.ResourceAlertRule{
					Percentage: 2,
					Duration:   "1s",
				},
				duration: 1 * time.Second,
			},
			usagePercentage: 1,
			expectedFiring:  false,
		},
		{
			name: "alert firing over duration",
			alert: &Alert{
				ResourceAlertRule: v1alpha1.ResourceAlertRule{
					Percentage: 1,
					Duration:   "1h",
				},
				firingSince: time.Now().Add(-61 * time.Minute), // inject duration + 1 minute
				duration:    1 * time.Hour,
			},
			usagePercentage: 2,
			expectedFiring:  true,
		},
		{
			name: "alert observed but below duration threshold",
			alert: &Alert{
				ResourceAlertRule: v1alpha1.ResourceAlertRule{
					Percentage: 80,
					Duration:   "30m",
				},
				firingSince: time.Now().Add(-29 * time.Minute), // inject duration - 1 minute
				duration:    30 * time.Minute,
			},
			usagePercentage: 90,
			expectedFiring:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.alert.Sync(tt.usagePercentage)
			require.Equal(t, tt.expectedFiring, tt.alert.IsFiring())
		})
	}

}

func TestUpdateAlerts(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name            string
		existingAlerts  map[v1alpha1.ResourceAlertSeverityType]*Alert
		expectedAlerts  map[v1alpha1.ResourceAlertSeverityType]*Alert
		expectedUpdated bool
		newRules        []v1alpha1.ResourceAlertRule
	}{
		{
			name: "new rules empty clear existing alerts",
			existingAlerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{
				v1alpha1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Percentage: 80,
						Duration:   "1s",
					},
					duration: 1 * time.Second,
				},
			},
			expectedAlerts:  map[v1alpha1.ResourceAlertSeverityType]*Alert{},
			newRules:        []v1alpha1.ResourceAlertRule{},
			expectedUpdated: true,
		},
		{
			name: "new rules remove an existing alerts",
			existingAlerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{
				v1alpha1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Percentage: 80,
						Duration:   "1s",
					},
				},
				v1alpha1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Percentage: 40,
						Duration:   "1h",
					},
				},
			},
			expectedAlerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{
				v1alpha1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Severity:   v1alpha1.ResourceAlertSeverityTypeWarning,
						Percentage: 40,
						Duration:   "1h",
					},
					duration: 1 * time.Hour,
				},
			},
			newRules: []v1alpha1.ResourceAlertRule{
				{
					Severity:   v1alpha1.ResourceAlertSeverityTypeWarning,
					Percentage: 40,
					Duration:   "1h",
				},
			},
			expectedUpdated: true,
		},
		{
			name:           "new rules add an alert",
			existingAlerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{},
			expectedAlerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{
				v1alpha1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Severity:   v1alpha1.ResourceAlertSeverityTypeWarning,
						Percentage: 40,
						Duration:   "1h",
					},
					duration: 1 * time.Hour,
				},
			},
			newRules: []v1alpha1.ResourceAlertRule{
				{
					Severity:   v1alpha1.ResourceAlertSeverityTypeWarning,
					Percentage: 40,
					Duration:   "1h",
				},
			},
			expectedUpdated: true,
		},
		{
			name: "new rules no change",
			existingAlerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{
				v1alpha1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Severity:   v1alpha1.ResourceAlertSeverityTypeWarning,
						Percentage: 40,
						Duration:   "1h",
					},
				},
			},
			expectedAlerts: map[v1alpha1.ResourceAlertSeverityType]*Alert{
				v1alpha1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1alpha1.ResourceAlertRule{
						Severity:   v1alpha1.ResourceAlertSeverityTypeWarning,
						Percentage: 40,
						Duration:   "1h",
					},
				},
			},
			newRules: []v1alpha1.ResourceAlertRule{
				{
					Severity:   v1alpha1.ResourceAlertSeverityTypeWarning,
					Percentage: 40,
					Duration:   "1h",
				},
			},
			expectedUpdated: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, err := updateAlerts(tt.newRules, tt.existingAlerts)
			require.NoError(err)
			if tt.expectedUpdated {
				require.True(updated)
			} else {
				require.False(updated)
			}
			require.Equal(tt.expectedAlerts, tt.existingAlerts)
		})
	}
}

func newEmptyMonitor(require *require.Assertions, samplingInterval time.Duration) v1alpha1.ResourceMonitor {
	monitorSpec := v1alpha1.CpuResourceMonitorSpec{
		SamplingInterval: samplingInterval.String(),
		MonitorType:      CPUMonitorType,
		AlertRules:       []v1alpha1.ResourceAlertRule{},
	}
	rm := v1alpha1.ResourceMonitor{}
	err := rm.FromCpuResourceMonitorSpec(monitorSpec)
	require.NoError(err)
	return rm
}

func newMockCPUResourceMonitor(require *require.Assertions, samplingInterval time.Duration) v1alpha1.ResourceMonitor {
	monitorSpec := v1alpha1.CpuResourceMonitorSpec{
		SamplingInterval: samplingInterval.String(),
		MonitorType:      CPUMonitorType,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  80,
				Duration:    "20m",
				Description: "Critical: CPU usage is above 80% for 20m",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  70,
				Duration:    "10m",
				Description: "Warning: CPU usage is above 70% for 10m",
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeInfo,
				Percentage:  50,
				Duration:    "1h",
				Description: "Warning: CPU usage is above 50% for 1h",
			},
		},
	}
	rm := v1alpha1.ResourceMonitor{}
	err := rm.FromCpuResourceMonitorSpec(monitorSpec)
	require.NoError(err)
	return rm
}
