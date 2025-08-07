package resource

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestCPUMonitor(t *testing.T) {
	tests := []struct {
		name         string
		prev         *CPUUsage
		snapshots    []*CPUUsage
		alertRules   []v1alpha1.ResourceAlertRule
		expectFiring []v1alpha1.ResourceAlertSeverityType
		expectUsages []int64
	}{
		{
			name: "below threshold",
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1005, System: 1005, Idle: 8090}, // prev -> 10%
				{User: 1010, System: 1010, Idle: 8180}, // snapshot[0] -> 10%
			},
			alertRules: []v1alpha1.ResourceAlertRule{
				{Severity: v1alpha1.ResourceAlertSeverityTypeWarning, Percentage: 11, Duration: "1ms"},
			},
			expectFiring: nil,
			expectUsages: []int64{10, 10},
		},
		{
			name: "above warning",
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1004, System: 1004, Idle: 8086}, // prev -> 9%
				{User: 1008, System: 1008, Idle: 8172}, // snapshot[0] -> 9%
			},
			alertRules: []v1alpha1.ResourceAlertRule{
				{Severity: v1alpha1.ResourceAlertSeverityTypeWarning, Percentage: 5, Duration: "1ms"},
			},
			expectFiring: []v1alpha1.ResourceAlertSeverityType{v1alpha1.ResourceAlertSeverityTypeWarning},
			expectUsages: []int64{9, 9},
		},
		{
			name: "alert fires then clears",
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1010, System: 1010, Idle: 8080}, // prev -> 20%
				{User: 1012, System: 1013, Idle: 8175}, // snapshot[0] -> 5% clear alert
			},
			alertRules: []v1alpha1.ResourceAlertRule{
				{Severity: v1alpha1.ResourceAlertSeverityTypeWarning, Percentage: 11, Duration: "1ms"},
			},
			expectFiring: nil,
			expectUsages: []int64{20, 5},
		},
		{
			name: "zero delta",
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1000, System: 1000, Idle: 8000}, // prev -> 0%
				{User: 1000, System: 1000, Idle: 8000}, // snapshot[0] -> 0%
			},
			alertRules: []v1alpha1.ResourceAlertRule{
				{Severity: v1alpha1.ResourceAlertSeverityTypeCritical, Percentage: 1, Duration: "1ms"},
			},
			expectFiring: nil,
			expectUsages: []int64{0, 0},
		},
		{
			name: "warning and critical alerts fire",
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1030, System: 1030, Idle: 8040}, // prev -> 60%
				{User: 1060, System: 1060, Idle: 8080}, // snapshot[0] -> 60%
			},
			alertRules: []v1alpha1.ResourceAlertRule{
				{Severity: v1alpha1.ResourceAlertSeverityTypeWarning, Percentage: 20, Duration: "1ms"},
				{Severity: v1alpha1.ResourceAlertSeverityTypeCritical, Percentage: 50, Duration: "1ms"},
			},
			expectUsages: []int64{60, 60},
			expectFiring: []v1alpha1.ResourceAlertSeverityType{
				v1alpha1.ResourceAlertSeverityTypeWarning,
				v1alpha1.ResourceAlertSeverityTypeCritical,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.TraceLevel)
			collector := &fakeCPUCollector{snapshots: tt.snapshots}

			cpuMonitor := &CPUMonitor{
				log:       log,
				alerts:    make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
				collector: collector,
				prevUsage: tt.prev,
			}
			var maxDuration time.Duration
			for _, rule := range tt.alertRules {
				alert, err := NewAlert(rule)
				require.NoError(err)
				cpuMonitor.alerts[rule.Severity] = alert

				if d, err := time.ParseDuration(rule.Duration); err == nil && d > maxDuration {
					maxDuration = d
				}
			}

			for i, expected := range tt.expectUsages {
				usage := &CPUUsage{}
				cpuMonitor.sync(ctx, usage)
				time.Sleep(maxDuration)
				require.Equalf(expected, usage.UsedPercent, "sync %d: expected %d%% usage", i+1, expected)
			}

			var gotSeverities []v1alpha1.ResourceAlertSeverityType
			for _, alert := range cpuMonitor.Alerts() {
				gotSeverities = append(gotSeverities, alert.Severity)
			}

			for _, severity := range tt.expectFiring {
				require.Contains(gotSeverities, severity)
			}

			require.ElementsMatch(tt.expectFiring, gotSeverities)
		})
	}
}

type fakeCPUCollector struct {
	snapshots []*CPUUsage
	index     int
}

func (f *fakeCPUCollector) CollectUsage(ctx context.Context, usage *CPUUsage) error {
	if f.index >= len(f.snapshots) {
		return nil
	}
	*usage = *f.snapshots[f.index]

	f.index++
	return nil
}
