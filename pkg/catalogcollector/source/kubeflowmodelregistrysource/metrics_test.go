package kubeflowmodelregistrysource

import (
	"context"
	"testing"
	"time"

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
