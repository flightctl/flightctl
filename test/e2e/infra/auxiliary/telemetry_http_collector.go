package auxiliary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	telemetryHTTPCollectorImage          = "registry.access.redhat.com/ubi9/python-312:latest"
	telemetryHTTPCollectorContainerName  = "e2e-telemetry-http-collector"
	telemetryHTTPCollectorPort           = "4318/tcp"
	telemetryHTTPCollectorPortNum        = "4318"
	telemetryHTTPCollectorMaxLogBytes    = 64 * 1024
	telemetryHTTPCollectorCleanupTimeout = 30 * time.Second
	telemetryHTTPCollectorProbeTimeout   = 5 * time.Second
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
	logrus.Infof("Starting telemetry HTTP collector container (reuse=%v)", reuse)
	req := testcontainers.ContainerRequest{
		Image:        telemetryHTTPCollectorImage,
		Name:         telemetryHTTPCollectorContainerName,
		ExposedPorts: []string{telemetryHTTPCollectorPort},
		Cmd:          []string{"/bin/bash", "-c", telemetryHTTPCollectorScript},
		WaitingFor: wait.ForHTTP("/ready").
			WithPort(telemetryHTTPCollectorPort).
			WithForcedIPv4LocalHost().
			WithStartupTimeout(5 * time.Minute),
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

// telemetryHTTPCollectorStartupError captures bounded diagnostics before removing
// a failed collector. Startup may have exhausted the caller's context already.
func telemetryHTTPCollectorStartupError(ctx context.Context, container testcontainers.Container, startErr error) error {
	err := fmt.Errorf("start telemetry HTTP collector container: %w", startErr)
	if container == nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryHTTPCollectorCleanupTimeout)
	defer cancel()
	state, stateErr := container.State(cleanupCtx)
	if stateErr != nil {
		err = errors.Join(err, fmt.Errorf("inspect failed collector: %w", stateErr))
	} else if state != nil {
		logrus.Infof("Failed telemetry collector: status=%s exitCode=%d", state.Status, state.ExitCode)
	}
	logTelemetryHTTPCollectorConnectivity(cleanupCtx, container)
	reader, logsErr := container.Logs(cleanupCtx)
	if logsErr == nil {
		data, readErr := io.ReadAll(io.LimitReader(reader, telemetryHTTPCollectorMaxLogBytes))
		closeErr := reader.Close()
		logrus.Infof("Failed telemetry collector startup output: %s", data)
		logsErr = errors.Join(readErr, closeErr)
	}
	if logsErr != nil {
		err = errors.Join(err, fmt.Errorf("read failed collector startup output: %w", logsErr))
	}
	// Give removal its own budget even if diagnostic retrieval timed out.
	removeCtx, removeCancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryHTTPCollectorCleanupTimeout)
	defer removeCancel()
	if removeErr := container.Terminate(removeCtx); removeErr != nil {
		err = errors.Join(err, fmt.Errorf("remove failed telemetry collector: %w", removeErr))
	}
	return err
}

// logTelemetryHTTPCollectorConnectivity exposes the address and transport error
// hidden by the HTTP wait strategy, without logging proxy URLs or response bodies.
func logTelemetryHTTPCollectorConnectivity(ctx context.Context, container testcontainers.Container) {
	host, hostErr := container.Host(ctx)
	port, portErr := container.MappedPort(ctx, telemetryHTTPCollectorPort)
	if hostErr != nil || portErr != nil {
		logrus.Warnf("Collector probe address unavailable: host=%v port=%v", hostErr, portErr)
		return
	}
	if inspect, err := container.Inspect(ctx); err != nil {
		logrus.Warnf("Collector port inspection failed: %v", err)
	} else if inspect.NetworkSettings != nil {
		logrus.Infof("Collector published ports: %v", inspect.NetworkSettings.Ports)
	}
	// Match the wait strategy's workaround for broken IPv6 localhost forwarding.
	if host == "localhost" {
		host = "127.0.0.1"
	}
	probeURL := "http://" + net.JoinHostPort(host, port.Port()) + "/ready"
	logrus.Infof("Collector readiness address: %s; forwarding host: %s", probeURL, GetHostIP())
	// Compare the waiter's environment-proxy route with a direct connection.
	for _, useProxy := range []bool{true, false} {
		transport := &http.Transport{}
		if useProxy {
			transport.Proxy = http.ProxyFromEnvironment
		}
		client := &http.Client{Transport: transport, Timeout: telemetryHTTPCollectorProbeTimeout}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		if err != nil {
			logrus.Warnf("Collector probe request failed: %v", err)
			transport.CloseIdleConnections()
			return
		}
		response, err := client.Do(req)
		if err != nil {
			logrus.Warnf("Collector readiness probe (environmentProxy=%t): %v", useProxy, err)
		} else {
			logrus.Infof("Collector readiness probe (environmentProxy=%t): HTTP %d", useProxy, response.StatusCode)
			if err := response.Body.Close(); err != nil {
				logrus.Warnf("Close collector readiness response: %v", err)
			}
		}
		transport.CloseIdleConnections()
	}
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

// LogsContain reports whether every substring is present in the collector logs.
func (c *TelemetryHTTPCollector) LogsContain(ctx context.Context, substrings ...string) (bool, string, error) {
	logs, err := c.Logs(ctx)
	if err != nil {
		return false, "", err
	}
	for _, substring := range substrings {
		if !strings.Contains(logs, substring) {
			return false, logs, nil
		}
	}
	return true, logs, nil
}

// HeaderSHA256 returns the collector log value for a received header without
// exposing the raw header value.
func HeaderSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func tailString(data []byte, maxBytes int) string {
	if maxBytes <= 0 || len(data) <= maxBytes {
		return string(data)
	}
	return string(data[len(data)-maxBytes:])
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
            if not self.server.ready_seen:
                print("OTLP_HTTP_READY_REQUEST=true", flush=True)
                self.server.ready_seen = True
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
        print("BODY_BYTES=" + str(len(body)), flush=True)
        self.send_response(200)
        self.send_header("Content-Type", "application/x-protobuf")
        self.end_headers()
        self.wfile.write(b"")

# Idle or incomplete connections must not block readiness or other senders.
with ThreadingHTTPServer(("0.0.0.0", 4318), H) as server:
    server.ready_seen = False
    print("OTLP_HTTP_LISTENING=0.0.0.0:4318", flush=True)
    server.serve_forever()
PY
python /tmp/server.py`
