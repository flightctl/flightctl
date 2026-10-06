package kubeflowmodelregistrysource

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	meterName = "flightctl.catalogcollector"

	collectionCounterName    = "flightctl.catalogcollector.source.collections"
	collectionDurationName   = "flightctl.catalogcollector.source.collection.duration"
	lastSuccessTimestampName = "flightctl.catalogcollector.source.last_success.timestamp"
)

const sourceIDAttribute = "source.id"

// sourceMetrics holds the per-source collection instruments.
//
// These instruments are separate from the service-owned pipeline instruments,
// which record work only after a source has produced a complete snapshot.
// Source metrics record every collection attempt, including failures that
// occur before a snapshot is emitted.
type sourceMetrics struct {
	sourceID string

	collections metric.Int64Counter
	duration    metric.Float64Histogram

	// The observable gauge callback may execute concurrently with collection
	// completion, so access to this value must be synchronized.
	lastSuccessTS atomic.Int64

	// Keep the observable instrument and callback registration associated with
	// this metrics instance for the lifetime of the source. The registration
	// is retained so each source's callback is independently observed by the
	// SDK, avoiding the problem where multiple Int64ObservableGauge creations
	// with WithInt64Callback silently drop all but the first callback.
	lastSuccessGauge metric.Int64ObservableGauge
	callbackReg      metric.Registration
}

func newMetrics(
	sourceID string,
	meterProvider metric.MeterProvider,
) (*sourceMetrics, error) {
	if meterProvider == nil {
		return nil, fmt.Errorf("meter provider must not be nil")
	}

	meter := meterProvider.Meter(meterName)

	collections, err := meter.Int64Counter(
		collectionCounterName,
		metric.WithDescription(
			"Number of Model Registry collection attempts",
		),
		metric.WithUnit("{collection}"),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"creating %s counter: %w",
			collectionCounterName,
			err,
		)
	}

	duration, err := meter.Float64Histogram(
		collectionDurationName,
		metric.WithDescription(
			"Duration of Model Registry collection attempts",
		),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"creating %s histogram: %w",
			collectionDurationName,
			err,
		)
	}

	metrics := &sourceMetrics{
		sourceID:    sourceID,
		collections: collections,
		duration:    duration,
	}

	gauge, err := meter.Int64ObservableGauge(
		lastSuccessTimestampName,
		metric.WithDescription(
			"Unix timestamp in seconds of the last successful collection",
		),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"creating %s gauge: %w",
			lastSuccessTimestampName,
			err,
		)
	}
	metrics.lastSuccessGauge = gauge

	// Register the callback via meter.RegisterCallback so that each source
	// gets its own independently-observed callback. Using WithInt64Callback
	// at gauge-creation time would silently drop all callbacks after the
	// first registration for the same instrument name.
	reg, err := meter.RegisterCallback(
		func(
			_ context.Context,
			observer metric.Observer,
		) error {
			timestamp := metrics.lastSuccessTS.Load()
			if timestamp == 0 {
				return nil
			}

			observer.ObserveInt64(
				gauge,
				timestamp,
				metric.WithAttributes(
					attribute.String(
						sourceIDAttribute,
						metrics.sourceID,
					),
				),
			)
			return nil
		},
		gauge,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"registering %s callback: %w",
			lastSuccessTimestampName,
			err,
		)
	}
	metrics.callbackReg = reg

	return metrics, nil
}

// recordCollection records the outcome of one collection attempt.
//
// Its signature matches pollsource.Helper.OnCollect, so it can be installed
// directly as the helper's collection callback. The helper invokes it once per
// attempt, before the snapshot reaches the downstream consumer, so a
// downstream failure is never attributed to Model Registry collection.
func (m *sourceMetrics) recordCollection(
	elapsed time.Duration,
	err error,
) {
	if err != nil {
		m.recordFailure(elapsed, err)
		return
	}

	m.recordSuccess(elapsed)
}

// recordSuccess records a successful collection attempt and advances the
// last-success timestamp.
func (m *sourceMetrics) recordSuccess(elapsed time.Duration) {
	attributes := metric.WithAttributes(
		attribute.String(sourceIDAttribute, m.sourceID),
		attribute.String("outcome", "success"),
	)

	ctx := context.Background()
	m.collections.Add(ctx, 1, attributes)
	m.duration.Record(ctx, elapsed.Seconds(), attributes)
	m.lastSuccessTS.Store(time.Now().Unix())
}

// recordFailure records an unsuccessful collection attempt.
//
// Service cancellation is reported separately from operational failures.
// Request and collection deadline expiration remains a failure because it
// indicates that the Model Registry did not complete within its configured
// timeout.
func (m *sourceMetrics) recordFailure(
	elapsed time.Duration,
	err error,
) {
	outcome := "failure"
	if errors.Is(err, context.Canceled) {
		outcome = "cancelled"
	}

	attributes := metric.WithAttributes(
		attribute.String(sourceIDAttribute, m.sourceID),
		attribute.String("outcome", outcome),
	)

	ctx := context.Background()
	m.collections.Add(ctx, 1, attributes)
	m.duration.Record(ctx, elapsed.Seconds(), attributes)
}
