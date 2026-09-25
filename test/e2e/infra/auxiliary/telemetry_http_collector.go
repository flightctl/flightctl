package auxiliary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	telemetryHTTPCollectorImage          = "registry.access.redhat.com/ubi9/python-312:latest"
	telemetryHTTPCollectorImageEnv       = "E2E_TELEMETRY_COLLECTOR_IMAGE"
	telemetryHTTPCollectorContainerName  = "e2e-telemetry-http-collector"
	telemetryHTTPCollectorPort           = "4318/tcp"
	telemetryHTTPCollectorStartupTimeout = 5 * time.Minute
	telemetryHTTPCollectorMaxLogBytes    = 64 * 1024
	telemetryHTTPCollectorCleanupTimeout = 30 * time.Second
)

// TelemetryHTTPCollector is a small OTLP/HTTP test receiver that records request
// paths, payload size, header hashes, and body marker matches.
type TelemetryHTTPCollector struct {
	URL       string
	Endpoint  string
	Host      string
	Port      string
	container testcontainers.Container
}

// StartTelemetryHTTPCollector removes a stale collector and starts a receiver
// using the environment's runtime and network, within the supplied timeout.
func StartTelemetryHTTPCollector(ctx context.Context, timeout time.Duration) (*TelemetryHTTPCollector, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("telemetry HTTP collector startup timeout must be positive")
	}
	if ctx == nil {
		return nil, fmt.Errorf("telemetry HTTP collector context is nil")
	}
	ioCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ioCtx.Err(); err != nil {
		return nil, err
	}
	if err := StopServices([]Service{ServiceTelemetryHTTPCollector}); err != nil {
		logrus.Warnf("Best-effort telemetry HTTP collector cleanup before start failed: %v", err)
	}
	services, err := StartServices(ioCtx, []Service{ServiceTelemetryHTTPCollector})
	if err != nil {
		return nil, err
	}
	if services == nil || services.TelemetryHTTPCollector == nil {
		return nil, fmt.Errorf("telemetry HTTP collector service was not started")
	}
	return services.TelemetryHTTPCollector, nil
}

// Start starts the OTLP/HTTP collector container and sets the host-reachable
// endpoint used by Flight Control services.
func (c *TelemetryHTTPCollector) Start(ctx context.Context, network string, reuse bool) error {
	if c == nil {
		return fmt.Errorf("telemetry HTTP collector is nil")
	}
	if ctx == nil {
		return fmt.Errorf("telemetry HTTP collector context is nil")
	}
	logrus.Infof("Starting telemetry HTTP collector container (reuse=%v)", reuse)
	req := testcontainers.ContainerRequest{
		Image:        telemetryCollectorImage(),
		Name:         telemetryHTTPCollectorContainerNameForProcess(),
		ExposedPorts: []string{telemetryHTTPCollectorPort},
		Cmd:          []string{"/bin/bash", "-c", telemetryHTTPCollectorScript},
		WaitingFor: wait.ForHTTP("/ready").
			WithPort(telemetryHTTPCollectorPort).
			WithForcedIPv4LocalHost().
			WithStartupTimeout(telemetryHTTPCollectorStartupTimeout),
		SkipReaper: reuse,
	}
	opts := []ContainerRequestOption{WithHostAccess()}
	if network != "" && network != "host" {
		opts = append(opts, WithNetwork(network))
	}
	container, err := CreateContainer(ctx, req, reuse, opts...)
	if err != nil {
		return telemetryHTTPCollectorStartupError(ctx, container, err)
	}
	c.container = container
	c.Host = GetHostIP()
	port, err := container.MappedPort(ctx, telemetryHTTPCollectorPort)
	if err != nil {
		return fmt.Errorf("get mapped telemetry HTTP collector port: %w", err)
	}
	c.Port = port.Port()
	c.URL = fmt.Sprintf("http://%s", net.JoinHostPort(c.Host, c.Port))
	c.Endpoint = c.URL + "/v1/metrics"
	logrus.Infof("Telemetry HTTP collector started: %s", c.Endpoint)
	return nil
}

// telemetryHTTPCollectorStartupError cleans up a failed collector start while
// preserving the original startup error.
func telemetryHTTPCollectorStartupError(ctx context.Context, container testcontainers.Container, startErr error) error {
	if startErr == nil {
		startErr = errors.New("telemetry HTTP collector startup failed")
	}
	err := fmt.Errorf("start telemetry HTTP collector container: %w", startErr)
	if container == nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryHTTPCollectorCleanupTimeout)
	defer cancel()
	if removeErr := container.Terminate(cleanupCtx); removeErr != nil {
		return errors.Join(err, fmt.Errorf("remove failed telemetry collector: %w", removeErr))
	}
	return err
}

// Logs returns the current collector stdout/stderr output.
func (c *TelemetryHTTPCollector) Logs(ctx context.Context) (logs string, err error) {
	if c == nil || c.container == nil {
		return "", fmt.Errorf("telemetry HTTP collector is not started")
	}
	reader, err := c.container.Logs(ctx)
	if err != nil {
		return "", fmt.Errorf("read telemetry HTTP collector logs: %w", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close telemetry HTTP collector log stream: %w", closeErr)
		}
	}()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read telemetry HTTP collector log stream: %w", err)
	}
	return tailString(data, telemetryHTTPCollectorMaxLogBytes), nil
}

// HeaderSHA256 returns the collector log value for a received header without
// exposing the raw header value.
func HeaderSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// tailString returns at most maxBytes from the end of data.
func tailString(data []byte, maxBytes int) string {
	if maxBytes <= 0 || len(data) <= maxBytes {
		return string(data)
	}
	return string(data[len(data)-maxBytes:])
}

// telemetryCollectorImage returns the configured collector image, falling back
// to the public image for local runs that do not provide a mirror.
func telemetryCollectorImage() string {
	if image := strings.TrimSpace(os.Getenv(telemetryHTTPCollectorImageEnv)); image != "" {
		return image
	}
	return telemetryHTTPCollectorImage
}

const telemetryHTTPCollectorScript = `cat >/tmp/server.py <<'PY'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import hashlib
import gzip
from urllib.parse import urlsplit

def header_sha256(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest() if value else ""

class H(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return

    def do_GET(self):
        request_path = urlsplit(self.path).path
        if request_path == "/ready":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        size = int(self.headers.get("content-length", "0"))
        raw = self.rfile.read(size)
        body = gzip.decompress(raw) if self.headers.get("Content-Encoding", "") == "gzip" else raw
        authorization = self.headers.get("Authorization", "")
        custom_header = self.headers.get("X-Custom-Header", "")
        body_marker = self.headers.get("X-Expected-Body-Substring", "")
        print("OTLP_HTTP_PATH=" + urlsplit(self.path).path, flush=True)
        print("AUTHORIZATION_PRESENT=" + str(bool(authorization)).lower(), flush=True)
        print("AUTHORIZATION_SHA256=" + header_sha256(authorization), flush=True)
        print("CUSTOM_HEADER_SHA256=" + header_sha256(custom_header), flush=True)
        print("EXPECTED_BODY_MATCH=" + str(bool(body_marker) and body_marker.encode("utf-8") in body).lower(), flush=True)
        self.send_response(200)
        self.send_header("Content-Type", "application/x-protobuf")
        self.end_headers()
        self.wfile.write(b"")

# Idle or incomplete connections must not block readiness or other senders.
with ThreadingHTTPServer(("0.0.0.0", 4318), H) as server:
    server.serve_forever()
PY
python /tmp/server.py`
