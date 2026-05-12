package auxiliary

import (
	"context"
	"fmt"
	"net"
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

// Prometheus holds connection info and the container for the aux Prometheus.
type Prometheus struct {
	URL       string
	Host      string
	Port      string
	container testcontainers.Container
}

// Start starts the Prometheus container and sets URL, Host, Port.
func (p *Prometheus) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Info("Starting prometheus container (always reused)")
	configPath, err := createPrometheusConfig()
	if err != nil {
		return fmt.Errorf("failed to create prometheus config: %w", err)
	}
	req := testcontainers.ContainerRequest{
		Image:        prometheusImage,
		Name:         prometheusContainerName,
		ExposedPorts: []string{prometheusPort},
		Cmd:          []string{"--config.file=/etc/prometheus/prometheus.yml", "--web.enable-lifecycle", "--storage.tsdb.retention.time=1h"},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: configPath, ContainerFilePath: "/etc/prometheus/prometheus.yml", FileMode: 0644},
		},
		WaitingFor: wait.ForHTTP("/-/ready").WithPort("9090"),
		SkipReaper: true,
	}
	container, err := CreateContainer(ctx, req, true, WithNetwork(network), WithHostAccess())
	if err != nil {
		return err
	}
	p.container = container
	p.Host = GetHostIP()
	port, _ := container.MappedPort(ctx, "9090")
	p.Port = port.Port()
	p.URL = fmt.Sprintf("http://%s", net.JoinHostPort(p.Host, p.Port))
	logrus.Infof("Prometheus container started: %s", p.URL)
	return nil
}

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

func createPrometheusConfig() (string, error) {
	config := fmt.Sprintf(prometheusConfigTemplate, GetContainerHostname())
	tmpPath := filepath.Join(os.TempDir(), "e2e-prometheus.yml")
	return tmpPath, os.WriteFile(tmpPath, []byte(config), 0600)
}

// ReloadPrometheusConfig triggers a config reload via the lifecycle API.
func ReloadPrometheusConfig(prometheusURL string) error {
	resp, err := http.Post(prometheusURL+"/-/reload", "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus reload returned status %d", resp.StatusCode)
	}
	return nil
}

// QueryPrometheus runs a PromQL query.
func QueryPrometheus(prometheusURL, query string) (*http.Response, error) {
	url := fmt.Sprintf("%s/api/v1/query?query=%s", prometheusURL, query)
	return http.Get(url) //nolint:gosec // URL from trusted params
}
