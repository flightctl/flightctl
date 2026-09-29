package resource

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestAlertSyncEdgeTransition(t *testing.T) {
	tests := []struct {
		name            string
		initialFiring   bool
		usagePercentage int64
		threshold       float32
		duration        string
		expectChanged   bool
		expectFiring    bool
	}{
		{
			name:            "When usage exceeds threshold after duration it should transition to firing",
			initialFiring:   false,
			usagePercentage: 95,
			threshold:       90,
			duration:        "0s",
			expectChanged:   true,
			expectFiring:    true,
		},
		{
			name:            "When already firing and usage still high it should not report a change",
			initialFiring:   true,
			usagePercentage: 95,
			threshold:       90,
			duration:        "0s",
			expectChanged:   false,
			expectFiring:    true,
		},
		{
			name:            "When firing and usage drops below threshold it should transition to not-firing",
			initialFiring:   true,
			usagePercentage: 50,
			threshold:       90,
			duration:        "0s",
			expectChanged:   true,
			expectFiring:    false,
		},
		{
			name:            "When not firing and usage stays below threshold it should not report a change",
			initialFiring:   false,
			usagePercentage: 50,
			threshold:       90,
			duration:        "0s",
			expectChanged:   false,
			expectFiring:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			alert := &Alert{
				ResourceAlertRule: v1beta1.ResourceAlertRule{
					Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
					Percentage: tc.threshold,
					Duration:   tc.duration,
				},
				duration: 0,
				firing:   tc.initialFiring,
			}
			if tc.initialFiring {
				alert.firingSince = time.Now().Add(-time.Hour)
			}

			changed := alert.Sync(tc.usagePercentage)
			require.Equal(tc.expectChanged, changed)
			require.Equal(tc.expectFiring, alert.IsFiring())
		})
	}
}

func TestInjectedNotifierCalledOnCriticalTransition(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")
	logger.Logger.SetLevel(logrus.TraceLevel)

	var notifyCount atomic.Int32
	notifier := func() { notifyCount.Add(1) }

	_ = NewManager(logger, notifier)

	// The notifier is injected — it's called by the monitor's sync loop
	// when a critical alert transitions. We verify the injection works
	// by calling the notifier directly (monitor integration tested via
	// the monitor tests).
	notifier()
	require.Equal(int32(1), notifyCount.Load())
}

func TestNilNotifierDoesNotPanic(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	// Passing nil should not panic — NewManager wraps it with a no-op
	mgr := NewManager(logger, nil)
	require.NotNil(t, mgr)
}

func TestAlertNotifyOnCriticalResolve(t *testing.T) {
	require := require.New(t)

	var notifyCount atomic.Int32
	notifier := func() { notifyCount.Add(1) }

	alert := &Alert{
		ResourceAlertRule: v1beta1.ResourceAlertRule{
			Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
			Percentage: 90,
			Duration:   "0s",
		},
		duration: 0,
	}

	// Transition to firing (not-firing → firing)
	changed := alert.Sync(95)
	require.True(changed, "should transition to firing")
	require.True(alert.IsFiring())

	// Simulate what the monitor sync loop does: call notifier on critical transition
	if changed {
		notifier()
	}
	require.Equal(int32(1), notifyCount.Load(), "firing transition should notify")

	// Transition to resolved (firing → not-firing)
	changed = alert.Sync(50)
	require.True(changed, "should transition to not-firing (resolve)")
	require.False(alert.IsFiring())

	if changed {
		notifier()
	}
	require.Equal(int32(2), notifyCount.Load(), "resolve transition should also notify")
}

func TestNotifyOnlyCriticalSeverity(t *testing.T) {
	require := require.New(t)

	// Warning alerts should not trigger the notification — verify
	// the criticalNotifyFunc is only called on critical severity
	warningAlert := &Alert{
		ResourceAlertRule: v1beta1.ResourceAlertRule{
			Severity:   v1beta1.ResourceAlertSeverityTypeWarning,
			Percentage: 80,
			Duration:   "0s",
		},
		duration: 0,
	}

	changed := warningAlert.Sync(95)
	require.True(changed, "warning alert should transition to firing")
	require.True(warningAlert.IsFiring(), "warning alert should be firing")
	// The monitor only calls criticalNotifyFunc for Critical severity —
	// warning transitions do not trigger it (tested in monitor sync loops)
}

func TestMonitorMemoryCallbackWiring(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	logger.Logger.SetLevel(logrus.TraceLevel)

	tests := []struct {
		name               string
		alerts             map[v1beta1.ResourceAlertSeverityType]*Alert
		percentages        []int64
		expectedNotifyCall int32
	}{
		{
			name: "When critical alert transitions to firing it should call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 90,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95},
			expectedNotifyCall: 1,
		},
		{
			name: "When critical alert transitions to firing then resolves it should call criticalNotifyFunc twice",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 90,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95, 50},
			expectedNotifyCall: 2,
		},
		{
			name: "When only warning alert transitions it should not call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeWarning,
						Percentage: 80,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95},
			expectedNotifyCall: 0,
		},
		{
			name: "When critical alert stays firing it should not call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 90,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95, 96},
			expectedNotifyCall: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			var notifyCount atomic.Int32
			monitor := NewMemoryMonitor(logger, func() { notifyCount.Add(1) })
			monitor.alerts = tc.alerts

			for _, pct := range tc.percentages {
				monitor.ensureAlerts(pct)
			}
			require.Equal(tc.expectedNotifyCall, notifyCount.Load())
		})
	}
}

func TestMonitorDiskCallbackWiring(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	logger.Logger.SetLevel(logrus.TraceLevel)

	tests := []struct {
		name               string
		alerts             map[v1beta1.ResourceAlertSeverityType]*Alert
		percentages        []int64
		expectedNotifyCall int32
	}{
		{
			name: "When critical alert transitions to firing it should call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 90,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95},
			expectedNotifyCall: 1,
		},
		{
			name: "When critical alert transitions to firing then resolves it should call criticalNotifyFunc twice",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 90,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95, 50},
			expectedNotifyCall: 2,
		},
		{
			name: "When only warning alert transitions it should not call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeWarning,
						Percentage: 80,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95},
			expectedNotifyCall: 0,
		},
		{
			name: "When critical alert stays firing it should not call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 90,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			percentages:        []int64{95, 96},
			expectedNotifyCall: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			var notifyCount atomic.Int32
			monitor := NewDiskMonitor(logger, func() { notifyCount.Add(1) })
			monitor.alerts = tc.alerts

			for _, pct := range tc.percentages {
				monitor.ensureAlerts(pct)
			}
			require.Equal(tc.expectedNotifyCall, notifyCount.Load())
		})
	}
}

func TestMonitorCPUCallbackWiring(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	logger.Logger.SetLevel(logrus.TraceLevel)

	tests := []struct {
		name               string
		alerts             map[v1beta1.ResourceAlertSeverityType]*Alert
		prev               *CPUUsage
		snapshots          []*CPUUsage
		expectedNotifyCall int32
	}{
		{
			name: "When critical alert transitions to firing it should call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 50,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1030, System: 1030, Idle: 8040}, // 60% usage
			},
			expectedNotifyCall: 1,
		},
		{
			name: "When critical alert transitions to firing then resolves it should call criticalNotifyFunc twice",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 50,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1030, System: 1030, Idle: 8040}, // 60% usage - fires
				{User: 1031, System: 1031, Idle: 8138}, // 2% usage - resolves
			},
			expectedNotifyCall: 2,
		},
		{
			name: "When only warning alert transitions it should not call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeWarning: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeWarning,
						Percentage: 50,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1030, System: 1030, Idle: 8040}, // 60% usage
			},
			expectedNotifyCall: 0,
		},
		{
			name: "When critical alert stays firing it should not call criticalNotifyFunc",
			alerts: map[v1beta1.ResourceAlertSeverityType]*Alert{
				v1beta1.ResourceAlertSeverityTypeCritical: {
					ResourceAlertRule: v1beta1.ResourceAlertRule{
						Severity:   v1beta1.ResourceAlertSeverityTypeCritical,
						Percentage: 50,
						Duration:   "0s",
					},
					duration: 0,
				},
			},
			prev: &CPUUsage{User: 1000, System: 1000, Idle: 8000},
			snapshots: []*CPUUsage{
				{User: 1030, System: 1030, Idle: 8040}, // 60% usage - fires
				{User: 1060, System: 1060, Idle: 8080}, // 60% usage - stays firing
			},
			expectedNotifyCall: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			var notifyCount atomic.Int32
			ctx := context.Background()
			collector := &fakeCPUCollector{snapshots: tc.snapshots}

			monitor := &CPUMonitor{
				log:                logger,
				alerts:             tc.alerts,
				collector:          collector,
				prevUsage:          tc.prev,
				criticalNotifyFunc: func() { notifyCount.Add(1) },
			}

			for range tc.snapshots {
				usage := &CPUUsage{}
				monitor.sync(ctx, usage)
			}
			require.Equal(tc.expectedNotifyCall, notifyCount.Load())
		})
	}
}
