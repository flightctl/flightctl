package auxiliary

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	jaegerImage         = "cr.jaegertracing.io/jaegertracing/jaeger:2.16.0"
	jaegerContainerName = "e2e-jaeger"
	jaegerUIPort        = "16686/tcp"
	jaegerOTLPHTTPPort  = "4318/tcp"
)

// Jaeger holds connection info and the container for the aux Jaeger.
type Jaeger struct {
	URL          string
	Host         string
	Port         string
	OTLPEndpoint string
	container    testcontainers.Container
}

// Start starts the Jaeger container and sets URL, Host, Port, OTLPEndpoint.
func (j *Jaeger) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Infof("Starting Jaeger container (reuse=%v)", reuse)
	configPath, err := createJaegerConfig()
	if err != nil {
		return fmt.Errorf("failed to create jaeger config: %w", err)
	}
	req := testcontainers.ContainerRequest{
		Image:        jaegerImage,
		Name:         jaegerContainerName,
		ExposedPorts: []string{jaegerUIPort, jaegerOTLPHTTPPort},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: configPath, ContainerFilePath: "/etc/jaeger/config.yaml", FileMode: 0644},
		},
		Cmd:        []string{"--config", "/etc/jaeger/config.yaml"},
		WaitingFor: wait.ForHTTP("/").WithPort("16686"),
		SkipReaper: reuse,
	}
	container, err := CreateContainer(ctx, req, reuse, WithNetwork(network), WithHostAccess())
	if err != nil {
		return err
	}
	j.container = container
	j.Host = GetHostIP()
	uiPort, err := container.MappedPort(ctx, jaegerUIPort)
	if err != nil {
		return fmt.Errorf("get mapped port for %s: %w", jaegerUIPort, err)
	}
	j.Port = uiPort.Port()
	j.URL = fmt.Sprintf("http://%s", net.JoinHostPort(j.Host, j.Port))
	otlpPort, err := container.MappedPort(ctx, jaegerOTLPHTTPPort)
	if err != nil {
		return fmt.Errorf("get mapped port for %s: %w", jaegerOTLPHTTPPort, err)
	}
	j.OTLPEndpoint = net.JoinHostPort(j.Host, otlpPort.Port())
	logrus.Infof("Jaeger container started: UI=%s OTLP=%s", j.URL, j.OTLPEndpoint)
	return nil
}

const jaegerConfigYAML = `extensions:
  jaeger_storage:
    backends:
      memstore:
        memory:
          max_traces: 50000
  jaeger_query:
    storage:
      traces: memstore

receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

exporters:
  jaeger_storage_exporter:
    trace_storage: memstore

service:
  extensions: [jaeger_storage, jaeger_query]
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [jaeger_storage_exporter]
  telemetry:
    logs:
      level: info
`

func createJaegerConfig() (string, error) {
	tmpPath := filepath.Join(os.TempDir(), "e2e-jaeger-config.yaml")
	return tmpPath, os.WriteFile(tmpPath, []byte(jaegerConfigYAML), 0600)
}
