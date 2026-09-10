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

	"github.com/sirupsen/logrus"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const telemetryGRPCCollectorMaxRequests = 64

// TelemetryGRPCCollector is a real OTLP/gRPC metrics receiver for deployed E2E tests.
// It uses a local TLS certificate and records bounded request history for assertions.
type TelemetryGRPCCollector struct {
	collectormetrics.UnimplementedMetricsServiceServer
	Endpoint string

	server   *grpc.Server
	listener net.Listener

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
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
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
		Endpoint: net.JoinHostPort(host, port),
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

// Stop shuts down the receiver and releases its listening socket.
func (c *TelemetryGRPCCollector) Stop(_ context.Context) error {
	if c == nil {
		return fmt.Errorf("telemetry gRPC collector is nil")
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
	})
	return c.stopErr
}

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
	return false
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
