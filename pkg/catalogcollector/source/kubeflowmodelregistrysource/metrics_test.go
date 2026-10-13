package kubeflowmodelregistrysource

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestMetrics_ManualReader_TwoSources verifies that two sources sharing the
// same MeterProvider each produce an independent last-success gauge observation.
//
// This is a regression test for the bug where WithInt64Callback at gauge
// creation time caused the OTel SDK to retain only the first source's callback.
// The fix uses meter.RegisterCallback per source instead.
func TestMetrics_ManualReader_TwoSources(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer provider.Shutdown(context.Background()) //nolint:errcheck

	m1, err := newMetrics("source-alpha", provider)
	if err != nil {
		t.Fatalf("newMetrics(source-alpha): %v", err)
	}
	m2, err := newMetrics("source-beta", provider)
	if err != nil {
		t.Fatalf("newMetrics(source-beta): %v", err)
	}

	// Record a success in each source at distinct timestamps.
	m1.lastSuccessTS.Store(1000)
	m2.lastSuccessTS.Store(2000)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Find the last-success gauge observations.
	type observation struct {
		sourceID  string
		timestamp int64
	}
	var observations []observation

	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != lastSuccessTimestampName {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("expected Gauge[int64], got %T", m.Data)
			}
			for _, dp := range gauge.DataPoints {
				var sid string
				for _, attr := range dp.Attributes.ToSlice() {
					if string(attr.Key) == sourceIDAttribute {
						sid = attr.Value.AsString()
					}
				}
				observations = append(observations, observation{
					sourceID:  sid,
					timestamp: dp.Value,
				})
			}
		}
	}

	if len(observations) != 2 {
		t.Fatalf("expected 2 gauge observations (one per source), got %d", len(observations))
	}

	foundAlpha, foundBeta := false, false
	for _, obs := range observations {
		switch obs.sourceID {
		case "source-alpha":
			foundAlpha = true
			if obs.timestamp != 1000 {
				t.Errorf("source-alpha timestamp = %d, want 1000", obs.timestamp)
			}
		case "source-beta":
			foundBeta = true
			if obs.timestamp != 2000 {
				t.Errorf("source-beta timestamp = %d, want 2000", obs.timestamp)
			}
		default:
			t.Errorf("unexpected source ID %q", obs.sourceID)
		}
	}

	if !foundAlpha {
		t.Error("source-alpha observation missing — second source callback was not registered")
	}
	if !foundBeta {
		t.Error("source-beta observation missing — callback was dropped")
	}

	// Verify the retained callback registrations are non-nil.
	if m1.callbackReg == nil {
		t.Error("source-alpha callbackReg is nil")
	}
	if m2.callbackReg == nil {
		t.Error("source-beta callbackReg is nil")
	}
}

// TestMetrics_RecordSuccess verifies that recordSuccess advances the
// last-success timestamp.
func TestMetrics_RecordSuccess(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer provider.Shutdown(context.Background()) //nolint:errcheck

	m, err := newMetrics("test-source", provider)
	if err != nil {
		t.Fatalf("newMetrics: %v", err)
	}

	m.recordSuccess(100 * time.Millisecond)

	ts := m.lastSuccessTS.Load()
	if ts == 0 {
		t.Error("lastSuccessTS should be non-zero after recordSuccess")
	}
}

// --- collection-callback semantics -----------------------------------------

// collectionOutcomes reads the per-outcome collection counter values recorded
// for one source.
func collectionOutcomes(
	t *testing.T,
	reader *metric.ManualReader,
	sourceID string,
) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	outcomes := make(map[string]int64)

	for _, scope := range rm.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != collectionCounterName {
				continue
			}
			sum, ok := recorded.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("expected Sum[int64] for %s, got %T", collectionCounterName, recorded.Data)
			}
			for _, dp := range sum.DataPoints {
				attrs := attributesOf(dp.Attributes.ToSlice())
				if attrs[sourceIDAttribute] != sourceID {
					continue
				}
				outcomes[attrs["outcome"]] += dp.Value
			}
		}
	}

	return outcomes
}

// durationCount reports how many duration observations were recorded for one
// source and outcome, along with the histogram's unit.
func durationCount(
	t *testing.T,
	reader *metric.ManualReader,
	sourceID string,
	outcome string,
) (count uint64, unit string) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for _, scope := range rm.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != collectionDurationName {
				continue
			}
			unit = recorded.Unit
			histogram, ok := recorded.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("expected Histogram[float64] for %s, got %T", collectionDurationName, recorded.Data)
			}
			for _, dp := range histogram.DataPoints {
				attrs := attributesOf(dp.Attributes.ToSlice())
				if attrs[sourceIDAttribute] != sourceID || attrs["outcome"] != outcome {
					continue
				}
				count += dp.Count
			}
		}
	}

	return count, unit
}

// lastSuccessTimestamp reads the last-success gauge observation for one
// source. The second return value reports whether the gauge was observed at
// all: the callback stays silent until the first successful collection.
func lastSuccessTimestamp(
	t *testing.T,
	reader *metric.ManualReader,
	sourceID string,
) (int64, bool) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for _, scope := range rm.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != lastSuccessTimestampName {
				continue
			}
			gauge, ok := recorded.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("expected Gauge[int64] for %s, got %T", lastSuccessTimestampName, recorded.Data)
			}
			for _, dp := range gauge.DataPoints {
				attrs := attributesOf(dp.Attributes.ToSlice())
				if attrs[sourceIDAttribute] == sourceID {
					return dp.Value, true
				}
			}
		}
	}

	return 0, false
}

func attributesOf(attrs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		out[string(attr.Key)] = attr.Value.AsString()
	}
	return out
}

// TestMetrics_RecordCollection_Outcomes exercises the collection callback
// against the SDK ManualReader and asserts the recorded instrument names,
// attributes, units, and cancellation classification.
func TestMetrics_RecordCollection_Outcomes(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantOutcome string
	}{
		{
			name:        "when the collection succeeds it should record a success",
			err:         nil,
			wantOutcome: "success",
		},
		{
			name:        "when the collection fails it should record a failure",
			err:         errors.New("connection refused"),
			wantOutcome: "failure",
		},
		{
			name:        "when the collection is cancelled it should record a cancellation",
			err:         fmt.Errorf("listing registered models: %w", context.Canceled),
			wantOutcome: "cancelled",
		},
		{
			name:        "when the collection deadline expires it should record a failure",
			err:         fmt.Errorf("listing registered models: %w", context.DeadlineExceeded),
			wantOutcome: "failure",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := metric.NewManualReader()
			provider := metric.NewMeterProvider(metric.WithReader(reader))
			defer provider.Shutdown(context.Background()) //nolint:errcheck

			m, err := newMetrics("outcome-source", provider)
			if err != nil {
				t.Fatalf("newMetrics: %v", err)
			}

			m.recordCollection(250*time.Millisecond, tc.err)

			outcomes := collectionOutcomes(t, reader, "outcome-source")
			if got := outcomes[tc.wantOutcome]; got != 1 {
				t.Errorf("collections{outcome=%s} = %d, want 1 (all outcomes: %v)", tc.wantOutcome, got, outcomes)
			}
			if len(outcomes) != 1 {
				t.Errorf("recorded outcomes = %v, want only %q", outcomes, tc.wantOutcome)
			}

			count, unit := durationCount(t, reader, "outcome-source", tc.wantOutcome)
			if count != 1 {
				t.Errorf("duration observations for outcome %q = %d, want 1", tc.wantOutcome, count)
			}
			if unit != "s" {
				t.Errorf("duration unit = %q, want %q", unit, "s")
			}
		})
	}
}

// TestMetrics_RecordCollection_FailureLeavesLastSuccessUnchanged asserts that
// a failed collection never advances or clears the last-success gauge.
func TestMetrics_RecordCollection_FailureLeavesLastSuccessUnchanged(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer provider.Shutdown(context.Background()) //nolint:errcheck

	m, err := newMetrics("last-success-source", provider)
	if err != nil {
		t.Fatalf("newMetrics: %v", err)
	}

	// Before any success the gauge reports nothing at all.
	if _, observed := lastSuccessTimestamp(t, reader, "last-success-source"); observed {
		t.Error("last-success gauge was observed before any successful collection")
	}

	m.recordCollection(10*time.Millisecond, nil)

	afterSuccess, observed := lastSuccessTimestamp(t, reader, "last-success-source")
	if !observed {
		t.Fatal("last-success gauge was not observed after a successful collection")
	}
	if afterSuccess == 0 {
		t.Fatal("last-success gauge = 0 after a successful collection")
	}

	m.recordCollection(20*time.Millisecond, errors.New("connection refused"))
	m.recordCollection(30*time.Millisecond, fmt.Errorf("aborting: %w", context.Canceled))

	afterFailure, observed := lastSuccessTimestamp(t, reader, "last-success-source")
	if !observed {
		t.Fatal("last-success gauge disappeared after a failed collection")
	}
	if afterFailure != afterSuccess {
		t.Errorf("last-success gauge = %d after failures, want it unchanged at %d", afterFailure, afterSuccess)
	}

	outcomes := collectionOutcomes(t, reader, "last-success-source")
	want := map[string]int64{"success": 1, "failure": 1, "cancelled": 1}
	for outcome, wantValue := range want {
		if outcomes[outcome] != wantValue {
			t.Errorf("collections{outcome=%s} = %d, want %d (all: %v)", outcome, outcomes[outcome], wantValue, outcomes)
		}
	}
}
