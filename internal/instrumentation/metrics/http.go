package metrics

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
)

// HTTPMetricsCollector implements NamedCollector and integrates OpenTelemetry HTTP metrics
// with the existing Prometheus metrics infrastructure.
type HTTPMetricsCollector struct {
	exporter      *otelprometheus.Exporter
	registry      *prometheus.Registry
	meterProvider *metric.MeterProvider
	log           logrus.FieldLogger
	ctx           context.Context
	cancel        context.CancelFunc
	serviceName   string
}

// NewHTTPMetricsCollector creates a new HTTPMetricsCollector that integrates OpenTelemetry
// HTTP metrics with Prometheus. It initializes the OpenTelemetry meter provider and
// creates a Prometheus exporter for metrics collection.
func NewHTTPMetricsCollector(ctx context.Context, _ *config.Config, serviceName string, log logrus.FieldLogger) *HTTPMetricsCollector {
	c, cancel := context.WithCancel(ctx)

	// Create a dedicated registry for OpenTelemetry metrics
	registry := prometheus.NewRegistry()

	// Create Prometheus exporter
	exporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registry),
		otelprometheus.WithoutScopeInfo(),
	)
	if err != nil {
		log.WithError(err).Error("Failed to create Prometheus exporter for OpenTelemetry HTTP metrics")
		cancel()
		return nil
	}

	// Create resource with service information
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(serviceName),
	)

	// Create meter provider with the Prometheus exporter
	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(exporter),
	)

	// Only set global meter provider if not already set
	if _, ok := otel.GetMeterProvider().(*metric.MeterProvider); !ok {
		otel.SetMeterProvider(mp)
	} else {
		log.Warn("Global meter provider already set, using existing provider")
	}

	collector := &HTTPMetricsCollector{
		exporter:      exporter,
		registry:      registry,
		meterProvider: mp,
		log:           log,
		ctx:           c,
		cancel:        cancel,
		serviceName:   serviceName,
	}

	log.Info("OpenTelemetry HTTP metrics collector initialized")
	return collector
}

// Describe forwards the Describe call to the OpenTelemetry Prometheus registry
func (c *HTTPMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	c.registry.Describe(ch)
}

// Collect forwards the Collect call to the OpenTelemetry Prometheus registry
func (c *HTTPMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	c.registry.Collect(ch)
}

// Shutdown gracefully shuts down the OpenTelemetry meter provider
func (c *HTTPMetricsCollector) Shutdown() error {
	defer c.cancel()

	if c.meterProvider != nil {
		if err := c.meterProvider.Shutdown(c.ctx); err != nil {
			c.log.WithError(err).Error("Failed to shutdown OpenTelemetry HTTP metrics")
			return err
		}
		c.log.Info("OpenTelemetry HTTP metrics shutdown successfully")
	}
	return nil
}
