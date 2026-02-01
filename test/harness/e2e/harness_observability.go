package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const promQueryEndpointPath = "/api/v1/query"

// StartPortForwardWithCleanup starts port-forwarding and returns a cleanup function.
func (h *Harness) StartPortForwardWithCleanup(namespace, target string, localPort, remotePort int) (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd, done, err := h.StartPortForward(ctx, namespace, target, localPort, remotePort)
	if err != nil {
		cancel()
		return nil, err
	}

	cleanup := func() {
		cancel()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(fiveSecondTimeout):
		}
	}
	return cleanup, nil
}

// MetricsBody returns a closure to fetch metrics for Eventually.
func (h *Harness) MetricsBody(url string) func() string {
	return func() string {
		body, err := h.FetchMetrics(url)
		if err != nil {
			return ""
		}
		return body
	}
}

// FetchMetrics fetches a Prometheus text metrics endpoint.
func (h *Harness) FetchMetrics(url string) (string, error) {
	client := &http.Client{Timeout: fiveSecondTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// OTelcolActiveStatus returns a closure for Eventually to check otelcol status.
func (h *Harness) OTelcolActiveStatus() func() string {
	return func() string {
		stdout, err := h.VM.RunSSH([]string{"sudo", "systemctl", "is-active", "otelcol"}, nil)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(stdout.String())
	}
}

// MetricsLineCount returns a closure to count lines in a metrics payload.
func (h *Harness) MetricsLineCount(url string) func() int {
	return func() int {
		body := h.MetricsBody(url)()
		if body == "" {
			return 0
		}
		return len(strings.Split(strings.TrimSpace(body), "\n"))
	}
}

// MetricsMatchLabels returns a closure to check for a metric with required labels.
func (h *Harness) MetricsMatchLabels(url string, exact map[string]string, required, requiredNonEmpty []string) func() bool {
	return func() bool {
		body := h.MetricsBody(url)()
		if body == "" {
			return false
		}
		families, err := parsePrometheusMetrics(body)
		if err != nil {
			return false
		}
		family, ok := families["system_cpu_time_seconds_total"]
		if !ok {
			return false
		}
		return metricFamilyHasLabels(family, exact, required, requiredNonEmpty)
	}
}

// PromQueryResponse represents a Prometheus query response.
type PromQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// PromQuery executes a Prometheus query against a base URL.
func (h *Harness) PromQuery(baseURL, query string) (PromQueryResponse, error) {
	var parsed PromQueryResponse
	client := &http.Client{Timeout: fiveSecondTimeout}
	req, err := http.NewRequest(http.MethodGet, baseURL+promQueryEndpointPath, nil)
	if err != nil {
		return parsed, err
	}
	q := req.URL.Query()
	q.Set("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return parsed, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return parsed, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return parsed, err
	}

	if err := json.Unmarshal(body, &parsed); err != nil {
		return parsed, err
	}
	if parsed.Status != "success" {
		return parsed, fmt.Errorf("prometheus query failed: %s", parsed.Status)
	}

	return parsed, nil
}

// PromQueryResultCount returns a closure for polling the count of query results.
func (h *Harness) PromQueryResultCount(promURL, query string) func() int {
	return func() int {
		resp, err := h.PromQuery(promURL, query)
		if err != nil {
			return 0
		}
		return len(resp.Data.Result)
	}
}

// PromQueryCountValue returns a closure for polling a count() query value.
func (h *Harness) PromQueryCountValue(promURL, query string) func() float64 {
	return func() float64 {
		resp, err := h.PromQuery(promURL, query)
		if err != nil {
			return 0
		}
		if len(resp.Data.Result) == 0 || len(resp.Data.Result[0].Value) < 2 {
			return 0
		}
		valueStr, ok := resp.Data.Result[0].Value[1].(string)
		if !ok {
			return 0
		}
		val, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			return 0
		}
		return val
	}
}

// PromQueryHasLabels returns a closure to check for a query result with required labels.
func (h *Harness) PromQueryHasLabels(promURL, query string, exact map[string]string, required []string) func() bool {
	return func() bool {
		resp, err := h.PromQuery(promURL, query)
		if err != nil {
			return false
		}
		for _, result := range resp.Data.Result {
			if labelsMatch(result.Metric, exact, required) {
				return true
			}
		}
		return false
	}
}

func labelsMatch(labels map[string]string, exact map[string]string, required []string) bool {
	for name, value := range exact {
		if labels[name] != value {
			return false
		}
	}
	for _, name := range required {
		if _, ok := labels[name]; !ok {
			return false
		}
	}
	return true
}

// GetConfigMapValue returns a string value from a ConfigMap using a jsonpath selector.
// Deprecated: Use GetServiceConfig instead for environment-agnostic config access.
func (h *Harness) GetConfigMapValue(namespace, name, jsonPath string) (string, error) {
	// #nosec G204 -- command args are fixed and controlled in test.
	out, err := exec.Command("kubectl", "get", "configmap", name,
		"-n", namespace,
		"-o", jsonPath,
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl get configmap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// GetServiceConfig returns the full configuration content for a service.
// This is environment-agnostic: uses ConfigMap for K8s, config files for Quadlet.
func (h *Harness) GetServiceConfig(service infra.ServiceName) (string, error) {
	provider := h.GetInfraProvider()
	if provider == nil {
		return "", fmt.Errorf("infra provider not initialized")
	}
	return provider.GetServiceConfig(service)
}

// VerifyServiceExists verifies a Kubernetes service exists.
func (h *Harness) VerifyServiceExists(namespace, name string) error {
	// #nosec G204 -- command args are fixed and controlled in test.
	out, err := exec.Command("kubectl", "get", "svc", "-n", namespace, name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl get svc %s/%s: %w: %s", namespace, name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func parsePrometheusMetrics(body string) (map[string]*dto.MetricFamily, error) {
	parser := expfmt.TextParser{}
	return parser.TextToMetricFamilies(strings.NewReader(body))
}

func metricFamilyHasLabels(family *dto.MetricFamily, exact map[string]string, required, requiredNonEmpty []string) bool {
	if family == nil {
		return false
	}
	for _, metric := range family.GetMetric() {
		if metricMatchesLabels(metric, exact, required, requiredNonEmpty) {
			return true
		}
	}
	return false
}

func metricMatchesLabels(metric *dto.Metric, exact map[string]string, required, requiredNonEmpty []string) bool {
	labelValues := map[string]string{}
	for _, label := range metric.GetLabel() {
		labelValues[label.GetName()] = label.GetValue()
	}
	for name, value := range exact {
		if labelValues[name] != value {
			return false
		}
	}
	for _, name := range required {
		if _, ok := labelValues[name]; !ok {
			return false
		}
	}
	for _, name := range requiredNonEmpty {
		if labelValues[name] == "" {
			return false
		}
	}
	return true
}
