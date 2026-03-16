package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/sirupsen/logrus"
)

const (
	promQueryEndpointPath    = "/api/v1/query"
	promBackendProbeQuery    = "vector(1)"
	promBackendProbeTimeout  = 10 * time.Second
	promBackendProbeInterval = 250 * time.Millisecond
	portForwardDialTimeout   = 200 * time.Millisecond
	portForwardPollInterval  = 100 * time.Millisecond
	portForwardStopTimeout   = time.Second
)

type ServiceAccessBackend struct {
	ServiceName string
	Namespaces  []string
	Port        int
	UseTLS      bool
	RequireAuth bool
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
	client := &http.Client{Timeout: fiveSecondTimeout}
	return h.PromQueryWithClientToken(client, baseURL, query, bearerToken)
}

// PromQueryWithClientToken executes a Prometheus query against a base URL with optional bearer token,
// using the provided HTTP client.
func (h *Harness) PromQueryWithClientToken(client *http.Client, baseURL, query, bearerToken string) (PromQueryResponse, error) {
	var parsed PromQueryResponse
	if client == nil {
		return parsed, fmt.Errorf("http client is nil")
	}
	if baseURL == "" {
		return parsed, fmt.Errorf("baseURL cannot be empty")
	}
	if query == "" {
		return parsed, fmt.Errorf("query cannot be empty")
	}

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

// StartServiceAccess exposes a service and returns URL/client/cleanup.
// For known flightctl services it uses infra ExposeService; for non-mapped services
// it falls back to kubectl port-forward across provided namespaces.
func (h *Harness) StartServiceAccess(serviceName string, namespaces []string, remotePort int, useTLS bool, timeout time.Duration) (string, *http.Client, func(), error) {
	if h == nil {
		return "", nil, nil, fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(serviceName) == "" {
		return "", nil, nil, fmt.Errorf("service name is empty")
	}
	if remotePort <= 0 {
		return "", nil, nil, fmt.Errorf("remote port must be positive")
	}
	providers := setup.GetDefaultProviders()
	if providers == nil {
		return "", nil, nil, fmt.Errorf("default providers are not initialized")
	}

	if timeout <= 0 {
		timeout = fiveSecondTimeout
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
	client := &http.Client{Timeout: timeout, Transport: transport}

	// Known flightctl services are exposed via infra abstraction.
	if svc, ok := infra.ServiceNameFromDeploymentName(serviceName); ok {
		baseURL, cleanup, err := providers.Infra.ExposeService(svc, scheme)
		if err != nil {
			return "", nil, nil, err
		}
		baseURL, err = normalizeServiceURL(baseURL, scheme)
		if err != nil {
			cleanup()
			return "", nil, nil, fmt.Errorf("invalid service URL for %q: %w", serviceName, err)
		}
		return baseURL, client, cleanup, nil
	}

	localPort, err := h.GetFreeLocalPort()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to allocate local port: %w", err)
	}
	for _, ns := range namespaces {
		cleanup, pfErr := startServicePortForward(ns, serviceName, localPort, remotePort, timeout)
		if pfErr != nil {
			logrus.Debugf("port-forward candidate failed for %s/%s: %v", ns, serviceName, pfErr)
			continue
		}
		baseURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, localPort)
		return baseURL, client, cleanup, nil
	}
	return "", nil, nil, fmt.Errorf("unable to expose backend service %q in namespaces %v", serviceName, namespaces)
}

// StartFirstAvailableBackendAccess starts access to the first available backend from a candidate list.
// If a backend requires auth, a cluster token is returned.
func (h *Harness) StartFirstAvailableBackendAccess(backends []ServiceAccessBackend, timeout time.Duration) (string, *http.Client, string, func(), ServiceAccessBackend, error) {
	return h.StartFirstAvailableBackendAccessWithQuery(backends, promBackendProbeQuery, timeout)
}

// StartFirstAvailableBackendAccessWithQuery starts access to the first available backend from a candidate list.
// Backend readiness is validated by running the provided Prometheus query.
func (h *Harness) StartFirstAvailableBackendAccessWithQuery(backends []ServiceAccessBackend, query string, timeout time.Duration) (string, *http.Client, string, func(), ServiceAccessBackend, error) {
	var selected ServiceAccessBackend
	if h == nil {
		return "", nil, "", nil, selected, fmt.Errorf("harness is nil")
	}
	if len(backends) == 0 {
		return "", nil, "", nil, selected, fmt.Errorf("no backend candidates provided")
	}
	if strings.TrimSpace(query) == "" {
		return "", nil, "", nil, selected, fmt.Errorf("backend readiness query cannot be empty")
	}
	providers := setup.GetDefaultProviders()
	if providers == nil {
		return "", nil, "", nil, selected, fmt.Errorf("default providers are not initialized")
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
			token, err = providers.Infra.GetAPILoginToken()
			if err != nil {
				cleanup()
				lookupErrors = append(lookupErrors, fmt.Sprintf("%s: token: %v", backend.ServiceName, err))
				continue
			}
		}

		deadline := time.Now().Add(timeout)
		var lastErr error
		for time.Now().Before(deadline) {
			_, lastErr = h.PromQueryWithClientToken(client, baseURL, query, token)
			if lastErr == nil {
				return baseURL, client, token, cleanup, backend, nil
			}
			time.Sleep(promBackendProbeInterval)
		}
		lookupErrors = append(lookupErrors, fmt.Sprintf("%s: readiness probe timeout: %v", backend.ServiceName, lastErr))
		cleanup()
	}

	return "", nil, "", nil, selected, fmt.Errorf("unable to resolve backend from known candidates: %v", lookupErrors)
}

func normalizeServiceURL(rawURL, fallbackScheme string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("service URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("failed to parse service URL %q: %w", rawURL, err)
	}
	if parsed.Scheme == "" {
		if strings.TrimSpace(fallbackScheme) != "" {
			parsed.Scheme = fallbackScheme
		} else {
			parsed.Scheme = "http"
		}
	}
	if parsed.Host == "" {
		// Handle values like "127.0.0.1:12345" parsed as Path when scheme is missing.
		if parsed.Path != "" && !strings.HasPrefix(parsed.Path, "/") {
			parsed.Host = parsed.Path
			parsed.Path = ""
		}
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("service URL %q has empty host", rawURL)
	}
	return parsed.String(), nil
}

func startServicePortForward(namespace, serviceName string, localPort, remotePort int, startupTimeout time.Duration) (func(), error) {
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("namespace is empty for service %q", serviceName)
	}
	if strings.TrimSpace(serviceName) == "" {
		return nil, fmt.Errorf("service name is empty")
	}
	if localPort <= 0 || remotePort <= 0 {
		return nil, fmt.Errorf("invalid port mapping %d:%d", localPort, remotePort)
	}
	if startupTimeout <= 0 {
		startupTimeout = fiveSecondTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())
	// #nosec G204 -- e2e harness command args are constrained to validated test inputs/service metadata.
	cmd := exec.CommandContext(
		ctx,
		"kubectl",
		"-n", namespace,
		"port-forward",
		"svc/"+serviceName,
		fmt.Sprintf("%d:%d", localPort, remotePort),
	)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start port-forward %s/%s: %w", namespace, serviceName, err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), portForwardDialTimeout)
		if err == nil {
			_ = conn.Close()
			return func() {
				cancel()
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				select {
				case <-done:
				case <-time.After(portForwardStopTimeout):
				}
			}, nil
		}

		select {
		case err := <-done:
			cancel()
			if err == nil {
				err = fmt.Errorf("port-forward exited unexpectedly")
			}
			return nil, fmt.Errorf("port-forward %s/%s failed: %w (output: %s)", namespace, serviceName, err, strings.TrimSpace(out.String()))
		default:
		}
		time.Sleep(portForwardPollInterval)
	}

	cancel()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-done:
	case <-time.After(portForwardStopTimeout):
	}
	return nil, fmt.Errorf("timed out waiting for port-forward %s/%s", namespace, serviceName)
}

// waitForPrometheusBackendReady is kept for potential future use (e.g. backend health before queries).
func (h *Harness) waitForPrometheusBackendReady(baseURL, bearerToken string) error { //nolint:unused
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
