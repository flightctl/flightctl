package satellite

import (
	"context"
	"fmt"
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

func (s *Services) startJaeger(ctx context.Context) error {
	logrus.Info("Starting jaeger container (always reused)")
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
		SkipReaper: true,
	}
	container, err := CreateContainer(ctx, req, true, WithNetwork(s.network), WithHostAccess())
	if err != nil {
		return err
	}
	s.jaeger = container
	s.JaegerHost = GetHostIP()
	uiPort, err := container.MappedPort(ctx, jaegerUIPort)
	if err != nil {
		return fmt.Errorf("get mapped port for %s: %w", jaegerUIPort, err)
	}
	s.JaegerPort = uiPort.Port()
	s.JaegerURL = fmt.Sprintf("http://%s:%s", s.JaegerHost, s.JaegerPort)
	otlpPort, err := container.MappedPort(ctx, jaegerOTLPHTTPPort)
	if err != nil {
		return fmt.Errorf("get mapped port for %s: %w", jaegerOTLPHTTPPort, err)
	}
	s.JaegerOTLPEndpoint = fmt.Sprintf("%s:%s", s.JaegerHost, otlpPort.Port())
	logrus.Infof("Jaeger container started: UI=%s OTLP=%s", s.JaegerURL, s.JaegerOTLPEndpoint)
	return nil
}

func createJaegerConfig() (string, error) {
	tmpPath := filepath.Join(os.TempDir(), "e2e-jaeger-config.yaml")
	return tmpPath, os.WriteFile(tmpPath, []byte(jaegerConfigYAML), 0600)
}
