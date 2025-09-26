package worker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockQueuesProvider implements queues.Provider for testing
type MockQueuesProvider struct {
	healthCheckError error
}

// Compile-time check that MockQueuesProvider satisfies queues.Provider
var _ queues.Provider = (*MockQueuesProvider)(nil)

func (m *MockQueuesProvider) NewQueueConsumer(ctx context.Context, queueName string) (queues.QueueConsumer, error) {
	return nil, nil
}

func (m *MockQueuesProvider) NewQueueProducer(ctx context.Context, queueName string) (queues.QueueProducer, error) {
	return nil, nil
}

func (m *MockQueuesProvider) NewPubSubPublisher(ctx context.Context, channelName string) (queues.PubSubPublisher, error) {
	return nil, nil
}

func (m *MockQueuesProvider) NewPubSubSubscriber(ctx context.Context, channelName string) (queues.PubSubSubscriber, error) {
	return nil, nil
}

func (m *MockQueuesProvider) ProcessTimedOutMessages(ctx context.Context, queueName string, timeout time.Duration, handler func(entryID string, body []byte) error) (int, error) {
	return 0, nil
}

func (m *MockQueuesProvider) RetryFailedMessages(ctx context.Context, queueName string, config queues.RetryConfig, handler func(entryID string, body []byte, retryCount int) error) (int, error) {
	return 0, nil
}

func (m *MockQueuesProvider) Stop() {}

func (m *MockQueuesProvider) Wait() {}

func (m *MockQueuesProvider) CheckHealth(ctx context.Context) error {
	return m.healthCheckError
}

func (m *MockQueuesProvider) GetLatestProcessedTimestamp(ctx context.Context) (time.Time, error) {
	return time.Time{}, nil
}

func (m *MockQueuesProvider) AdvanceCheckpointAndCleanup(ctx context.Context) error {
	return nil
}

func (m *MockQueuesProvider) SetCheckpointTimestamp(ctx context.Context, timestamp time.Time) error {
	return nil
}

// ============================================================================
// Unit Tests - Testing WorkerCollector in isolation
// ============================================================================

func TestWorkerCollector_NewWorkerCollector(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	cfg := &config.Config{}
	mockProvider := &MockQueuesProvider{}

	collector := NewWorkerCollector(ctx, log, cfg, mockProvider)

	assert.NotNil(t, collector)
	assert.Equal(t, "worker", collector.MetricsName())
	assert.Equal(t, log, collector.log)
	assert.Equal(t, cfg, collector.cfg)
	assert.Equal(t, mockProvider, collector.queuesProvider)
}

func TestWorkerCollector_MetricsInterface(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	cfg := &config.Config{}
	mockProvider := &MockQueuesProvider{}

	collector := NewWorkerCollector(ctx, log, cfg, mockProvider)

	// Test message processing metrics
	collector.IncMessagesInProgress()
	collector.DecMessagesInProgress()
	collector.IncMessagesProcessed("success")
	collector.IncMessagesProcessed("retryable_failure")
	collector.IncMessageRetries()
	collector.IncRetryableFailures()
	collector.IncPermanentFailures()
	collector.ObserveProcessingDuration(100 * time.Millisecond)

	// Test task metrics
	collector.IncTasksByType("deviceRender")
	collector.ObserveTaskExecutionDuration("deviceRender", 200*time.Millisecond)

	// Test queue health metrics
	collector.SetQueueDepth("task_queue", 5.0)
	collector.SetConsumersActive(3.0)
	collector.SetRedisConnectionStatus(true)
	collector.UpdateLastSuccessfulTask()

	// Verify metrics were recorded by collecting them
	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	require.NoError(t, err)

	metrics, err := registry.Gather()
	require.NoError(t, err)
	assert.NotEmpty(t, metrics)

	// Find specific metrics and verify they exist
	metricNames := make(map[string]bool)
	for _, mf := range metrics {
		metricNames[*mf.Name] = true
	}

	expectedMetrics := []string{
		"flightctl_worker_messages_in_progress",
		"flightctl_worker_messages_processed_total",
		"flightctl_worker_message_retries_total",
		"flightctl_worker_retryable_failures_total",
		"flightctl_worker_permanent_failures_total",
		"flightctl_worker_message_processing_duration_seconds",
		"flightctl_worker_tasks_by_type_total",
		"flightctl_worker_task_execution_duration_seconds",
		"flightctl_worker_queue_depth",
		"flightctl_worker_consumers_active",
		"flightctl_worker_redis_up",
		"flightctl_worker_last_successful_task_timestamp_seconds",
	}

	for _, expectedMetric := range expectedMetrics {
		assert.True(t, metricNames[expectedMetric], "Missing metric: %s", expectedMetric)
	}
}

func TestWorkerCollector_CounterValues(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	cfg := &config.Config{}
	mockProvider := &MockQueuesProvider{}

	collector := NewWorkerCollector(ctx, log, cfg, mockProvider)

	// Increment counters multiple times
	collector.IncMessagesProcessed("success")
	collector.IncMessagesProcessed("success")
	collector.IncMessagesProcessed("retryable_failure")
	collector.IncTasksByType("deviceRender")
	collector.IncTasksByType("fleetRollout")
	collector.IncTasksByType("deviceRender")

	// Create a registry and gather metrics
	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	require.NoError(t, err)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	// Find and verify counter values
	for _, mf := range metrics {
		switch *mf.Name {
		case "flightctl_worker_messages_processed_total":
			// Should have 2 success and 1 failure
			assert.Len(t, mf.Metric, 2)
			for _, m := range mf.Metric {
				status := getLabelValue(m.Label, "status")
				if status == "success" {
					assert.Equal(t, 2.0, *m.Counter.Value)
				} else if status == "retryable_failure" {
					assert.Equal(t, 1.0, *m.Counter.Value)
				}
			}
		case "flightctl_worker_tasks_by_type_total":
			// Should have 2 deviceRender and 1 fleetRollout
			assert.Len(t, mf.Metric, 2)
			for _, m := range mf.Metric {
				taskType := getLabelValue(m.Label, "task_type")
				if taskType == "deviceRender" {
					assert.Equal(t, 2.0, *m.Counter.Value)
				} else if taskType == "fleetRollout" {
					assert.Equal(t, 1.0, *m.Counter.Value)
				}
			}
		}
	}
}

func TestWorkerCollector_GaugeValues(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	cfg := &config.Config{}
	mockProvider := &MockQueuesProvider{}

	collector := NewWorkerCollector(ctx, log, cfg, mockProvider)

	// Set gauge values
	collector.SetQueueDepth("task_queue", 10.0)
	collector.SetConsumersActive(5.0)
	collector.SetRedisConnectionStatus(true)

	// Create a registry and gather metrics
	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	require.NoError(t, err)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	// Find and verify gauge values
	for _, mf := range metrics {
		switch *mf.Name {
		case "flightctl_worker_queue_depth":
			assert.Len(t, mf.Metric, 1)
			assert.Equal(t, 10.0, *mf.Metric[0].Gauge.Value)
			queue := getLabelValue(mf.Metric[0].Label, "queue")
			assert.Equal(t, "task_queue", queue)
		case "flightctl_worker_consumers_active":
			assert.Len(t, mf.Metric, 1)
			assert.Equal(t, 5.0, *mf.Metric[0].Gauge.Value)
		case "flightctl_worker_redis_up":
			// Should have 1 metric: up=1 or down=0
			assert.Len(t, mf.Metric, 1)
			// Should have exactly one metric with value 1 (up) or 0 (down)
			assert.Len(t, mf.Metric, 1)
			value := *mf.Metric[0].Gauge.Value
			assert.True(t, value == 0.0 || value == 1.0, "Redis up metric should be 0 or 1, got %f", value)
		}
	}
}

func TestWorkerCollector_HistogramValues(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	cfg := &config.Config{}
	mockProvider := &MockQueuesProvider{}

	collector := NewWorkerCollector(ctx, log, cfg, mockProvider)

	// Record histogram observations
	collector.ObserveProcessingDuration(100 * time.Millisecond)
	collector.ObserveProcessingDuration(200 * time.Millisecond)
	collector.ObserveTaskExecutionDuration("deviceRender", 150*time.Millisecond)

	// Create a registry and gather metrics
	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	require.NoError(t, err)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	// Find and verify histogram values
	for _, mf := range metrics {
		switch *mf.Name {
		case "flightctl_worker_message_processing_duration_seconds":
			assert.Len(t, mf.Metric, 1)
			assert.Equal(t, uint64(2), *mf.Metric[0].Histogram.SampleCount)
			assert.True(t, *mf.Metric[0].Histogram.SampleSum > 0.0)
		case "flightctl_worker_task_execution_duration_seconds":
			assert.Len(t, mf.Metric, 1)
			assert.Equal(t, uint64(1), *mf.Metric[0].Histogram.SampleCount)
			assert.True(t, *mf.Metric[0].Histogram.SampleSum > 0.0)
			taskType := getLabelValue(mf.Metric[0].Label, "task_type")
			assert.Equal(t, "deviceRender", taskType)
		}
	}
}

func TestWorkerCollector_RedisConnectionStatus(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	cfg := &config.Config{}
	mockProvider := &MockQueuesProvider{}

	collector := NewWorkerCollector(ctx, log, cfg, mockProvider)

	// Test connected status
	collector.SetRedisConnectionStatus(true)

	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	require.NoError(t, err)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	// Verify redis_up=1 (connected)
	var connectionMetric *dto.MetricFamily
	for _, mf := range metrics {
		if *mf.Name == "flightctl_worker_redis_up" {
			connectionMetric = mf
			break
		}
	}
	require.NotNil(t, connectionMetric)
	assert.Len(t, connectionMetric.Metric, 1)
	assert.Equal(t, 1.0, *connectionMetric.Metric[0].Gauge.Value)

	// Test disconnected status
	collector.SetRedisConnectionStatus(false)

	// Re-gather metrics
	metrics, err = registry.Gather()
	require.NoError(t, err)

	for _, mf := range metrics {
		if *mf.Name == "flightctl_worker_redis_up" {
			// Should have exactly one metric with value 1 (up) or 0 (down)
			assert.Len(t, mf.Metric, 1)
			value := *mf.Metric[0].Gauge.Value
			assert.True(t, value == 0.0 || value == 1.0, "Redis up metric should be 0 or 1, got %f", value)
			break
		}
	}
}

// ============================================================================
// HTTP Endpoint Tests - Testing metrics exposure via HTTP
// ============================================================================

func TestWorkerCollector_HTTPMetricsEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping HTTP endpoint test in short mode")
	}

	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // Reduce noise in test output

	// Create minimal config
	cfg := &config.Config{}

	// Create worker collector with mock provider
	mockProvider := &MockQueuesProvider{}
	workerCollector := NewWorkerCollector(ctx, log, cfg, mockProvider)

	// Record some test metrics
	workerCollector.IncMessagesInProgress()
	workerCollector.IncMessagesProcessed("success")
	workerCollector.IncTasksByType("deviceRender")
	workerCollector.SetConsumersActive(3.0)
	workerCollector.SetRedisConnectionStatus(true)

	// Create a simple HTTP handler using the metrics package
	handler := metrics.NewHandler(workerCollector)

	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Test metrics endpoint
	resp, err := http.Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Read response body
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyStr := string(body)

	// Verify expected metrics are present
	expectedMetrics := []string{
		"flightctl_worker_messages_in_progress",
		"flightctl_worker_messages_processed_total",
		"flightctl_worker_tasks_by_type_total",
		"flightctl_worker_consumers_active",
		"flightctl_worker_redis_up",
	}

	for _, metric := range expectedMetrics {
		assert.Contains(t, bodyStr, metric, "Expected metric %s not found in response", metric)
	}

	// Verify specific metric values
	assert.Contains(t, bodyStr, `flightctl_worker_messages_in_progress 1`)
	assert.Contains(t, bodyStr, `flightctl_worker_messages_processed_total{status="success"} 1`)
	assert.Contains(t, bodyStr, `flightctl_worker_tasks_by_type_total{task_type="deviceRender"} 1`)
	assert.Contains(t, bodyStr, `flightctl_worker_consumers_active 3`)

	// Server auto-closes via defer
}

func TestWorkerCollector_MetricsServerDisabled(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create config with no metrics configuration (disabled)
	cfg := &config.Config{}

	metricsServer := instrumentation.NewMetricsServer(log, cfg)

	// Should return error when disabled
	err := metricsServer.Run(ctx)
	assert.Error(t, err)
	// The exact error message depends on the configuration validation
}

// ============================================================================
// Helper Functions
// ============================================================================

// Helper function to get label value from metric labels
func getLabelValue(labels []*dto.LabelPair, name string) string {
	for _, label := range labels {
		if *label.Name == name {
			return *label.Value
		}
	}
	return ""
}
