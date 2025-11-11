package tracing

import (
	"context"
	"net/http"

	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// RunMetricsServer starts the metrics server and wraps /metrics with OTEL HTTP middleware
// so every scrape produces a server span named "metrics-http-server".
func RunMetricsServer(
	ctx context.Context,
	log logrus.FieldLogger,
	addr string,
	collectors ...prometheus.Collector,
) error {
	s := metrics.NewMetricsServer(log, collectors...)

	// wrap /metrics with otelhttp to get a span per scrape
	return s.Run(
		ctx,
		metrics.WithListenAddr(addr),
		metrics.WithHandlerWrapper(func(h http.Handler) http.Handler {
			return otelhttp.NewHandler(h, "metrics-http-server", otelhttp.WithPublicEndpoint())
		}),
	)
}
