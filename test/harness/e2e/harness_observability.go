package e2e

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const (
	promQueryEndpointPath    = "/api/v1/query"
	promBackendProbeQuery    = "1"
	promBackendProbeTimeout  = 10 * time.Second
	promBackendProbeInterval = 250 * time.Millisecond
)

type ServiceAccessBackend struct {
	ServiceName string
	Namespaces  []string
	Port        int
	UseTLS      bool
	RequireAuth bool
}

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
	return h.PromQueryWithToken(baseURL, query, "")
}

// PromQueryWithToken executes a Prometheus query against a base URL with optional bearer token.
func (h *Harness) PromQueryWithToken(baseURL, query, bearerToken string) (PromQueryResponse, error) {
	var parsed PromQueryResponse
	if baseURL == "" {
		return parsed, fmt.Errorf("baseURL cannot be empty")
	}
	if query == "" {
		return parsed, fmt.Errorf("query cannot be empty")
	}

	client := &http.Client{Timeout: fiveSecondTimeout}
	req, err := http.NewRequest(http.MethodGet, baseURL+promQueryEndpointPath, nil)
	if err != nil {
		return parsed, err
	}
	q := req.URL.Query()
	q.Set("query", query)
	req.URL.RawQuery = q.Encode()
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

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

// HTTPGetWithContentType performs a GET request against baseURL+path with optional bearer token.
func (h *Harness) HTTPGetWithContentType(client *http.Client, baseURL, path, bearerToken string) (int, string, string, error) {
	if client == nil {
		return 0, "", "", fmt.Errorf("http client is nil")
	}
	if baseURL == "" {
		return 0, "", "", fmt.Errorf("base URL is empty")
	}
	if path == "" {
		return 0, "", "", fmt.Errorf("request path is empty")
	}

	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to create GET request: %w", err)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", fmt.Errorf("GET %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed reading response body from %s: %w", path, err)
	}

	return resp.StatusCode, string(body), resp.Header.Get("Content-Type"), nil
}

// HTTPGet performs a GET request against baseURL+path with optional bearer token.
func (h *Harness) HTTPGet(client *http.Client, baseURL, path, bearerToken string) (int, string, error) {
	statusCode, body, _, err := h.HTTPGetWithContentType(client, baseURL, path, bearerToken)
	return statusCode, body, err
}

// StatusCodePollerWithExpectedBody returns a poller that validates status code and exact body.
func (h *Harness) StatusCodePollerWithExpectedBody(client *http.Client, baseURL, path, bearerToken, expectedBody string, expectedStatus int) func() int {
	return func() int {
		statusCode, body, err := h.HTTPGet(client, baseURL, path, bearerToken)
		if err != nil {
			return 0
		}
		if expectedBody != "" && body != expectedBody {
			return 0
		}
		if expectedStatus == 0 {
			return statusCode
		}
		if statusCode != expectedStatus {
			return 0
		}
		return statusCode
	}
}

// JSONStatusCodePoller returns a poller that validates status code and JSON body/content type.
func (h *Harness) JSONStatusCodePoller(client *http.Client, baseURL, path, bearerToken, contentTypeMustContain string, expectedStatus int) func() int {
	return func() int {
		statusCode, body, contentType, err := h.HTTPGetWithContentType(client, baseURL, path, bearerToken)
		if err != nil {
			return 0
		}
		if expectedStatus != 0 && statusCode != expectedStatus {
			return 0
		}
		if body == "" || !json.Valid([]byte(body)) {
			return 0
		}
		if contentTypeMustContain != "" && !strings.Contains(contentType, contentTypeMustContain) {
			return 0
		}
		return statusCode
	}
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

// VerifyServiceExists verifies a Kubernetes service exists.
func (h *Harness) VerifyServiceExists(namespace, name string) error {
	// #nosec G204 -- command args are fixed and controlled in test.
	out, err := exec.Command("kubectl", "get", "svc", "-n", namespace, name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl get svc %s/%s: %w: %s", namespace, name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StartServiceAccess resolves, port-forwards and returns an HTTP client to a service.
func (h *Harness) StartServiceAccess(serviceName string, namespaces []string, remotePort int, useTLS bool, timeout time.Duration) (string, *http.Client, func(), error) {
	if h == nil {
		return "", nil, nil, fmt.Errorf("harness is nil")
	}
	if serviceName == "" {
		return "", nil, nil, fmt.Errorf("service name is empty")
	}
	if remotePort <= 0 {
		return "", nil, nil, fmt.Errorf("invalid remote port: %d", remotePort)
	}

	namespace, err := h.ResolveServiceNamespace(serviceName, namespaces)
	if err != nil {
		return "", nil, nil, err
	}

	localPort, err := h.GetFreeLocalPort()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to allocate local port: %w", err)
	}

	target := "svc/" + serviceName
	cleanup, err := h.StartPortForwardWithCleanup(namespace, target, localPort, remotePort)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to start port-forward for %s in namespace %s: %w", target, namespace, err)
	}

	scheme := "http"
	transport := &http.Transport{}
	if useTLS {
		scheme = "https"
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- e2e test uses local ephemeral port-forward endpoint
			MinVersion:         tls.VersionTLS12,
		}
	}
	if timeout <= 0 {
		timeout = fiveSecondTimeout
	}
	baseURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, localPort)
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
	return baseURL, client, cleanup, nil
}

// StartFirstAvailableBackendAccess starts access to the first available backend from a candidate list.
// If a backend requires auth, an OpenShift token is returned.
func (h *Harness) StartFirstAvailableBackendAccess(backends []ServiceAccessBackend, timeout time.Duration) (string, *http.Client, string, func(), ServiceAccessBackend, error) {
	var selected ServiceAccessBackend
	if h == nil {
		return "", nil, "", nil, selected, fmt.Errorf("harness is nil")
	}
	if len(backends) == 0 {
		return "", nil, "", nil, selected, fmt.Errorf("no backend candidates provided")
	}

	var lookupErrors []string
	for _, backend := range backends {
		baseURL, client, cleanup, err := h.StartServiceAccess(backend.ServiceName, backend.Namespaces, backend.Port, backend.UseTLS, timeout)
		if err != nil {
			lookupErrors = append(lookupErrors, fmt.Sprintf("%s: %v", backend.ServiceName, err))
			continue
		}

		token := ""
		if backend.RequireAuth {
			token, err = h.GetOpenShiftToken()
			if err != nil {
				cleanup()
				return "", nil, "", nil, selected, fmt.Errorf("failed to resolve OpenShift token for backend %s: %w", backend.ServiceName, err)
			}
		}

		if err := h.waitForPrometheusBackendReady(baseURL, token); err != nil {
			lookupErrors = append(lookupErrors, fmt.Sprintf("%s: %v", backend.ServiceName, err))
			cleanup()
			continue
		}

		return baseURL, client, token, cleanup, backend, nil
	}

	return "", nil, "", nil, selected, fmt.Errorf("unable to resolve backend from known candidates: %v", lookupErrors)
}

func (h *Harness) waitForPrometheusBackendReady(baseURL, bearerToken string) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}

	deadline := time.Now().Add(promBackendProbeTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = h.PromQueryWithToken(baseURL, promBackendProbeQuery, bearerToken)
		if lastErr == nil {
			return nil
		}
		time.Sleep(promBackendProbeInterval)
	}

	return fmt.Errorf("prometheus backend did not become ready within %s: %w", promBackendProbeTimeout, lastErr)
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
