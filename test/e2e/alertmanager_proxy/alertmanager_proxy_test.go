package alertmanagerproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	alertmanagerProxyServiceName = "flightctl-alertmanager-proxy"
	alertmanagerProxyServicePort = 8443

	proxyHealthPath   = "/health"
	proxyStatusPath   = "/api/v2/status"
	proxyAlertsPath   = "/api/v2/alerts"
	proxyInvalidOrgID = "not-a-uuid"
	healthBodyOK      = "OK"
	contentTypeHeader = "Content-Type"
	applicationJSON   = "application/json"
	statusKindField   = "\"kind\":\"Status\""
	statusFailure     = "\"status\":\"Failure\""
	authTokenErrMsg   = "failed to get auth token"
	statusForbidden   = "Forbidden"
	nonAdminUser      = "demouser1"
	authDisabledMsg   = "auth is disabled in this environment"
	alertsLabelName   = "alertname"
	alertsLabelOrgID  = "org_id"
	alertsLabelRes    = "resource"

	alertNameDeviceDisconnected = "DeviceDisconnected"
	alertNameDeviceDiskWarning  = "DeviceDiskWarning"

	prometheusServiceName = "flightctl-prometheus"
	prometheusServicePort = 9090
	prometheusQueryAlerts = `ALERTS{org_id="%s",alertname=~"%s|%s"}`
	prometheusStatusOK    = "success"
	prometheusQueryPath   = "/api/v1/query"

	uiServiceName         = "flightctl-ui"
	uiServicePort         = 8080
	uiConfigMapName       = "flightctl-ui"
	uiConfigProxyJSONPath = "jsonpath={.data.FLIGHTCTL_ALERTMANAGER_PROXY}"
	uiExpectedProxyHost   = "alertmanager-proxy"

	httpClientTimeout = 10 * time.Second
)

var (
	testHarness            *e2e.Harness
	testRuntimeContext     string
	defaultProxyNamespaces = []string{"flightctl-external", "flightctl", util.E2E_NAMESPACE}
	defaultPromNamespaces  = []string{util.E2E_NAMESPACE, "flightctl", "flightctl-external"}
	defaultUINamespaces    = []string{"flightctl-external", "flightctl", util.E2E_NAMESPACE}
	defaultUWMNamespaces   = []string{"openshift-user-workload-monitoring"}
	defaultOCPNamespace    = []string{"openshift-monitoring"}
)

type prometheusBackend struct {
	serviceName string
	namespaces  []string
	port        int
	useTLS      bool
	requireAuth bool
}

type proxyAuthContext struct {
	orgID string
	token string
}

type alertmanagerAlertResponse struct {
	Labels map[string]string `json:"labels"`
}

var prometheusBackends = []prometheusBackend{
	{
		serviceName: prometheusServiceName,
		namespaces:  defaultPromNamespaces,
		port:        prometheusServicePort,
		useTLS:      false,
		requireAuth: false,
	},
	{
		serviceName: "prometheus-k8s",
		namespaces:  defaultOCPNamespace,
		port:        9091,
		useTLS:      true,
		requireAuth: true,
	},
	{
		serviceName: "prometheus-user-workload",
		namespaces:  defaultUWMNamespaces,
		port:        9091,
		useTLS:      true,
		requireAuth: true,
	},
}

var _ = Describe("Alertmanager proxy", Label("alerts", "alertmanager-proxy", "sanity"), func() {
	BeforeEach(func() {
		testHarness = e2e.GetWorkerHarness()

		ctxName, err := e2e.GetContext()
		Expect(err).ToNot(HaveOccurred())
		testRuntimeContext = ctxName
		if ctxName != util.KIND && ctxName != util.OCP {
			Skip(fmt.Sprintf("Kubernetes-backed test context required, got %q", ctxName))
		}
	})

	It("validates core alertmanager-proxy API contract", Label("87773", "sanity"), func() {
		GinkgoWriter.Printf("[86001-86004,86006] validating core proxy API contract\n")
		baseURL, client, cleanup, err := startProxyAccess(testHarness)
		Expect(err).ToNot(HaveOccurred())
		defer cleanup()

		GinkgoWriter.Printf("[86001] validating public health and status endpoints\n")
		Eventually(proxyHealthStatusCodePoller(client, baseURL), util.TIMEOUT, util.POLLING).Should(Equal(http.StatusOK))
		Eventually(proxyStatusEndpointPoller(client, baseURL), util.TIMEOUT, util.POLLING).Should(Equal(http.StatusOK))

		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred())
		Expect(orgID).ToNot(BeEmpty())
		GinkgoWriter.Printf("[86002] using org_id=%s for unauthenticated request\n", orgID)

		alertsPath := fmt.Sprintf("%s?filter=org_id=%s", proxyAlertsPath, orgID)
		noAuthStatusCode, noAuthBody, err := doProxyGet(client, baseURL, alertsPath, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(noAuthStatusCode).To(Equal(http.StatusBadRequest))
		Expect(noAuthBody).ToNot(BeEmpty())
		Expect(noAuthBody).To(ContainSubstring(statusKindField))
		Expect(noAuthBody).To(ContainSubstring(statusFailure))
		Expect(noAuthBody).To(ContainSubstring(authTokenErrMsg))

		authCtx, err := resolveProxyAuthContext(testHarness)
		if err != nil && strings.Contains(err.Error(), authDisabledMsg) {
			Skip(authDisabledMsg)
		}
		Expect(err).ToNot(HaveOccurred())
		Expect(authCtx.orgID).ToNot(BeEmpty())
		Expect(authCtx.token).ToNot(BeEmpty())
		GinkgoWriter.Printf("[86003] resolved authenticated context org_id=%s\n", authCtx.orgID)

		authAlertsPath := fmt.Sprintf("%s?filter=org_id=%s", proxyAlertsPath, authCtx.orgID)
		withAuthStatusCode, withAuthBody, err := doProxyGet(client, baseURL, authAlertsPath, authCtx.token)
		Expect(err).ToNot(HaveOccurred())
		Expect(withAuthStatusCode).To(Equal(http.StatusOK))
		Expect(withAuthBody).ToNot(BeEmpty())
		Expect(json.Valid([]byte(withAuthBody))).To(BeTrue(), "alerts response body should be valid JSON")

		GinkgoWriter.Printf("[86004] validating malformed org_id filter handling\n")
		invalidPath := fmt.Sprintf("%s?filter=org_id=%s", proxyAlertsPath, proxyInvalidOrgID)
		statusCode, body, err := doProxyGet(client, baseURL, invalidPath, authCtx.token)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusBadRequest))
		Expect(body).ToNot(BeEmpty())
		Expect(body).To(ContainSubstring(statusKindField))
		Expect(body).To(ContainSubstring(statusFailure))
		Expect(body).To(ContainSubstring("invalid org_id format in filter"))

		GinkgoWriter.Printf("[86006] validating common alert filters for offline and threshold alerts\n")
		for _, alertName := range []string{alertNameDeviceDisconnected, alertNameDeviceDiskWarning} {
			GinkgoWriter.Printf("[86006] querying alert filter alertname=%s\n", alertName)
			filteredPath := buildAlertsFilterPath(authCtx.orgID, alertName, "")
			filterStatusCode, filterBody, err := doProxyGet(client, baseURL, filteredPath, authCtx.token)
			Expect(err).ToNot(HaveOccurred())
			Expect(filterStatusCode).To(Equal(http.StatusOK))
			Expect(filterBody).ToNot(BeEmpty())
			Expect(json.Valid([]byte(filterBody))).To(BeTrue())

			alerts, err := parseAlertsResponse(filterBody)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("[86006] alertname=%s returned %d alerts\n", alertName, len(alerts))
			for _, alert := range alerts {
				Expect(alert.Labels).ToNot(BeNil())
				if name, ok := alert.Labels[alertsLabelName]; ok {
					Expect(name).To(Equal(alertName))
				}
				if id, ok := alert.Labels[alertsLabelOrgID]; ok {
					Expect(id).To(Equal(authCtx.orgID))
				}
			}
		}
	})

	It("denies authenticated user without alerts permission", Label("87776", "sanity"), func() {
		GinkgoWriter.Printf("[86005] validating non-admin authorization behavior on OCP\n")
		if testRuntimeContext != util.OCP {
			Skip("non-admin authz-denied flow currently validated on OCP context")
		}

		baseURL, client, cleanup, err := startProxyAccess(testHarness)
		Expect(err).ToNot(HaveOccurred())
		defer cleanup()

		defaultK8sContext, k8sAPIEndpoint, err := getClusterLoginContext(testHarness, testHarness.Context)
		if err != nil {
			Skip(fmt.Sprintf("unable to resolve cluster login context for non-admin authz test: %v", err))
		}
		GinkgoWriter.Printf("[86005] using k8s context=%s api=%s\n", defaultK8sContext, k8sAPIEndpoint)

		err = loginAsNonAdminForAlertProxy(testHarness, nonAdminUser, defaultK8sContext, k8sAPIEndpoint)
		if err != nil {
			Skip(fmt.Sprintf("unable to login as non-admin user %q for authz-denied test: %v", nonAdminUser, err))
		}
		defer restoreAdminLoginContext(testHarness, testHarness.Context, defaultK8sContext)

		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred())
		Expect(orgID).ToNot(BeEmpty())

		token, err := getClientAccessToken(testHarness)
		Expect(err).ToNot(HaveOccurred())
		Expect(token).ToNot(BeEmpty())
		GinkgoWriter.Printf("[86005] resolved non-admin access token for authz check\n")

		alertsPath := fmt.Sprintf("%s?filter=org_id=%s", proxyAlertsPath, orgID)
		statusCode, body, err := doProxyGet(client, baseURL, alertsPath, token)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusForbidden))
		Expect(body).ToNot(BeEmpty())
		Expect(body).To(ContainSubstring(statusForbidden))
	})

	It("verifies Prometheus query path for common alert series", Label("87775", "sanity"), func() {
		GinkgoWriter.Printf("[86007] validating prometheus alert series query path\n")
		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred())
		Expect(orgID).ToNot(BeEmpty())
		GinkgoWriter.Printf("[86007] using org_id=%s\n", orgID)

		promURL, promClient, promToken, cleanup, err := startPrometheusAccess(testHarness)
		Expect(err).ToNot(HaveOccurred())
		defer cleanup()

		query := fmt.Sprintf(prometheusQueryAlerts, orgID, alertNameDeviceDisconnected, alertNameDeviceDiskWarning)
		GinkgoWriter.Printf("[86007] query=%s\n", query)
		resp, err := queryPrometheus(promClient, promURL, query, promToken)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.Status).To(Equal(prometheusStatusOK))
		Expect(resp.Data.Result).ToNot(BeNil(), "prometheus query should return a valid result array")
		GinkgoWriter.Printf("[86007] prometheus result size=%d\n", len(resp.Data.Result))
	})

	It("verifies UI visibility wiring for alertmanager-proxy", Label("87774", "sanity"), func() {
		GinkgoWriter.Printf("[86008] validating ui wiring for alertmanager-proxy visibility\n")
		uiNamespace, err := testHarness.ResolveServiceNamespace(uiServiceName, defaultUINamespaces)
		if err != nil {
			Skip(fmt.Sprintf("ui service unavailable for this environment: %v", err))
		}
		GinkgoWriter.Printf("[86008] using ui namespace=%s\n", uiNamespace)

		proxyURL, err := testHarness.GetConfigMapValue(uiNamespace, uiConfigMapName, uiConfigProxyJSONPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(proxyURL)).ToNot(BeEmpty())
		Expect(strings.ToLower(proxyURL)).To(ContainSubstring(uiExpectedProxyHost))
		GinkgoWriter.Printf("[86008] ui proxy URL=%s\n", strings.TrimSpace(proxyURL))

		uiBaseURL, uiClient, cleanup, err := testHarness.StartServiceAccess(uiServiceName, defaultUINamespaces, uiServicePort, false, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred())
		defer cleanup()

		statusCode, body, contentType, err := doProxyGetWithContentType(uiClient, uiBaseURL, "/", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(BeElementOf(http.StatusOK, http.StatusBadRequest))
		Expect(body).ToNot(BeEmpty())
		GinkgoWriter.Printf("[86008] ui root status=%d contentType=%q bodyPreview=%q\n", statusCode, contentType, truncateForLog(body, 200))
		if contentType != "" {
			Expect(strings.Contains(contentType, "text/html") || strings.Contains(contentType, applicationJSON) || strings.Contains(contentType, "text/plain")).To(BeTrue())
		}
	})
})

func startProxyAccess(harness *e2e.Harness) (string, *http.Client, func(), error) {
	if harness == nil {
		return "", nil, noopCleanup, fmt.Errorf("harness is nil")
	}

	baseURL, client, cleanup, err := harness.StartServiceAccess(alertmanagerProxyServiceName, defaultProxyNamespaces, alertmanagerProxyServicePort, true, httpClientTimeout)
	if err != nil {
		return "", nil, noopCleanup, err
	}
	GinkgoWriter.Printf("Using alertmanager-proxy service %s via %s\n", alertmanagerProxyServiceName, baseURL)
	return baseURL, client, cleanup, nil
}

func startPrometheusAccess(harness *e2e.Harness) (string, *http.Client, string, func(), error) {
	if harness == nil {
		return "", nil, "", noopCleanup, fmt.Errorf("harness is nil")
	}

	var lookupErrors []string
	for _, backend := range prometheusBackends {
		GinkgoWriter.Printf("Trying prometheus backend candidate %s (namespaces=%v, port=%d, tls=%t, auth=%t)\n", backend.serviceName, backend.namespaces, backend.port, backend.useTLS, backend.requireAuth)
		baseURL, client, cleanup, err := harness.StartServiceAccess(backend.serviceName, backend.namespaces, backend.port, backend.useTLS, httpClientTimeout)
		if err != nil {
			GinkgoWriter.Printf("Prometheus backend candidate %s unavailable: %v\n", backend.serviceName, err)
			lookupErrors = append(lookupErrors, fmt.Sprintf("%s: %v", backend.serviceName, err))
			continue
		}

		token := ""
		if backend.requireAuth {
			token, err = harness.GetOpenShiftToken()
			if err != nil {
				cleanup()
				return "", nil, "", noopCleanup, fmt.Errorf("failed to resolve OpenShift token for prometheus backend %s: %w", backend.serviceName, err)
			}
			GinkgoWriter.Printf("Using OpenShift token for prometheus backend %s\n", backend.serviceName)
		}

		GinkgoWriter.Printf("Using prometheus backend %s via %s (auth=%t)\n", backend.serviceName, baseURL, backend.requireAuth)
		return baseURL, client, token, cleanup, nil
	}

	return "", nil, "", noopCleanup, fmt.Errorf("unable to resolve prometheus service backend from known candidates: %v", lookupErrors)
}

func proxyHealthStatusCodePoller(client *http.Client, baseURL string) func() int {
	return func() int {
		statusCode, body, err := doProxyGet(client, baseURL, proxyHealthPath, "")
		if err != nil {
			GinkgoWriter.Printf("Health endpoint request failed: %v\n", err)
			return 0
		}
		if body != healthBodyOK {
			GinkgoWriter.Printf("Unexpected /health body: %q\n", body)
			return 0
		}
		return statusCode
	}
}

func proxyStatusEndpointPoller(client *http.Client, baseURL string) func() int {
	return func() int {
		statusCode, body, contentType, err := doProxyGetWithContentType(client, baseURL, proxyStatusPath, "")
		if err != nil {
			GinkgoWriter.Printf("Status endpoint request failed: %v\n", err)
			return 0
		}
		if statusCode != http.StatusOK {
			return statusCode
		}
		if body == "" {
			GinkgoWriter.Printf("Empty /api/v2/status body\n")
			return 0
		}
		if !json.Valid([]byte(body)) {
			GinkgoWriter.Printf("Invalid JSON returned by /api/v2/status: %q\n", body)
			return 0
		}
		if !strings.Contains(contentType, applicationJSON) {
			GinkgoWriter.Printf("Unexpected status content-type: %q\n", contentType)
			return 0
		}
		return statusCode
	}
}

func doProxyGetWithContentType(client *http.Client, baseURL, path, bearerToken string) (int, string, string, error) {
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
	GinkgoWriter.Printf("HTTP GET %s tokenProvided=%t\n", req.URL.String(), bearerToken != "")

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", fmt.Errorf("GET %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed reading response body from %s: %w", path, err)
	}
	if len(body) == 0 {
		GinkgoWriter.Printf("HTTP GET %s -> status=%d contentType=%q body=<empty>\n", path, resp.StatusCode, resp.Header.Get(contentTypeHeader))
		return resp.StatusCode, "", resp.Header.Get(contentTypeHeader), nil
	}
	GinkgoWriter.Printf("HTTP GET %s -> status=%d contentType=%q bodyPreview=%q\n", path, resp.StatusCode, resp.Header.Get(contentTypeHeader), truncateForLog(string(body), 240))

	return resp.StatusCode, string(body), resp.Header.Get(contentTypeHeader), nil
}

func doProxyGet(client *http.Client, baseURL, path, bearerToken string) (int, string, error) {
	statusCode, body, _, err := doProxyGetWithContentType(client, baseURL, path, bearerToken)
	return statusCode, body, err
}

type prometheusQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []json.RawMessage `json:"result"`
	} `json:"data"`
}

func queryPrometheus(client *http.Client, baseURL, query, bearerToken string) (prometheusQueryResponse, error) {
	respModel := prometheusQueryResponse{}
	if client == nil {
		return respModel, fmt.Errorf("http client is nil")
	}
	if baseURL == "" {
		return respModel, fmt.Errorf("base URL is empty")
	}
	if query == "" {
		return respModel, fmt.Errorf("query is empty")
	}

	req, err := http.NewRequest(http.MethodGet, baseURL+prometheusQueryPath, nil)
	if err != nil {
		return respModel, fmt.Errorf("failed to create prometheus query request: %w", err)
	}
	req.URL.RawQuery = url.Values{"query": []string{query}}.Encode()
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	GinkgoWriter.Printf("Prometheus query request %s tokenProvided=%t\n", req.URL.String(), bearerToken != "")

	response, err := client.Do(req)
	if err != nil {
		return respModel, fmt.Errorf("prometheus query request failed: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return respModel, fmt.Errorf("failed reading prometheus query response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return respModel, fmt.Errorf("prometheus query returned status %d: %s", response.StatusCode, string(body))
	}
	if !json.Valid(body) {
		return respModel, fmt.Errorf("prometheus query returned invalid json: %q", string(body))
	}
	if err := json.Unmarshal(body, &respModel); err != nil {
		return respModel, fmt.Errorf("failed to decode prometheus query response: %w", err)
	}
	GinkgoWriter.Printf("Prometheus query response status=%s resultCount=%d\n", respModel.Status, len(respModel.Data.Result))
	return respModel, nil
}

func resolveProxyAuthContext(harness *e2e.Harness) (proxyAuthContext, error) {
	authCtx := proxyAuthContext{}
	if harness == nil {
		return authCtx, fmt.Errorf("harness is nil")
	}

	authMethod := login.LoginToAPIWithToken(harness)
	if authMethod == login.AuthDisabled {
		return authCtx, errors.New(authDisabledMsg)
	}

	orgID, err := harness.GetOrganizationID()
	if err != nil {
		return authCtx, fmt.Errorf("failed to resolve organization id: %w", err)
	}
	if orgID == "" {
		return authCtx, fmt.Errorf("organization id is empty")
	}

	token, err := getClientAccessToken(harness)
	if err != nil {
		return authCtx, fmt.Errorf("failed to resolve client access token: %w", err)
	}
	if token == "" {
		return authCtx, fmt.Errorf("client access token is empty")
	}

	authCtx.orgID = orgID
	authCtx.token = token
	GinkgoWriter.Printf("Resolved proxy auth context for org_id=%s\n", orgID)
	return authCtx, nil
}

func buildAlertsFilterPath(orgID, alertName, resourceName string) string {
	filters := url.Values{}
	if orgID != "" {
		filters.Add("filter", fmt.Sprintf("%s=%s", alertsLabelOrgID, orgID))
	}
	if alertName != "" {
		filters.Add("filter", fmt.Sprintf("%s=%s", alertsLabelName, alertName))
	}
	if resourceName != "" {
		filters.Add("filter", fmt.Sprintf("%s=%s", alertsLabelRes, resourceName))
	}
	return proxyAlertsPath + "?" + filters.Encode()
}

func parseAlertsResponse(body string) ([]alertmanagerAlertResponse, error) {
	result := make([]alertmanagerAlertResponse, 0)
	if body == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, fmt.Errorf("failed to decode alerts response: %w", err)
	}
	GinkgoWriter.Printf("Parsed alerts response with %d entries\n", len(result))
	return result, nil
}

func getClientAccessToken(harness *e2e.Harness) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}

	cfg, err := harness.ReadClientConfig("")
	if err != nil {
		return "", fmt.Errorf("failed to read client config: %w", err)
	}
	if cfg == nil {
		return "", fmt.Errorf("client config is nil")
	}
	if cfg.AuthInfo.AccessToken == "" {
		return "", fmt.Errorf("access token is empty")
	}
	GinkgoWriter.Printf("Client access token resolved from config\n")

	return cfg.AuthInfo.AccessToken, nil
}

func getClusterLoginContext(harness *e2e.Harness, ctx context.Context) (string, string, error) {
	if harness == nil {
		return "", "", fmt.Errorf("harness is nil")
	}

	defaultK8sContext, err := harness.GetDefaultK8sContext()
	if err != nil {
		return "", "", fmt.Errorf("failed to get default k8s context: %w", err)
	}

	k8sAPIEndpoint, err := harness.GetK8sApiEndpoint(ctx, defaultK8sContext)
	if err != nil {
		return "", "", fmt.Errorf("failed to get k8s api endpoint for context %q: %w", defaultK8sContext, err)
	}

	GinkgoWriter.Printf("Resolved cluster login context=%s api=%s\n", defaultK8sContext, k8sAPIEndpoint)
	return defaultK8sContext, k8sAPIEndpoint, nil
}

func loginAsNonAdminForAlertProxy(harness *e2e.Harness, userName, k8sContext, k8sAPIEndpoint string) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	if userName == "" {
		return fmt.Errorf("username is empty")
	}
	if k8sContext == "" {
		return fmt.Errorf("k8s context is empty")
	}
	if k8sAPIEndpoint == "" {
		return fmt.Errorf("k8s api endpoint is empty")
	}
	GinkgoWriter.Printf("Logging in as non-admin user=%s context=%s\n", userName, k8sContext)

	// test env convention from existing rbac suite: username == password for demo users
	return login.LoginAsNonAdmin(harness, userName, userName, k8sContext, k8sAPIEndpoint)
}

func restoreAdminLoginContext(harness *e2e.Harness, ctx context.Context, defaultK8sContext string) {
	if harness == nil {
		GinkgoWriter.Printf("Skipping admin context restore: harness is nil\n")
		return
	}
	if defaultK8sContext == "" {
		GinkgoWriter.Printf("Skipping admin context restore: default context is empty\n")
		return
	}

	_, err := harness.ChangeK8sContext(ctx, defaultK8sContext)
	if err != nil {
		GinkgoWriter.Printf("Failed to restore k8s context %q: %v\n", defaultK8sContext, err)
		return
	}

	loginMethod := login.LoginToAPIWithToken(harness)
	if loginMethod == login.AuthDisabled {
		GinkgoWriter.Printf("Auth is disabled while restoring admin login context\n")
		return
	}
	GinkgoWriter.Printf("Restored admin login context for context=%s\n", defaultK8sContext)
}

func truncateForLog(input string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(input) <= maxLen {
		return input
	}
	return input[:maxLen] + "...(truncated)"
}

func noopCleanup() {}
