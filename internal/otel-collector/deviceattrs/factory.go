package deviceattrs

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"
)

func NewFactory() processor.Factory {
	return processor.NewFactory(
		component.MustNewType("deviceattrs"),
		createDefaultConfig,
		processor.WithMetrics(createMetricsProcessor, component.StabilityLevelAlpha),
	)
}

func createDefaultConfig() component.Config {
	return nil
}

func createMetricsProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	next consumer.Metrics,
) (processor.Metrics, error) {
	p := &deviceattrs{}
	return processorhelper.NewMetrics(
		ctx, set, cfg, next, p.processMetrics, processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}),
	)
}
