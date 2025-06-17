package metrics

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"go.opentelemetry.io/otel/attribute"
)

const (
	contentTypeHeader     = "Content-Type"
	contentEncodingHeader = "Content-Encoding"
	acceptEncodingHeader  = "Accept-Encoding"
)

// NamedCollector is a Prometheus collector that also exposes a consistent name
// used for tracing purposes.
type NamedCollector interface {
	prometheus.Collector
	MetricsName() string
}

// contextualCollector allows context injection into a wrapped collector.
type contextualCollector interface {
	prometheus.Collector
	WithContext(ctx context.Context) NamedCollector
}

// tracedCollector wraps a NamedCollector and opens a span before collecting.
type tracedCollector struct {
	ctx         context.Context
	collector   NamedCollector
	metricNames []string
}

func (tc *tracedCollector) MetricsName() string {
	return tc.collector.MetricsName()
}

// Describe forwards the Describe call to the wrapped collector.
func (tc *tracedCollector) Describe(ch chan<- *prometheus.Desc) {
	tc.collector.Describe(ch)
}

// Collect starts a tracing span, collects the metrics, then ends the span.
func (tc *tracedCollector) Collect(ch chan<- prometheus.Metric) {
	_, span := tracing.StartSpan(ctxOrBackground(tc.ctx), "flightctl/metrics", tc.collector.MetricsName())
	defer span.End()

	if len(tc.metricNames) > 20 {
		span.SetAttributes(attribute.Int("collector.metric_count", len(tc.metricNames)))
	} else {
		span.SetAttributes(attribute.StringSlice("collector.metrics", tc.metricNames))
	}

	tc.collector.Collect(ch)
}

// WithContext returns a new tracedCollector with the provided context,
// reusing the metricNames to avoid recalculating Describe.
func (tc *tracedCollector) WithContext(ctx context.Context) NamedCollector {
	return &tracedCollector{
		ctx:         ctxOrBackground(ctx),
		collector:   tc.collector,
		metricNames: tc.metricNames,
	}
}

// WrapWithTrace wraps a NamedCollector with a tracedCollector that adds span tracing
// and records metric descriptor names only once.
func WrapWithTrace(c NamedCollector) NamedCollector {
	descs := make(chan *prometheus.Desc)
	var metricNames []string
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for d := range descs {
			metricNames = append(metricNames, d.String())
		}
	}()

	c.Describe(descs)
	close(descs)
	wg.Wait()

	return &tracedCollector{
		collector:   c,
		metricNames: metricNames,
	}
}

// NewHandler returns an HTTP handler that gathers metrics from provided NamedCollectors,
// wrapping each one in tracing with the incoming request context.
func NewHandler(collectors ...NamedCollector) http.Handler {
	return http.HandlerFunc(func(rsp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		registry := prometheus.NewRegistry()
		for _, c := range collectors {
			var col prometheus.Collector = c
			if cc, ok := c.(contextualCollector); ok {
				col = cc.WithContext(ctx)
			}
			if err := registry.Register(col); err != nil {
				http.Error(rsp, fmt.Sprintf("register error: %v", err), http.StatusInternalServerError)
				return
			}
		}

		mfs, err := registry.Gather()
		if err != nil {
			http.Error(rsp, fmt.Sprintf("gather error: %v", err), http.StatusInternalServerError)
			return
		}

		contentType := expfmt.Negotiate(req.Header)
		rsp.Header().Set(contentTypeHeader, string(contentType))

		var writer io.Writer = rsp
		if gzipAccepted(req.Header) {
			rsp.Header().Set(contentEncodingHeader, "gzip")
			gz := gzip.NewWriter(rsp)
			defer gz.Close()
			writer = gz
		}

		enc := expfmt.NewEncoder(writer, contentType)
		for _, mf := range mfs {
			if err := enc.Encode(mf); err != nil {
				http.Error(rsp, fmt.Sprintf("encode error: %v", err), http.StatusInternalServerError)
				return
			}
		}

		if closer, ok := enc.(expfmt.Closer); ok {
			if err := closer.Close(); err != nil {
				http.Error(rsp, fmt.Sprintf("flush error: %v", err), http.StatusInternalServerError)
			}
		}
	})
}

// ctxOrBackground safely returns context.Background if ctx is nil
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

// gzipAccepted returns whether the client accepts gzip encoding.
func gzipAccepted(header http.Header) bool {
	for _, part := range strings.Split(header.Get(acceptEncodingHeader), ",") {
		part = strings.TrimSpace(part)
		if part == "gzip" || strings.HasPrefix(part, "gzip;") {
			return true
		}
	}
	return false
}
