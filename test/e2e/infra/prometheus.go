package infra

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	prometheusImage         = "prom/prometheus:latest"
	prometheusContainerName = "e2e-prometheus"
	prometheusPort          = "9090/tcp"
)

// prometheusConfig is a minimal Prometheus config for E2E testing.
// It scrapes from the telemetry gateway which should be accessible from the container.
const prometheusConfigTemplate = `
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: "prometheus"
    static_configs:
      - targets: ["localhost:9090"]

  - job_name: "telemetry-gateway"
    static_configs:
      - targets: ["%s:9464"]
    relabel_configs:
      - source_labels: [__address__]
        regex: '([^:]+)(?::\d+)?'
        target_label: instance
        replacement: '$1'
`

// startPrometheus starts a Prometheus container for E2E tests.
// Prometheus always uses container reuse for metric accumulation across tests.
func (s *SatelliteServices) startPrometheus(ctx context.Context) error {
	logrus.Info("Starting prometheus container (always reused)")

	// Create a temporary prometheus config file
	configPath, err := createPrometheusConfig()
	if err != nil {
		return fmt.Errorf("failed to create prometheus config: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        prometheusImage,
		Name:         prometheusContainerName,
		ExposedPorts: []string{prometheusPort},
		Cmd: []string{
			"--config.file=/etc/prometheus/prometheus.yml",
			"--web.enable-lifecycle",
			"--storage.tsdb.retention.time=1h",
		},
		// Use ContainerFile instead of BindMount for DinD compatibility
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      configPath,
				ContainerFilePath: "/etc/prometheus/prometheus.yml",
				FileMode:          0644,
			},
		},
		WaitingFor: wait.ForHTTP("/-/ready").WithPort("9090"),
	}

	// Create container with appropriate provider and network
	// Prometheus always reuses containers for metric accumulation
	container, err := CreateContainer(ctx, req, true, // Always reuse for Prometheus
		WithNetwork(s.network),
		WithHostAccess(),
	)
	if err != nil {
		return fmt.Errorf("failed to start prometheus container: %w", err)
	}

	s.prometheus = container

	// Get host and port
	host, err := container.Host(ctx)
	if err != nil {
		return fmt.Errorf("failed to get prometheus host: %w", err)
	}
	s.PrometheusHost = host

	port, err := container.MappedPort(ctx, "9090")
	if err != nil {
		return fmt.Errorf("failed to get prometheus port: %w", err)
	}
	s.PrometheusPort = port.Port()
	s.PrometheusURL = fmt.Sprintf("http://%s:%s", s.PrometheusHost, s.PrometheusPort)

	logrus.Infof("Prometheus container started: %s", s.PrometheusURL)
	return nil
}

// createPrometheusConfig creates a temporary prometheus config file.
func createPrometheusConfig() (string, error) {
	// Get the telemetry gateway target - use host IP for KIND or host.containers.internal for quadlets
	telemetryGatewayTarget := GetContainerHostname()

	config := fmt.Sprintf(prometheusConfigTemplate, telemetryGatewayTarget)

	// Create temp file
	tmpDir := os.TempDir()
	configPath := filepath.Join(tmpDir, "e2e-prometheus.yml")

	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		return "", fmt.Errorf("failed to write prometheus config: %w", err)
	}

	return configPath, nil
}

// ReloadPrometheusConfig triggers a configuration reload via the lifecycle API.
// This is useful when the telemetry gateway target changes.
func ReloadPrometheusConfig(prometheusURL string) error {
	resp, err := http.Post(prometheusURL+"/-/reload", "", nil)
	if err != nil {
		return fmt.Errorf("failed to reload prometheus config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus config reload returned status %d", resp.StatusCode)
	}

	return nil
}

// QueryPrometheus executes a PromQL query and returns the result.
// This is a helper for tests that need to verify metrics.
func QueryPrometheus(prometheusURL, query string) (*http.Response, error) {
	url := fmt.Sprintf("%s/api/v1/query?query=%s", prometheusURL, query)
	return http.Get(url) //nolint:gosec // URL is constructed from trusted prometheusURL and query parameters
}
