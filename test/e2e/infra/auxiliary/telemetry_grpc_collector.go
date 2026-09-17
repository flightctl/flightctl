package auxiliary

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	telemetryGRPCCollectorListenHost   = "0.0.0.0"
	telemetryGRPCCollectorMaxRequests  = 64
	telemetryGRPCCollectorProxyImage   = telemetryHTTPCollectorImage
	telemetryGRPCCollectorProxyPort    = "4317/tcp"
	telemetryGRPCCollectorProxyPortNum = "4317"
	telemetryGRPCCollectorProxyName    = "e2e-telemetry-grpc-proxy"
	telemetryCollectorKindNetwork      = "kind"
)

// TelemetryGRPCCollector is a real OTLP/gRPC metrics receiver for deployed E2E tests.
// It uses a local TLS certificate and records bounded request history for assertions.
type TelemetryGRPCCollector struct {
	collectormetrics.UnimplementedMetricsServiceServer
	Endpoint string

	server   *grpc.Server
	listener net.Listener
	proxy    testcontainers.Container

	mu       sync.RWMutex
	requests []*collectormetrics.ExportMetricsServiceRequest
	stopOnce sync.Once
	stopErr  error
}

// StartTelemetryGRPCCollector starts a TLS-enabled OTLP/gRPC metrics receiver.
// The endpoint is returned as a bare host:port value for gRPC transport selection.
func StartTelemetryGRPCCollector(ctx context.Context, timeout time.Duration) (*TelemetryGRPCCollector, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("telemetry gRPC collector startup timeout must be positive")
	}
	if ctx == nil {
		return nil, fmt.Errorf("telemetry gRPC collector context is nil")
	}
	startCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := startCtx.Err(); err != nil {
		return nil, err
	}

	host := strings.TrimSpace(GetHostIP())
	if host == "" {
		return nil, fmt.Errorf("telemetry gRPC collector host is empty")
	}
	// The gateway runs in a Kubernetes network namespace and may reach the
	// test process through a different host interface than E2E_AUX_HOST.
	// Bind the test-only receiver broadly and advertise only the configured
	// auxiliary host address in the endpoint.
	listener, err := net.Listen("tcp", net.JoinHostPort(telemetryGRPCCollectorListenHost, "0")) //nolint:gosec // G102: the E2E receiver must accept connections from the cluster network.
	if err != nil {
		return nil, fmt.Errorf("listen for telemetry gRPC collector: %w", err)
	}
	tlsCert, err := telemetryGRPCServerCertificate()
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("create telemetry gRPC collector certificate: %w", err)
	}

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("get telemetry gRPC collector port: %w", err)
	}
	collector := &TelemetryGRPCCollector{
		listener: listener,
	}
	collector.server = grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{tlsCert},
	})))
	collectormetrics.RegisterMetricsServiceServer(collector.server, collector)
	go func() {
		if err := collector.server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
			logrus.Warnf("telemetry gRPC collector stopped: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		_ = collector.Stop(context.Background())
	}()
	if network := GetDockerNetwork(); network != "host" {
		proxy, endpoint, err := startTelemetryGRPCProxy(startCtx, network, port)
		if err != nil {
			_ = collector.Stop(context.Background())
			return nil, fmt.Errorf("start telemetry gRPC proxy: %w", err)
		}
		collector.proxy = proxy
		collector.Endpoint = endpoint
	} else {
		collector.Endpoint = net.JoinHostPort(host, port)
	}
	logrus.Infof("Telemetry gRPC collector started: %s", collector.Endpoint)
	return collector, nil
}

// Export records an OTLP metrics request for bounded, non-sensitive assertions.
func (c *TelemetryGRPCCollector) Export(_ context.Context, request *collectormetrics.ExportMetricsServiceRequest) (*collectormetrics.ExportMetricsServiceResponse, error) {
	if c == nil || request == nil {
		return nil, fmt.Errorf("telemetry gRPC collector received a nil request")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == telemetryGRPCCollectorMaxRequests {
		copy(c.requests, c.requests[1:])
		c.requests = c.requests[:len(c.requests)-1]
	}
	c.requests = append(c.requests, request)
	return &collectormetrics.ExportMetricsServiceResponse{}, nil
}

// MetricsContain reports whether a received metric request contains the value.
// It checks metric names and string resource/data-point attributes only.
func (c *TelemetryGRPCCollector) MetricsContain(ctx context.Context, value string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("telemetry gRPC collector is nil")
	}
	if strings.TrimSpace(value) == "" {
		return false, fmt.Errorf("telemetry gRPC collector search value is empty")
	}
	if ctx == nil {
		return false, fmt.Errorf("telemetry gRPC collector context is nil")
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, request := range c.requests {
		if metricsRequestContains(request, value) {
			return true, nil
		}
	}
	return false, nil
}

// RequestCount returns the number of retained export requests.
func (c *TelemetryGRPCCollector) RequestCount() (int, error) {
	if c == nil {
		return 0, fmt.Errorf("telemetry gRPC collector is nil")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.requests), nil
}

// Stop shuts down the receiver and releases its listening socket.
func (c *TelemetryGRPCCollector) Stop(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("telemetry gRPC collector is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.stopOnce.Do(func() {
		if c.server != nil {
			c.server.Stop()
		}
		if c.listener != nil {
			if err := c.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				c.stopErr = err
			}
		}
		if c.proxy != nil {
			if err := c.proxy.Terminate(ctx); err != nil {
				c.stopErr = errors.Join(c.stopErr, fmt.Errorf("terminate telemetry gRPC proxy: %w", err))
			}
		}
	})
	return c.stopErr
}

func startTelemetryGRPCProxy(ctx context.Context, network, targetPort string) (testcontainers.Container, string, error) {
	proxyName := telemetryCollectorContainerNameForProcess(telemetryGRPCCollectorProxyName)
	if containers.ContainerExistsByName(proxyName) {
		if err := containers.RemoveContainerByName(proxyName); err != nil {
			return nil, "", fmt.Errorf("remove stale telemetry gRPC proxy %s: %w", proxyName, err)
		}
	}

	hostGateway := "host.docker.internal"
	if GetProviderType() == testcontainers.ProviderPodman {
		hostGateway = "host.containers.internal"
	}
	targetHosts := hostGateway + "," + GetHostIP()
	req := testcontainers.ContainerRequest{
		Image:        telemetryGRPCCollectorProxyImage,
		Name:         proxyName,
		ExposedPorts: []string{telemetryGRPCCollectorProxyPort},
		Env: map[string]string{
			"TARGET_HOSTS": targetHosts,
			"TARGET_PORT":  targetPort,
		},
		Cmd:        []string{"/bin/bash", "-c", telemetryGRPCProxyScript},
		WaitingFor: wait.ForListeningPort(telemetryGRPCCollectorProxyPort),
	}
	withHostGateway := func(request *testcontainers.ContainerRequest) {
		old := request.HostConfigModifier
		request.HostConfigModifier = func(hostConfig *dockercontainer.HostConfig) {
			if old != nil {
				old(hostConfig)
			}
			hostConfig.ExtraHosts = append(hostConfig.ExtraHosts, hostGateway+":host-gateway")
		}
	}
	proxy, err := CreateContainer(ctx, req, false, WithNetwork(network), withHostGateway)
	if err != nil {
		return nil, "", err
	}
	if network == telemetryCollectorKindNetwork {
		host, err := proxy.ContainerIP(ctx)
		if err != nil {
			terminateErr := proxy.Terminate(context.Background())
			if terminateErr != nil {
				return nil, "", errors.Join(
					fmt.Errorf("get telemetry gRPC proxy container IP: %w", err),
					fmt.Errorf("terminate telemetry gRPC proxy after IP lookup failure: %w", terminateErr),
				)
			}
			return nil, "", fmt.Errorf("get telemetry gRPC proxy container IP: %w", err)
		}
		endpoint := net.JoinHostPort(host, telemetryGRPCCollectorProxyPortNum)
		logrus.Infof("Telemetry gRPC collector proxy started: %s", endpoint)
		return proxy, endpoint, nil
	}
	port, err := proxy.MappedPort(ctx, telemetryGRPCCollectorProxyPort)
	if err != nil {
		terminateErr := proxy.Terminate(context.Background())
		if terminateErr != nil {
			return nil, "", errors.Join(
				fmt.Errorf("get mapped telemetry gRPC proxy port: %w", err),
				fmt.Errorf("terminate telemetry gRPC proxy after port lookup failure: %w", terminateErr),
			)
		}
		return nil, "", fmt.Errorf("get mapped telemetry gRPC proxy port: %w", err)
	}
	endpoint := net.JoinHostPort(GetHostIP(), port.Port())
	logrus.Infof("Telemetry gRPC collector proxy started: %s", endpoint)
	return proxy, endpoint, nil
}

const telemetryGRPCProxyScript = `cat >/tmp/proxy.py <<'PY'
import os
import socket
import socketserver
import threading

targets = [(host, int(os.environ["TARGET_PORT"])) for host in os.environ["TARGET_HOSTS"].split(",") if host]

def connect_upstream():
    last_error = None
    for target in targets:
        try:
            return socket.create_connection(target, timeout=10)
        except OSError as error:
            last_error = error
    raise last_error

def forward(source, destination):
    try:
        while True:
            data = source.recv(65536)
            if not data:
                break
            destination.sendall(data)
    finally:
        try:
            destination.shutdown(socket.SHUT_WR)
        except OSError:
            pass

class Proxy(socketserver.BaseRequestHandler):
    def handle(self):
        print("OTLP_GRPC_PROXY_CONNECTION=true", flush=True)
        try:
            upstream = connect_upstream()
        except OSError:
            print("OTLP_GRPC_PROXY_UPSTREAM_CONNECTED=false", flush=True)
            return
        print("OTLP_GRPC_PROXY_UPSTREAM_CONNECTED=true", flush=True)
        threads = [
            threading.Thread(target=forward, args=(self.request, upstream), daemon=True),
            threading.Thread(target=forward, args=(upstream, self.request), daemon=True),
        ]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        upstream.close()

class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

with Server(("0.0.0.0", 4317), Proxy) as server:
    print("OTLP_GRPC_PROXY_LISTENING=0.0.0.0:4317", flush=True)
    server.serve_forever()
PY
python /tmp/proxy.py`

func metricsRequestContains(request *collectormetrics.ExportMetricsServiceRequest, value string) bool {
	if request == nil {
		return false
	}
	for _, resourceMetrics := range request.ResourceMetrics {
		if resourceMetrics == nil {
			continue
		}
		if resourceMetrics.Resource != nil {
			for _, attribute := range resourceMetrics.Resource.Attributes {
				if attribute != nil && attribute.Value != nil && strings.Contains(attribute.Value.GetStringValue(), value) {
					return true
				}
			}
		}
		for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
			if scopeMetrics == nil {
				continue
			}
			for _, metric := range scopeMetrics.Metrics {
				if metric != nil && strings.Contains(metric.Name, value) {
					return true
				}
			}
		}
	}
	// Resource attributes are expected to contain device_id, but checking the
	// complete request also covers data-point attributes added by processors.
	return strings.Contains(request.String(), value)
}

func telemetryGRPCServerCertificate() (tls.Certificate, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: "telemetry-grpc-collector"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
