package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instrument name constants follow the OpenTelemetry semantic convention of
// dotted, lowercase, hierarchy. Units use UCUM annotations.
const (
	meterName = "flightctl.catalogcollector"

	sourceSnapshotsName  = "flightctl.catalogcollector.source.snapshots"
	sourceResourcesName  = "flightctl.catalogcollector.source.snapshot.resources"
	pipelineSyncsName    = "flightctl.catalogcollector.pipeline.syncs"
	pipelineDurationName = "flightctl.catalogcollector.pipeline.sync.duration"
	pipelineResourceName = "flightctl.catalogcollector.pipeline.snapshot.resources"
)

// Attribute key constants. Only bounded, low-cardinality values are used as
// metric attributes. Resource names, revisions, URLs, and error messages are
// never recorded as attributes.
var (
	attrSourceID      = attribute.Key("source.id")
	attrResourceKind  = attribute.Key("resource.kind")
	attrPipelineID    = attribute.Key("pipeline.id")
	attrDestinationID = attribute.Key("destination.id")
	attrOutcome       = attribute.Key("outcome")
)

// outcomeAttr classifies an error for metric attribution.
//
//   - nil → "success"
//   - context.Canceled or context.DeadlineExceeded → "cancelled"
//   - anything else → "failure"
func outcomeAttr(err error) attribute.KeyValue {
	if err == nil {
		return attrOutcome.String("success")
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return attrOutcome.String("cancelled")
	}
	return attrOutcome.String("failure")
}

// sourceInstruments holds the pre-created instruments for source-boundary
// metrics.
type sourceInstruments struct {
	snapshots metric.Int64Counter
	resources metric.Int64Histogram
}

func newSourceInstruments(mp metric.MeterProvider) (*sourceInstruments, error) {
	meter := mp.Meter(meterName)

	snapshots, err := meter.Int64Counter(
		sourceSnapshotsName,
		metric.WithDescription("Number of snapshots received from a source"),
		metric.WithUnit("{snapshot}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s counter: %w", sourceSnapshotsName, err)
	}

	resources, err := meter.Int64Histogram(
		sourceResourcesName,
		metric.WithDescription("Number of resources per snapshot received from a source"),
		metric.WithUnit("{resource}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", sourceResourcesName, err)
	}

	return &sourceInstruments{
		snapshots: snapshots,
		resources: resources,
	}, nil
}

// instrumentedFanoutConsumer wraps a fanoutConsumer and records source-boundary
// metrics on every Consume call.
type instrumentedFanoutConsumer struct {
	inner       catalogcollector.Consumer
	sourceID    string
	instruments *sourceInstruments
}

func (c *instrumentedFanoutConsumer) Consume(
	ctx context.Context,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	if snapshot == nil {
		return fmt.Errorf("source emitted nil snapshot")
	}

	sourceAttr := attrSourceID.String(c.sourceID)

	c.instruments.snapshots.Add(ctx, 1,
		metric.WithAttributes(sourceAttr),
	)

	c.instruments.resources.Record(ctx,
		int64(len(snapshot.Catalogs)),
		metric.WithAttributes(
			sourceAttr,
			attrResourceKind.String("Catalog"),
		),
	)
	c.instruments.resources.Record(ctx,
		int64(len(snapshot.CatalogItems)),
		metric.WithAttributes(
			sourceAttr,
			attrResourceKind.String("CatalogItem"),
		),
	)

	return c.inner.Consume(ctx, snapshot)
}

// pipelineInstruments holds the pre-created instruments for pipeline-boundary
// metrics.
type pipelineInstruments struct {
	syncs     metric.Int64Counter
	duration  metric.Float64Histogram
	resources metric.Int64Histogram
}

func newPipelineInstruments(mp metric.MeterProvider) (*pipelineInstruments, error) {
	meter := mp.Meter(meterName)

	syncs, err := meter.Int64Counter(
		pipelineSyncsName,
		metric.WithDescription("Number of pipeline sync attempts"),
		metric.WithUnit("{sync}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s counter: %w", pipelineSyncsName, err)
	}

	duration, err := meter.Float64Histogram(
		pipelineDurationName,
		metric.WithDescription("Duration of a pipeline sync from first processor to destination"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", pipelineDurationName, err)
	}

	resources, err := meter.Int64Histogram(
		pipelineResourceName,
		metric.WithDescription("Number of resources per snapshot entering a pipeline before processors"),
		metric.WithUnit("{resource}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating %s histogram: %w", pipelineResourceName, err)
	}

	return &pipelineInstruments{
		syncs:     syncs,
		duration:  duration,
		resources: resources,
	}, nil
}

// instrumentedBranch wraps a pipeline branch consumer and records
// pipeline-boundary metrics on every Consume call.
type instrumentedBranch struct {
	inner         catalogcollector.Consumer
	pipelineID    string
	destinationID string
	instruments   *pipelineInstruments
}

func (b *instrumentedBranch) Consume(
	ctx context.Context,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	start := time.Now()
	err := b.inner.Consume(ctx, snapshot)
	elapsed := time.Since(start)

	attrs := metric.WithAttributes(
		attrPipelineID.String(b.pipelineID),
		attrDestinationID.String(b.destinationID),
		outcomeAttr(err),
	)

	b.instruments.syncs.Add(ctx, 1, attrs)
	b.instruments.duration.Record(ctx, elapsed.Seconds(), attrs)

	b.instruments.resources.Record(ctx,
		int64(len(snapshot.Catalogs)),
		metric.WithAttributes(
			attrPipelineID.String(b.pipelineID),
			attrDestinationID.String(b.destinationID),
			outcomeAttr(err),
			attrResourceKind.String("Catalog"),
		),
	)
	b.instruments.resources.Record(ctx,
		int64(len(snapshot.CatalogItems)),
		metric.WithAttributes(
			attrPipelineID.String(b.pipelineID),
			attrDestinationID.String(b.destinationID),
			outcomeAttr(err),
			attrResourceKind.String("CatalogItem"),
		),
	)

	return err
}
