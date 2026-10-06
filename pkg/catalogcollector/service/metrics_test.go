package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
)

func testLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log
}

func TestNewMetricsRuntime_DisabledWhenNilConfig(t *testing.T) {
	rt, err := newMetricsRuntime(nil, testLogger())
	require.NoError(t, err)

	// Provider must be a no-op.
	_, isNoop := rt.provider.(noop.MeterProvider)
	assert.True(t, isNoop, "disabled metrics must use noop.MeterProvider")

	// Start and Shutdown must be safe no-ops.
	serveErr, err := rt.Start()
	require.NoError(t, err)
	assert.Nil(t, serveErr)
	assert.NoError(t, rt.Shutdown(context.Background()))
}

func TestNewMetricsRuntime_EmptyEndpointDefaultsToLocalhost8888(t *testing.T) {
	// A programmatically constructed MetricsConfig with an empty Endpoint
	// (bypassing config.Parse) must resolve to localhost:8888 without
	// actually binding port 8888 — we verify the resolved endpoint field
	// without calling Start.
	rt, err := newMetricsRuntime(&config.MetricsConfig{}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, rt.Shutdown(context.Background()))
	})

	assert.Equal(t, "localhost:8888", rt.endpoint,
		"empty Endpoint must resolve to the default localhost:8888")
	assert.NotNil(t, rt.server,
		"a non-nil config must produce a real HTTP server")
}

func TestNewMetricsRuntime_BindsAndServes(t *testing.T) {
	cfg := &config.MetricsConfig{Endpoint: "127.0.0.1:0"}
	rt, err := newMetricsRuntime(cfg, testLogger())
	require.NoError(t, err)

	serveErr, err := rt.Start()
	require.NoError(t, err)
	require.NotNil(t, serveErr)
	require.NotNil(t, rt.listener)

	// Scrape the /metrics endpoint.
	addr := rt.listener.Addr().String()
	resp, err := http.Get("http://" + addr + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Shutdown must succeed.
	require.NoError(t, rt.Shutdown(context.Background()))
	require.NoError(t, <-serveErr)
}

func TestNewMetricsRuntime_InvalidEndpointReturnsError(t *testing.T) {
	// Port 0 on a non-routable address forces a listen failure.
	cfg := &config.MetricsConfig{Endpoint: "192.0.2.1:0"}
	rt, err := newMetricsRuntime(cfg, testLogger())
	require.NoError(t, err)

	_, err = rt.Start()
	require.Error(t, err)
	require.ErrorContains(t, err, "listen")
	require.NoError(t, rt.Shutdown(context.Background()))
}

func TestNewMetricsRuntime_DedicatedRegistry(t *testing.T) {
	cfg := &config.MetricsConfig{Endpoint: "127.0.0.1:0"}
	rt, err := newMetricsRuntime(cfg, testLogger())
	require.NoError(t, err)

	serveErr, err := rt.Start()
	require.NoError(t, err)

	// The /metrics endpoint should NOT contain go_info (which the default
	// registry would include). Our dedicated registry starts empty.
	addr := rt.listener.Addr().String()
	resp, err := http.Get("http://" + addr + "/metrics")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	assert.NotContains(t, string(body), "go_info",
		"dedicated registry must not include default Go collector metrics")

	require.NoError(t, rt.Shutdown(context.Background()))
	require.NoError(t, <-serveErr)
}

func TestNewMetricsRuntime_ConfiguresHTTPTimeouts(t *testing.T) {
	rt, err := newMetricsRuntime(
		&config.MetricsConfig{Endpoint: "127.0.0.1:0"},
		testLogger(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, rt.Shutdown(context.Background()))
	})

	require.NotNil(t, rt.server)
	assert.Equal(t, metricsReadHeaderTimeout, rt.server.ReadHeaderTimeout)
	assert.Equal(t, metricsReadTimeout, rt.server.ReadTimeout)
	assert.Equal(t, metricsWriteTimeout, rt.server.WriteTimeout)
	assert.Equal(t, metricsIdleTimeout, rt.server.IdleTimeout)
}

func TestMetricsRuntime_ShutdownIgnoresCallerCancellation(t *testing.T) {
	rt, err := newMetricsRuntime(
		&config.MetricsConfig{Endpoint: "127.0.0.1:0"},
		testLogger(),
	)
	require.NoError(t, err)

	serveErr, err := rt.Start()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, rt.Shutdown(ctx))
	require.NoError(t, <-serveErr)

	// Shutdown is idempotent.
	require.NoError(t, rt.Shutdown(context.Background()))
}

func TestMetricsRuntime_ShutdownBeforeStart(t *testing.T) {
	rt, err := newMetricsRuntime(
		&config.MetricsConfig{Endpoint: "127.0.0.1:0"},
		testLogger(),
	)
	require.NoError(t, err)

	require.NoError(t, rt.Shutdown(context.Background()))
	_, err = rt.Start()
	require.ErrorContains(t, err, "already stopped")
}

func TestMetricsRuntime_ExposesRecordedOTelMetrics(t *testing.T) {
	rt, err := newMetricsRuntime(
		&config.MetricsConfig{Endpoint: "127.0.0.1:0"},
		testLogger(),
	)
	require.NoError(t, err)

	instruments, err := newSourceInstruments(rt.provider)
	require.NoError(t, err)
	consumer := &instrumentedFanoutConsumer{
		inner:       &recordingConsumer{},
		sourceID:    "http/dev-input",
		instruments: instruments,
	}
	require.NoError(t, consumer.Consume(context.Background(), testSnapshot()))

	serveErr, err := rt.Start()
	require.NoError(t, err)

	resp, err := http.Get("http://" + rt.listener.Addr().String() + "/metrics")
	require.NoError(t, err)
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, readErr)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	output := string(body)
	assert.Contains(
		t,
		output,
		"# TYPE flightctl_catalogcollector_source_snapshots_total counter",
	)
	assert.Contains(t, output, "flightctl_catalogcollector_source_snapshots_total{")
	assert.Contains(
		t,
		output,
		"# TYPE flightctl_catalogcollector_source_snapshot_resources histogram",
	)
	assert.Contains(t, output, `source_id="http/dev-input"`)
	assert.Contains(t, output, `resource_kind="Catalog"`)
	assert.Contains(t, output, `resource_kind="CatalogItem"`)
	assert.True(
		t,
		strings.Contains(output, "} 1\n") || strings.Contains(output, "} 1.0\n"),
		"expected the source snapshot counter to expose value 1",
	)

	require.NoError(t, rt.Shutdown(context.Background()))
	require.NoError(t, <-serveErr)
}
