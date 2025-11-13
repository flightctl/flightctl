package shutdown

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownCollector_BasicMetrics(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

	collector := NewShutdownCollector(ctx, log)

	// Test MetricsName
	assert.Equal(t, "shutdown", collector.MetricsName())

	// Verify initial state
	assert.False(t, collector.IsShutdownInProgress())

	// Test registration with Prometheus
	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	require.NoError(t, err)

	// Gather initial metrics
	metrics, err := registry.Gather()
	require.NoError(t, err)
	assert.Greater(t, len(metrics), 0)
}

func TestShutdownCollector_ShutdownFlow(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	collector := NewShutdownCollector(ctx, log)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	serviceName := "test-service"

	// Test shutdown sequence
	collector.RecordShutdownInitiated(serviceName)
	assert.True(t, collector.IsShutdownInProgress())

	collector.RecordShutdownInProgress(serviceName)

	// Record component shutdown
	component := "http-server"
	priority := 0
	collector.RecordComponentShutdownStart(component, priority)
	time.Sleep(10 * time.Millisecond) // Simulate work
	collector.RecordComponentShutdownEnd(component, priority, "success", 10*time.Millisecond)

	// Complete shutdown
	totalDuration := 50 * time.Millisecond
	collector.RecordShutdownCompleted(serviceName, "success", totalDuration)
	assert.False(t, collector.IsShutdownInProgress())

	// Verify metrics were recorded
	metricNames := []string{
		"flightctl_shutdown_initiated_total",
		"flightctl_shutdown_state",
		"flightctl_shutdown_completed_total",
		"flightctl_shutdown_component_status_total",
		"flightctl_shutdown_component_duration_seconds",
	}

	for _, name := range metricNames {
		assert.Greater(t, testutil.CollectAndCount(collector, name), 0,
			"Expected metric %s to have values", name)
	}

	// Verify specific metric values
	expected := `
		# HELP flightctl_shutdown_initiated_total Total number of shutdown sequences initiated
		# TYPE flightctl_shutdown_initiated_total counter
		flightctl_shutdown_initiated_total 1
	`
	err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "flightctl_shutdown_initiated_total")
	assert.NoError(t, err)
}

func TestShutdownCollector_ErrorHandling(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	collector := NewShutdownCollector(ctx, log)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	serviceName := "test-service"
	component := "database"

	// Test error scenarios
	collector.RecordShutdownInitiated(serviceName)
	collector.RecordComponentShutdownStart(component, 4)

	// Record component error
	collector.RecordShutdownError(component, "connection_timeout")
	collector.RecordComponentShutdownEnd(component, 4, "error", 5*time.Second)

	// Record fail-fast event
	collector.RecordFailFastEvent(component, "critical_error")

	// Complete with error
	collector.RecordShutdownCompleted(serviceName, "error", 10*time.Second)

	// Verify error metrics
	errorMetric := testutil.ToFloat64(collector.shutdownErrorsCounter.WithLabelValues(component, "connection_timeout"))
	assert.Equal(t, float64(1), errorMetric)

	failFastMetric := testutil.ToFloat64(collector.failFastEventsCounter.WithLabelValues(component, "critical_error"))
	assert.Equal(t, float64(1), failFastMetric)
}

func TestShutdownCollector_SignalHandling(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	collector := NewShutdownCollector(ctx, log)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	// Test signal recording
	collector.RecordSignalReceived("SIGTERM")
	collector.RecordSignalReceived("SIGINT")
	collector.RecordSignalTimeout()

	// Verify signal metrics
	sigtermMetric := testutil.ToFloat64(collector.signalReceivedCounter.WithLabelValues("SIGTERM"))
	assert.Equal(t, float64(1), sigtermMetric)

	sigintMetric := testutil.ToFloat64(collector.signalReceivedCounter.WithLabelValues("SIGINT"))
	assert.Equal(t, float64(1), sigintMetric)

	timeoutMetric := testutil.ToFloat64(collector.signalTimeoutCounter)
	assert.Equal(t, float64(1), timeoutMetric)
}

func TestShutdownCollector_ResourceCleanup(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	collector := NewShutdownCollector(ctx, log)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	// Test resource cleanup recording
	collector.RecordResourceCleanup("database", "success")
	collector.RecordResourceCleanup("kvstore", "success")
	collector.RecordResourceCleanup("queue", "failed")

	// Verify resource cleanup metrics
	dbMetric := testutil.ToFloat64(collector.resourceCleanupCounter.WithLabelValues("database", "success"))
	assert.Equal(t, float64(1), dbMetric)

	kvMetric := testutil.ToFloat64(collector.resourceCleanupCounter.WithLabelValues("kvstore", "success"))
	assert.Equal(t, float64(1), kvMetric)

	queueMetric := testutil.ToFloat64(collector.resourceCleanupCounter.WithLabelValues("queue", "failed"))
	assert.Equal(t, float64(1), queueMetric)
}

func TestShutdownCollector_ComponentTimeouts(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	collector := NewShutdownCollector(ctx, log)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	component := "slow-component"
	priority := 2

	// Record component that times out
	collector.RecordComponentShutdownStart(component, priority)
	collector.RecordComponentShutdownEnd(component, priority, "timeout", 30*time.Second)

	// Verify timeout counter is incremented
	timeoutMetric := testutil.ToFloat64(collector.componentTimeoutCounter)
	assert.Equal(t, float64(1), timeoutMetric)

	// Verify component status shows timeout
	statusMetric := testutil.ToFloat64(collector.componentShutdownStatus.WithLabelValues(component, "2", "timeout"))
	assert.Equal(t, float64(1), statusMetric)
}

func TestShutdownCollector_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	collector := NewShutdownCollector(ctx, log)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	// Test concurrent access to ensure thread safety
	serviceName := "test-service"
	numGoroutines := 10

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			component := fmt.Sprintf("component-%d", id)
			collector.RecordComponentShutdownStart(component, id%5)
			time.Sleep(time.Millisecond) // Simulate work
			collector.RecordComponentShutdownEnd(component, id%5, "success", time.Millisecond)

			if id == 0 {
				collector.RecordShutdownInitiated(serviceName)
				collector.RecordShutdownCompleted(serviceName, "success", 10*time.Millisecond)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Test timed out waiting for goroutines")
		}
	}

	// Verify metrics were recorded without race conditions
	initiatedMetric := testutil.ToFloat64(collector.shutdownInitiatedCounter)
	assert.Equal(t, float64(1), initiatedMetric)

	// Should have recorded multiple components
	metrics, err := registry.Gather()
	require.NoError(t, err)
	assert.Greater(t, len(metrics), 0)
}
