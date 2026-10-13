package service

import (
	"context"
	"errors"
	"testing"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// --- outcomeAttr tests ---

func TestOutcomeAttr(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "When error is nil it should return success",
			err:      nil,
			expected: "success",
		},
		{
			name:     "When error is context.Canceled it should return cancelled",
			err:      context.Canceled,
			expected: "cancelled",
		},
		{
			name:     "When error is context.DeadlineExceeded it should return cancelled",
			err:      context.DeadlineExceeded,
			expected: "cancelled",
		},
		{
			name:     "When error wraps context.Canceled it should return cancelled",
			err:      errors.Join(errors.New("wrapper"), context.Canceled),
			expected: "cancelled",
		},
		{
			name:     "When error is a generic failure it should return failure",
			err:      errors.New("connection refused"),
			expected: "failure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attr := outcomeAttr(tc.err)
			assert.Equal(t, "outcome", string(attr.Key))
			assert.Equal(t, tc.expected, attr.Value.AsString())
		})
	}
}

// --- Source instrumentation tests ---

func testSnapshot() *catalogcollector.CatalogSnapshot {
	return &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			{Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("cat-a")}},
			{Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("cat-b")}},
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			{Metadata: apiv1alpha1.CatalogItemMeta{Name: lo.ToPtr("item-1")}},
			{Metadata: apiv1alpha1.CatalogItemMeta{Name: lo.ToPtr("item-2")}},
			{Metadata: apiv1alpha1.CatalogItemMeta{Name: lo.ToPtr("item-3")}},
		},
	}
}

func TestInstrumentedFanoutConsumer_RecordsSourceMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	instr, err := newSourceInstruments(mp)
	require.NoError(t, err)

	inner := &recordingConsumer{}
	consumer := &instrumentedFanoutConsumer{
		inner:       inner,
		sourceID:    "http/dev-input",
		instruments: instr,
	}

	snap := testSnapshot()
	require.NoError(t, consumer.Consume(context.Background(), snap))

	// Verify snapshot was forwarded to inner consumer.
	require.Len(t, inner.snapshots, 1)
	require.Same(t, snap, inner.snapshots[0])

	// Collect and verify metrics.
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	metrics := flattenMetrics(rm)
	assertCounterValue(t, metrics, sourceSnapshotsName, 1,
		attribute.String("source.id", "http/dev-input"),
	)
	assertInt64HistogramSum(t, metrics, sourceResourcesName, 2,
		attribute.String("source.id", "http/dev-input"),
		attribute.String("resource.kind", "Catalog"),
	)
	assertInt64HistogramSum(t, metrics, sourceResourcesName, 3,
		attribute.String("source.id", "http/dev-input"),
		attribute.String("resource.kind", "CatalogItem"),
	)
}

// --- Pipeline instrumentation tests ---

func TestInstrumentedBranch_RecordsPipelineMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	instr, err := newPipelineInstruments(mp)
	require.NoError(t, err)

	inner := &recordingConsumer{}
	branch := &instrumentedBranch{
		inner:         inner,
		pipelineID:    "prod-pipeline",
		destinationID: "flightctl/local",
		instruments:   instr,
	}

	snap := testSnapshot()
	require.NoError(t, branch.Consume(context.Background(), snap))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	metrics := flattenMetrics(rm)
	assertCounterValue(t, metrics, pipelineSyncsName, 1,
		attribute.String("pipeline.id", "prod-pipeline"),
		attribute.String("destination.id", "flightctl/local"),
		attribute.String("outcome", "success"),
	)

	// Duration must be recorded (non-negative).
	assertFloat64HistogramRecorded(t, metrics, pipelineDurationName,
		attribute.String("pipeline.id", "prod-pipeline"),
		attribute.String("destination.id", "flightctl/local"),
		attribute.String("outcome", "success"),
	)

	assertInt64HistogramSum(t, metrics, pipelineResourceName, 2,
		attribute.String("pipeline.id", "prod-pipeline"),
		attribute.String("destination.id", "flightctl/local"),
		attribute.String("outcome", "success"),
		attribute.String("resource.kind", "Catalog"),
	)
	assertInt64HistogramSum(t, metrics, pipelineResourceName, 3,
		attribute.String("pipeline.id", "prod-pipeline"),
		attribute.String("destination.id", "flightctl/local"),
		attribute.String("outcome", "success"),
		attribute.String("resource.kind", "CatalogItem"),
	)
}

func TestInstrumentedFanoutConsumer_NilSnapshot_ReturnsError(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	instr, err := newSourceInstruments(mp)
	require.NoError(t, err)

	inner := &recordingConsumer{}
	consumer := &instrumentedFanoutConsumer{
		inner:       inner,
		sourceID:    "test-source",
		instruments: instr,
	}

	err = consumer.Consume(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source emitted nil snapshot")

	// Inner consumer must not be called.
	require.Empty(t, inner.snapshots, "inner consumer must not be called for nil snapshot")
}

func TestInstrumentedBranch_RecordsFailureOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	instr, err := newPipelineInstruments(mp)
	require.NoError(t, err)

	expectedErr := errors.New("destination unreachable")
	branch := &instrumentedBranch{
		inner:         &errorConsumer{err: expectedErr},
		pipelineID:    "test-pipeline",
		destinationID: "flightctl/local",
		instruments:   instr,
	}

	err = branch.Consume(context.Background(), testSnapshot())
	require.ErrorIs(t, err, expectedErr)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	metrics := flattenMetrics(rm)
	assertCounterValue(t, metrics, pipelineSyncsName, 1,
		attribute.String("pipeline.id", "test-pipeline"),
		attribute.String("destination.id", "flightctl/local"),
		attribute.String("outcome", "failure"),
	)
}

func TestInstrumentedBranch_ValidationFailure_RecordsFailureOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	instr, err := newPipelineInstruments(mp)
	require.NoError(t, err)

	// Use a REAL destinationConsumer so that snapshot validation is exercised
	// through the production code path rather than a fabricated error.
	dst := &recordingConsumer{}
	realConsumer := &destinationConsumer{
		pipelineID:  "test-pipeline",
		destination: dst,
	}
	branch := &instrumentedBranch{
		inner:         realConsumer,
		pipelineID:    "test-pipeline",
		destinationID: "flightctl/local",
		instruments:   instr,
	}

	// Submit an invalid snapshot (empty revision) to trigger real validation.
	invalidSnap := &catalogcollector.CatalogSnapshot{Revision: ""}
	err = branch.Consume(context.Background(), invalidSnap)
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot validation failed")

	// The destination must NOT have been invoked — validation should have
	// short-circuited before reaching the destination.
	require.Empty(t, dst.snapshots, "destination must not be called for invalid snapshot")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	metrics := flattenMetrics(rm)
	assertCounterValue(t, metrics, pipelineSyncsName, 1,
		attribute.String("pipeline.id", "test-pipeline"),
		attribute.String("destination.id", "flightctl/local"),
		attribute.String("outcome", "failure"),
	)
}

func TestInstrumentedBranch_RecordsCancelledOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	instr, err := newPipelineInstruments(mp)
	require.NoError(t, err)

	branch := &instrumentedBranch{
		inner:         &errorConsumer{err: context.Canceled},
		pipelineID:    "test-pipeline",
		destinationID: "flightctl/local",
		instruments:   instr,
	}

	err = branch.Consume(context.Background(), testSnapshot())
	require.ErrorIs(t, err, context.Canceled)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	metrics := flattenMetrics(rm)
	assertCounterValue(t, metrics, pipelineSyncsName, 1,
		attribute.String("pipeline.id", "test-pipeline"),
		attribute.String("destination.id", "flightctl/local"),
		attribute.String("outcome", "cancelled"),
	)
}

// =============================================================================
// Test helpers for OTel SDK metric assertions
// =============================================================================

type flatMetrics map[string]metricdata.Metrics

func flattenMetrics(rm metricdata.ResourceMetrics) flatMetrics {
	result := make(flatMetrics)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			result[m.Name] = m
		}
	}
	return result
}

func assertCounterValue(
	t *testing.T,
	metrics flatMetrics,
	name string,
	expected int64,
	attrs ...attribute.KeyValue,
) {
	t.Helper()
	m, ok := metrics[name]
	require.True(t, ok, "metric %q not found", name)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "metric %q is not an int64 Sum", name)

	for _, dp := range sum.DataPoints {
		if attributesMatch(dp.Attributes, attrs) {
			assert.Equal(t, expected, dp.Value,
				"metric %q value mismatch", name)
			return
		}
	}
	t.Errorf("metric %q: no data point matching attributes %v", name, attrs)
}

func assertInt64HistogramSum(
	t *testing.T,
	metrics flatMetrics,
	name string,
	expectedSum int64,
	attrs ...attribute.KeyValue,
) {
	t.Helper()
	m, ok := metrics[name]
	require.True(t, ok, "metric %q not found", name)

	hist, ok := m.Data.(metricdata.Histogram[int64])
	require.True(t, ok, "metric %q is not an int64 Histogram", name)

	for i := range hist.DataPoints {
		if attributesMatch(hist.DataPoints[i].Attributes, attrs) {
			assert.Equal(t, expectedSum, hist.DataPoints[i].Sum,
				"metric %q histogram sum mismatch", name)
			return
		}
	}
	t.Errorf("metric %q: no int64 histogram data point matching attributes %v", name, attrs)
}

func assertFloat64HistogramRecorded(
	t *testing.T,
	metrics flatMetrics,
	name string,
	attrs ...attribute.KeyValue,
) {
	t.Helper()
	m, ok := metrics[name]
	require.True(t, ok, "metric %q not found", name)

	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "metric %q is not a float64 Histogram", name)

	for i := range hist.DataPoints {
		if attributesMatch(hist.DataPoints[i].Attributes, attrs) {
			assert.GreaterOrEqual(t, hist.DataPoints[i].Sum, float64(0),
				"metric %q duration sum must be non-negative", name)
			assert.Equal(t, uint64(1), hist.DataPoints[i].Count,
				"metric %q must have exactly one observation", name)
			return
		}
	}
	t.Errorf("metric %q: no float64 histogram data point matching attributes %v", name, attrs)
}

func attributesMatch(
	set attribute.Set,
	want []attribute.KeyValue,
) bool {
	for _, kv := range want {
		v, found := set.Value(kv.Key)
		if !found || v != kv.Value {
			return false
		}
	}
	return true
}
