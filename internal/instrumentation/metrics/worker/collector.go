package worker

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// WorkerCollector implements NamedCollector and gathers worker-related metrics.
type WorkerCollector struct {
	// Message processing metrics
	messagesInProgressGauge  prometheus.Gauge
	messagesProcessedCounter *prometheus.CounterVec
	messageRetriesCounter    prometheus.Counter
	retryableFailuresCounter prometheus.Counter
	permanentFailuresCounter prometheus.Counter
	processingDurationHist   prometheus.Histogram

	// Task-specific metrics
	tasksByTypeCounter        *prometheus.CounterVec
	taskExecutionDurationHist *prometheus.HistogramVec

	// Queue health metrics
	consumersActiveGauge prometheus.Gauge
	queueDepthGauge      *prometheus.GaugeVec
	redisUpGauge         prometheus.Gauge

	// System metrics
	lastSuccessfulTaskGauge prometheus.Gauge

	log            logrus.FieldLogger
	cfg            *config.Config
	ctx            context.Context
	queuesProvider queues.Provider
}

// NewWorkerCollector creates a WorkerCollector.
func NewWorkerCollector(ctx context.Context, log logrus.FieldLogger, cfg *config.Config, queuesProvider queues.Provider) *WorkerCollector {
	collector := &WorkerCollector{
		// Message processing metrics
		messagesInProgressGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_worker_messages_in_progress",
			Help: "Current number of messages being processed",
		}),
		messagesProcessedCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_worker_messages_processed_total",
			Help: "Total number of messages processed by final outcome",
		}, []string{"status"}),
		messageRetriesCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_worker_message_retries_total",
			Help: "Total number of message retry attempts",
		}),
		retryableFailuresCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_worker_retryable_failures_total",
			Help: "Total number of retryable failures (messages queued for retry)",
		}),
		permanentFailuresCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_worker_permanent_failures_total",
			Help: "Total number of permanent failures (messages permanently dropped)",
		}),
		processingDurationHist: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "flightctl_worker_message_processing_duration_seconds",
			Help:    "Histogram of message processing time",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 15), // 10ms .. ~163s
		}),

		// Task-specific metrics
		tasksByTypeCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_worker_tasks_by_type_total",
			Help: "Total number of tasks executed by type",
		}, []string{"task_type"}),
		taskExecutionDurationHist: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "flightctl_worker_task_execution_duration_seconds",
			Help:    "Histogram of task execution time by type",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 14), // 50ms .. ~409s
		}, []string{"task_type"}),

		// Queue health metrics
		queueDepthGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_worker_queue_depth",
			Help: "Current queue depth",
		}, []string{"queue"}),
		consumersActiveGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_worker_consumers_active",
			Help: "Number of active consumer goroutines",
		}),
		redisUpGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_worker_redis_up",
			Help: "Redis connection up (1) or down (0)",
		}),

		// System metrics
		lastSuccessfulTaskGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_worker_last_successful_task_timestamp_seconds",
			Help: "Unix timestamp (seconds) of last successful task",
		}),

		log:            log,
		cfg:            cfg,
		ctx:            ctx,
		queuesProvider: queuesProvider,
	}

	collector.log.Debug("Worker metrics collector initialized")

	// Start monitoring Redis connection and queue depth
	if queuesProvider != nil {
		go collector.monitorQueueHealth()
	}

	return collector
}

func (c *WorkerCollector) MetricsName() string {
	return "worker"
}

func (c *WorkerCollector) Describe(ch chan<- *prometheus.Desc) {
	c.messagesInProgressGauge.Describe(ch)
	c.messagesProcessedCounter.Describe(ch)
	c.messageRetriesCounter.Describe(ch)
	c.retryableFailuresCounter.Describe(ch)
	c.permanentFailuresCounter.Describe(ch)
	c.processingDurationHist.Describe(ch)
	c.tasksByTypeCounter.Describe(ch)
	c.taskExecutionDurationHist.Describe(ch)
	c.queueDepthGauge.Describe(ch)
	c.consumersActiveGauge.Describe(ch)
	c.redisUpGauge.Describe(ch)
	c.lastSuccessfulTaskGauge.Describe(ch)
}

func (c *WorkerCollector) Collect(ch chan<- prometheus.Metric) {
	c.messagesInProgressGauge.Collect(ch)
	c.messagesProcessedCounter.Collect(ch)
	c.messageRetriesCounter.Collect(ch)
	c.retryableFailuresCounter.Collect(ch)
	c.permanentFailuresCounter.Collect(ch)
	c.processingDurationHist.Collect(ch)
	c.tasksByTypeCounter.Collect(ch)
	c.taskExecutionDurationHist.Collect(ch)
	c.queueDepthGauge.Collect(ch)
	c.consumersActiveGauge.Collect(ch)
	c.redisUpGauge.Collect(ch)
	c.lastSuccessfulTaskGauge.Collect(ch)
}

// Metric update methods to be called by the worker code

func (c *WorkerCollector) IncMessagesInProgress() {
	c.messagesInProgressGauge.Inc()
}

func (c *WorkerCollector) DecMessagesInProgress() {
	c.messagesInProgressGauge.Dec()
}

func (c *WorkerCollector) IncMessagesProcessed(status string) {
	c.messagesProcessedCounter.WithLabelValues(status).Inc()
}

func (c *WorkerCollector) IncMessageRetries() {
	c.messageRetriesCounter.Inc()
}

func (c *WorkerCollector) IncRetryableFailures() {
	c.retryableFailuresCounter.Inc()
}

func (c *WorkerCollector) IncPermanentFailures() {
	c.permanentFailuresCounter.Inc()
}

func (c *WorkerCollector) ObserveProcessingDuration(duration time.Duration) {
	c.processingDurationHist.Observe(duration.Seconds())
}

func (c *WorkerCollector) IncTasksByType(taskType string) {
	c.tasksByTypeCounter.WithLabelValues(taskType).Inc()
}

func (c *WorkerCollector) ObserveTaskExecutionDuration(taskType string, duration time.Duration) {
	c.taskExecutionDurationHist.WithLabelValues(taskType).Observe(duration.Seconds())
}

func (c *WorkerCollector) SetQueueDepth(queue string, depth float64) {
	c.queueDepthGauge.WithLabelValues(queue).Set(depth)
}

func (c *WorkerCollector) SetConsumersActive(count float64) {
	c.consumersActiveGauge.Set(count)
}

func (c *WorkerCollector) SetRedisConnectionStatus(connected bool) {
	if connected {
		c.redisUpGauge.Set(1)
	} else {
		c.redisUpGauge.Set(0)
	}
}

func (c *WorkerCollector) UpdateLastSuccessfulTask() {
	c.lastSuccessfulTaskGauge.Set(float64(time.Now().Unix()))
}

// monitorQueueHealth periodically checks Redis connection and queue depth
func (c *WorkerCollector) monitorQueueHealth() {
	ticker := time.NewTicker(10 * time.Second) // Check every 10 seconds
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			c.log.Info("Worker metrics collector context cancelled, stopping queue health monitoring")
			return
		case <-ticker.C:
			// Check Redis connection status with timeout
			hcCtx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
			err := c.queuesProvider.CheckHealth(hcCtx)
			cancel()
			if err != nil {
				c.SetRedisConnectionStatus(false)
				c.log.WithError(err).Warn("Redis connection health check failed")
			} else {
				c.SetRedisConnectionStatus(true)
			}
			// Queue depth: implement via queuesProvider when available; avoid emitting placeholder values.
		}
	}
}
