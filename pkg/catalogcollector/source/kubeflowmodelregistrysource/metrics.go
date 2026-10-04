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

	// Keep the observable instrument associated with this metrics instance for
	// the lifetime of the source.
	lastSuccessGauge metric.Int64ObservableGauge
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
		metric.WithInt64Callback(
			func(
				_ context.Context,
				observer metric.Int64Observer,
			) error {
				timestamp := metrics.lastSuccessTS.Load()
				if timestamp == 0 {
					return nil
				}

				observer.Observe(
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
		),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"creating %s gauge: %w",
			lastSuccessTimestampName,
			err,
		)
	}
	metrics.lastSuccessGauge = gauge

	return metrics, nil
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
