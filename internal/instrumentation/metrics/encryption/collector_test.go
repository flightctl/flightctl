package encryption

import (
	"context"
	"testing"
	"time"

	enc "github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptionCollector_Implements_Collector(t *testing.T) {
	mgr := createTestManager(t)
	collector := NewEncryptionCollector(mgr)

	// Verify it implements prometheus.Collector
	var _ prometheus.Collector = collector
}

func TestEncryptionCollector_RecordOperation(t *testing.T) {
	mgr := createTestManager(t)
	collector := NewEncryptionCollector(mgr)

	// Record some operations
	collector.RecordOperation("encrypt", "v1", "default", "success", 100*time.Microsecond)
	collector.RecordOperation("encrypt", "v1", "default", "success", 200*time.Microsecond)
	collector.RecordOperation("decrypt", "v1", "default", "success", 50*time.Microsecond)
	collector.RecordOperation("encrypt", "v1", "default", "error", 10*time.Microsecond)

	// Check counter values
	assert.Equal(t, 2.0, testutil.ToFloat64(collector.operationsTotal.WithLabelValues("encrypt", "v1", "default", "success")))
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.operationsTotal.WithLabelValues("decrypt", "v1", "default", "success")))
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.operationsTotal.WithLabelValues("encrypt", "v1", "default", "error")))

	// Check histogram was updated (only for success)
	// Histogram should have 2 observations for encrypt successes
	count := testutil.CollectAndCount(collector.operationDuration)
	assert.Greater(t, count, 0, "Histogram should have observations")
}

func TestEncryptionCollector_RecordError(t *testing.T) {
	mgr := createTestManager(t)
	collector := NewEncryptionCollector(mgr)

	// Record some errors
	collector.RecordError("encrypt", "v1", "default", "encryption_failed")
	collector.RecordError("decrypt", "v1", "default", "decryption_failed")
	collector.RecordError("decrypt", "v1", "default", "decryption_failed")

	// Check counter values
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.errorsTotal.WithLabelValues("encrypt", "v1", "default", "encryption_failed")))
	assert.Equal(t, 2.0, testutil.ToFloat64(collector.errorsTotal.WithLabelValues("decrypt", "v1", "default", "decryption_failed")))
}

func TestEncryptionCollector_RecordCanaryValidation(t *testing.T) {
	mgr := createTestManager(t)
	collector := NewEncryptionCollector(mgr)

	// Record some canary validations
	collector.RecordCanaryValidation("v1", "default", "success")
	collector.RecordCanaryValidation("v1", "default", "success")
	collector.RecordCanaryValidation("v1", "default", "decrypt_failed")
	collector.RecordCanaryValidation("v1", "default", "mismatch")

	// Check counter values
	assert.Equal(t, 2.0, testutil.ToFloat64(collector.canaryValidationsTotal.WithLabelValues("v1", "default", "success")))
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.canaryValidationsTotal.WithLabelValues("v1", "default", "decrypt_failed")))
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.canaryValidationsTotal.WithLabelValues("v1", "default", "mismatch")))
}

func TestEncryptionCollector_ActiveKeyInfo(t *testing.T) {
	mgr := createTestManager(t)
	collector := NewEncryptionCollector(mgr)

	// Collect metrics to trigger updateActiveKeyInfo
	ch := make(chan prometheus.Metric, 100)
	collector.Collect(ch)
	close(ch)

	// Check that active key info gauge was set
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.activeKeyInfo.WithLabelValues("v1", "default", "AES-256-GCM")))
}

func TestEncryptionCollector_ActiveKeyInfo_NoStrategy(t *testing.T) {
	mgr := enc.NewManager() // No strategy registered
	collector := NewEncryptionCollector(mgr)

	// Collect metrics
	ch := make(chan prometheus.Metric, 100)
	collector.Collect(ch)
	close(ch)

	// Should not panic, gauge should be reset
	// We can't directly check if it's zero because the gauge doesn't exist without labels
}

func TestEncryptionCollector_Integration_WithManager(t *testing.T) {
	mgr := createTestManager(t)
	collector := NewEncryptionCollector(mgr)
	mgr.SetMetricsRecorder(collector)

	ctx := context.Background()
	plaintext := []byte("test-data")

	// Perform encryption
	encrypted, err := mgr.Encrypt(ctx, plaintext)
	require.NoError(t, err)

	// Perform decryption
	decrypted, err := mgr.Decrypt(ctx, encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)

	// Check that metrics were recorded
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.operationsTotal.WithLabelValues("encrypt", "v1", "default", "success")))
	assert.Equal(t, 1.0, testutil.ToFloat64(collector.operationsTotal.WithLabelValues("decrypt", "v1", "default", "success")))
}

// Helper function to create a test manager
func createTestManager(t *testing.T) *enc.Manager {
	t.Helper()

	// Generate and set test key via environment variable
	key, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	t.Setenv("FLIGHTCTL_ENCRYPTION_KEY", key)

	// Create strategy
	strategy, err := enc.NewV1Strategy()
	require.NoError(t, err)

	mgr := enc.NewManager()
	mgr.RegisterStrategy(strategy, true)

	return mgr
}
