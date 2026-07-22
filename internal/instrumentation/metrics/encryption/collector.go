package encryption

import (
	"time"

	enc "github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/prometheus/client_golang/prometheus"
)

// EncryptionCollector implements prometheus.Collector for encryption metrics.
type EncryptionCollector struct {
	// Cryptographic operation count
	operationsTotal *prometheus.CounterVec

	// Cryptographic operation latency
	operationDuration *prometheus.HistogramVec

	// Encryption errors
	errorsTotal *prometheus.CounterVec

	// Canary validation
	canaryValidationsTotal *prometheus.CounterVec

	// Active encryption configuration
	activeKeyInfo *prometheus.GaugeVec

	// Reference to manager for active key info
	manager *enc.Manager
}

// NewEncryptionCollector creates a new EncryptionCollector.
func NewEncryptionCollector(manager *enc.Manager) *EncryptionCollector {
	return &EncryptionCollector{
		operationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_encryption_operations_total",
				Help: "Total number of encryption operations by operation type, strategy, key identifier, and result",
			},
			[]string{"operation", "strategy", "key_id", "status"},
		),
		operationDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "flightctl_encryption_operation_duration_seconds",
				Help:    "Duration of encryption and decryption operations by strategy and key identifier",
				Buckets: prometheus.ExponentialBuckets(0.0001, 2, 12), // 0.1ms to 204.8ms
			},
			[]string{"operation", "strategy", "key_id"},
		),
		errorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_encryption_errors_total",
				Help: "Total number of encryption errors by operation, strategy, key identifier, and error type",
			},
			[]string{"operation", "strategy", "key_id", "error_type"},
		),
		canaryValidationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_encryption_canary_validations_total",
				Help: "Total number of canary validation attempts and their outcomes",
			},
			[]string{"strategy", "key_id", "status"},
		),
		activeKeyInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "flightctl_encryption_active_key_info",
				Help: "Active encryption configuration (strategy, key identifier, and algorithm)",
			},
			[]string{"strategy", "key_id", "algorithm"},
		),
		manager: manager,
	}
}

// Describe implements prometheus.Collector.
func (c *EncryptionCollector) Describe(ch chan<- *prometheus.Desc) {
	c.operationsTotal.Describe(ch)
	c.operationDuration.Describe(ch)
	c.errorsTotal.Describe(ch)
	c.canaryValidationsTotal.Describe(ch)
	c.activeKeyInfo.Describe(ch)
}

// Collect implements prometheus.Collector.
func (c *EncryptionCollector) Collect(ch chan<- prometheus.Metric) {
	c.operationsTotal.Collect(ch)
	c.operationDuration.Collect(ch)
	c.errorsTotal.Collect(ch)
	c.canaryValidationsTotal.Collect(ch)

	// Update active key info gauge before collecting
	c.updateActiveKeyInfo()
	c.activeKeyInfo.Collect(ch)
}

// updateActiveKeyInfo updates the active key info gauge.
func (c *EncryptionCollector) updateActiveKeyInfo() {
	if c.manager == nil {
		return
	}

	version, strategy := c.manager.GetActiveStrategy()
	if version == "" || strategy == nil {
		// No active strategy - reset gauge
		c.activeKeyInfo.Reset()
		return
	}

	keyID := strategy.ActiveKeyID()
	algorithm := strategy.Algorithm()

	// Reset and set the current active configuration
	c.activeKeyInfo.Reset()
	c.activeKeyInfo.WithLabelValues(version, keyID, algorithm).Set(1)
}

// RecordOperation records a successful or failed encryption/decryption operation.
func (c *EncryptionCollector) RecordOperation(operation, strategy, keyID, status string, duration time.Duration) {
	c.operationsTotal.WithLabelValues(operation, strategy, keyID, status).Inc()
	if status == "success" {
		c.operationDuration.WithLabelValues(operation, strategy, keyID).Observe(duration.Seconds())
	}
}

// RecordError records an encryption error.
func (c *EncryptionCollector) RecordError(operation, strategy, keyID, errorType string) {
	c.errorsTotal.WithLabelValues(operation, strategy, keyID, errorType).Inc()
}

// RecordCanaryValidation records a canary validation attempt.
func (c *EncryptionCollector) RecordCanaryValidation(strategy, keyID, status string) {
	c.canaryValidationsTotal.WithLabelValues(strategy, keyID, status).Inc()
}
