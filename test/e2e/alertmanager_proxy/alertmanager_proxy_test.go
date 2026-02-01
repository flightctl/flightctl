package alertmanagerproxy_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
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
	applicationJSON   = "application/json"
	statusKindField   = "\"kind\":\"Status\""
	statusFailure     = "\"status\":\"Failure\""
	authTokenErrMsg   = "failed to get auth token"
	statusForbidden   = "Forbidden"
	nonAdminUser      = "demouser1"
	nonAdminPassword  = "demouser1"
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

	uiServiceName         = "flightctl-ui"
	uiServicePort         = 8080
	uiConfigMapName       = "flightctl-ui"
	uiConfigProxyJSONPath = "jsonpath={.data.FLIGHTCTL_ALERTMANAGER_PROXY}"
	uiExpectedProxyHost   = "alertmanager-proxy"

	httpClientTimeout = 10 * time.Second
)

var (
	testHarness           *e2e.Harness
	testRuntimeContext    string
	defaultPromNamespaces = []string{util.E2E_NAMESPACE, "flightctl", "flightctl-external"}
	defaultUWMNamespaces  = []string{"openshift-user-workload-monitoring"}
	defaultOCPNamespace   = []string{"openshift-monitoring"}
)

type alertmanagerAlertResponse struct {
	Labels map[string]string `json:"labels"`
}

// startServiceAccess exposes a service via infra and returns base URL, HTTP client, and cleanup.
func startServiceAccess(serviceName string, useTLS bool, timeout time.Duration) (string, *http.Client, func(), error) {
	p := setup.GetDefaultProviders()
	svc, ok := infra.ServiceNameFromDeploymentName(serviceName)
	if !ok {
		return "", nil, nil, fmt.Errorf("unknown service %q", serviceName)
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	baseURL, cleanup, err := p.Infra.ExposeService(svc, scheme)
	if err != nil {
		return "", nil, nil, err
	}
	transport := &http.Transport{}
	if useTLS {
		// #nosec G402 -- test code: TLS skip verify for e2e proxy
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	}
	if timeout <= 0 {
		timeout = httpClientTimeout
	}
	client := &http.Client{Timeout: timeout, Transport: transport}
	return baseURL, client, cleanup, nil
}

// getConfigValue returns a config value via infra (e.g. ConfigMap key).
func getConfigValue(name, key string) (string, error) {
	p := setup.GetDefaultProviders()
	return p.Infra.GetConfigValue(name, key)
}

// startFirstAvailableBackendAccess tries each backend via infra and returns the first that is ready.
func startFirstAvailableBackendAccess(h *e2e.Harness, backends []e2e.ServiceAccessBackend, timeout time.Duration) (baseURL string, client *http.Client, token string, cleanup func(), used e2e.ServiceAccessBackend, err error) {
	for _, backend := range backends {
		baseURL, client, cleanup, err := startServiceAccess(backend.ServiceName, backend.UseTLS, timeout)
		if err != nil {
			continue
		}
		token := ""
		if backend.RequireAuth {
			token, err = h.GetClientAccessToken()
			if err != nil {
				cleanup()
				continue
			}
		}
		// Wait for backend to be queryable
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			_, qerr := h.PromQueryWithToken(baseURL, "1", token)
			if qerr == nil {
				return baseURL, client, token, cleanup, backend, nil
			}
			time.Sleep(250 * time.Millisecond)
		}
		cleanup()
	}
	return "", nil, "", nil, e2e.ServiceAccessBackend{}, fmt.Errorf("unable to resolve backend from known candidates")
}

var prometheusBackends = []e2e.ServiceAccessBackend{
	{
		ServiceName: prometheusServiceName,
		Namespaces:  defaultPromNamespaces,
		Port:        prometheusServicePort,
		UseTLS:      false,
		RequireAuth: false,
	},
	{
		ServiceName: "prometheus-k8s",
		Namespaces:  defaultOCPNamespace,
		Port:        9091,
		UseTLS:      true,
		RequireAuth: true,
	},
	{
		ServiceName: "prometheus-user-workload",
		Namespaces:  defaultUWMNamespaces,
		Port:        9091,
		UseTLS:      true,
		RequireAuth: true,
	},
}

var _ = Describe("Alertmanager proxy", func() {
	BeforeEach(func() {
		testHarness = e2e.GetWorkerHarness()
		p := setup.GetDefaultProviders()
		envType := p.Infra.GetEnvironmentType()
		testRuntimeContext = envType
		if envType != infra.EnvironmentKind && envType != infra.EnvironmentOCP {
			Skip(fmt.Sprintf("Kubernetes-backed test context required, got %q", envType))
		}
	})

	It("Lets a user discover and query alerts through the proxy", Label("87773", "sanity"), func() {
		By("Opening the alertmanager proxy endpoint")
		baseURL, client, cleanup, err := startServiceAccess(alertmanagerProxyServiceName, true, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start proxy service access")
		defer cleanup()

		By("Verifying public health and status endpoints are reachable")
		Eventually(testHarness.StatusCodePollerWithExpectedBody(client, baseURL, proxyHealthPath, "", healthBodyOK, http.StatusOK), util.TIMEOUT, util.POLLING).
			Should(Equal(http.StatusOK), "expected /health to return %d with body %q", http.StatusOK, healthBodyOK)
		Eventually(testHarness.JSONStatusCodePoller(client, baseURL, proxyStatusPath, "", applicationJSON, http.StatusOK), util.TIMEOUT, util.POLLING).
			Should(Equal(http.StatusOK), "expected /api/v2/status to return %d with JSON content type", http.StatusOK)

		By("Resolving the current organization context used by the logged-in user")
		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve organization id")
		Expect(orgID).ToNot(BeEmpty(), "organization id should not be empty")

		By("Ensuring an unauthenticated user cannot read alerts")
		alertsPath := buildAlertsFilterPath(orgID, "", "")
		noAuthStatusCode, noAuthBody, err := testHarness.HTTPGet(client, baseURL, alertsPath, "")
		Expect(err).ToNot(HaveOccurred(), "unauthenticated alerts request failed for path %q", alertsPath)
		Expect(noAuthStatusCode).To(Equal(http.StatusBadRequest), "expected unauthenticated /alerts to return %d", http.StatusBadRequest)
		Expect(noAuthBody).ToNot(BeEmpty(), "unauthenticated /alerts response body should not be empty")
		Expect(noAuthBody).To(ContainSubstring(statusKindField), "unauthenticated /alerts response should include status kind")
		Expect(noAuthBody).To(ContainSubstring(statusFailure), "unauthenticated /alerts response should indicate failure")
		Expect(noAuthBody).To(ContainSubstring(authTokenErrMsg), "unauthenticated /alerts response should mention missing auth token")

		By("Authenticating and reading alerts for the same organization")
		authMethod := login.LoginToAPIWithToken(testHarness)
		if authMethod == login.AuthDisabled {
			Skip(authDisabledMsg)
		}
		authOrgID, authToken, err := testHarness.ResolveOrganizationAndClientToken()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve authenticated org/token context")
		Expect(authOrgID).ToNot(BeEmpty(), "authenticated organization id should not be empty")
		Expect(authToken).ToNot(BeEmpty(), "authenticated token should not be empty")

		authAlertsPath := buildAlertsFilterPath(authOrgID, "", "")
		withAuthStatusCode, withAuthBody, err := testHarness.HTTPGet(client, baseURL, authAlertsPath, authToken)
		Expect(err).ToNot(HaveOccurred(), "authenticated alerts request failed for path %q", authAlertsPath)
		Expect(withAuthStatusCode).To(Equal(http.StatusOK), "expected authenticated /alerts to return %d", http.StatusOK)
		Expect(withAuthBody).ToNot(BeEmpty(), "authenticated /alerts response body should not be empty")
		Expect(json.Valid([]byte(withAuthBody))).To(BeTrue(), "authenticated /alerts response body should be valid JSON")

		By("Returning a clear validation error for malformed organization filters")
		invalidPath := buildAlertsFilterPath(proxyInvalidOrgID, "", "")
		statusCode, body, err := testHarness.HTTPGet(client, baseURL, invalidPath, authToken)
		Expect(err).ToNot(HaveOccurred(), "malformed org_id request failed for path %q", invalidPath)
		Expect(statusCode).To(Equal(http.StatusBadRequest), "expected malformed org_id request to return %d", http.StatusBadRequest)
		Expect(body).ToNot(BeEmpty(), "malformed org_id response body should not be empty")
		Expect(body).To(ContainSubstring("invalid org_id format in filter"), "malformed org_id response should describe invalid org_id")
		Expect(body).To(ContainSubstring("invalid UUID length"), "malformed org_id response should include UUID length validation details")

		By("Letting a user filter alerts by common alert categories")
		for _, alertName := range []string{alertNameDeviceDisconnected, alertNameDeviceDiskWarning} {
			filteredPath := buildAlertsFilterPath(authOrgID, alertName, "")
			filterStatusCode, filterBody, err := testHarness.HTTPGet(client, baseURL, filteredPath, authToken)
			Expect(err).ToNot(HaveOccurred(), "alert filter request failed for alertname=%q", alertName)
			Expect(filterStatusCode).To(Equal(http.StatusOK), "expected alert filter request to return %d for alertname=%q", http.StatusOK, alertName)
			Expect(filterBody).ToNot(BeEmpty(), "alert filter response should not be empty for alertname=%q", alertName)
			Expect(json.Valid([]byte(filterBody))).To(BeTrue(), "alert filter response should be valid JSON for alertname=%q", alertName)

			alerts, err := parseAlertsResponse(filterBody)
			Expect(err).ToNot(HaveOccurred(), "failed to parse alert filter response for alertname=%q", alertName)
			for _, alert := range alerts {
				Expect(alert.Labels).ToNot(BeNil(), "alert labels should not be nil")
				if name, ok := alert.Labels[alertsLabelName]; ok {
					Expect(name).To(Equal(alertName), "alertname label mismatch")
				}
				if id, ok := alert.Labels[alertsLabelOrgID]; ok {
					Expect(id).To(Equal(authOrgID), "org_id label mismatch")
				}
			}
		}
	})

	It("denies authenticated user without alerts permission", Label("87776", "sanity"), func() {
		if testRuntimeContext != infra.EnvironmentOCP {
			Skip("non-admin authz-denied flow currently validated on OCP context")
		}

		baseURL, client, cleanup, err := startServiceAccess(alertmanagerProxyServiceName, true, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start proxy service access")
		defer cleanup()

		defaultK8sContext, k8sAPIEndpoint, err := testHarness.ResolveClusterLoginContext(testHarness.Context)
		if err != nil {
			Skip(fmt.Sprintf("unable to resolve cluster login context for non-admin authz test: %v", err))
		}

		err = login.LoginAsNonAdmin(testHarness, nonAdminUser, nonAdminPassword, defaultK8sContext, k8sAPIEndpoint)
		if err != nil {
			Skip(fmt.Sprintf("unable to login as non-admin user %q for authz-denied test: %v", nonAdminUser, err))
		}
		defer func() {
			restoreErr := restoreAdminLoginContext(testHarness, testHarness.Context, defaultK8sContext)
			Expect(restoreErr).ToNot(HaveOccurred(), "failed to restore admin login context")
		}()

		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve organization id")
		Expect(orgID).ToNot(BeEmpty(), "organization id should not be empty")

		token, err := testHarness.GetClientAccessToken()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve non-admin token")
		Expect(token).ToNot(BeEmpty(), "non-admin token should not be empty")

		alertsPath := buildAlertsFilterPath(orgID, "", "")
		statusCode, body, err := testHarness.HTTPGet(client, baseURL, alertsPath, token)
		Expect(err).ToNot(HaveOccurred(), "non-admin alerts request failed")
		Expect(statusCode).To(Equal(http.StatusForbidden), "expected non-admin alerts request to return %d", http.StatusForbidden)
		Expect(body).ToNot(BeEmpty(), "forbidden response body should not be empty")
		Expect(body).To(ContainSubstring(statusForbidden), "forbidden response should include %q", statusForbidden)
	})

	It("verifies Prometheus query path for common alert series", Label("87775"), func() {
		authMethod := login.LoginToAPIWithToken(testHarness)
		if authMethod == login.AuthDisabled {
			Skip(authDisabledMsg)
		}

		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve organization id")
		Expect(orgID).ToNot(BeEmpty(), "organization id should not be empty")

		promURL, _, promToken, cleanup, usedBackend, err := startFirstAvailableBackendAccess(testHarness, prometheusBackends, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start prometheus backend access")
		defer cleanup()
		Expect(usedBackend.ServiceName).ToNot(BeEmpty(), "selected prometheus backend name should not be empty")
		Expect(promURL).ToNot(BeEmpty(), "prometheus base URL should not be empty")

		query := fmt.Sprintf(prometheusQueryAlerts, orgID, alertNameDeviceDisconnected, alertNameDeviceDiskWarning)
		resp, err := testHarness.PromQueryWithToken(promURL, query, promToken)
		Expect(err).ToNot(HaveOccurred(), "prometheus query failed for %q", query)
		Expect(resp.Status).To(Equal(prometheusStatusOK), "expected prometheus query status %q", prometheusStatusOK)
		Expect(resp.Data.Result).ToNot(BeNil(), "prometheus query should return a valid result array")
	})

	It("verifies UI visibility wiring for alertmanager-proxy", Label("87774"), func() {
		proxyURL, err := getConfigValue(uiConfigMapName, "FLIGHTCTL_ALERTMANAGER_PROXY")
		if err != nil {
			Skip(fmt.Sprintf("ui config unavailable for this environment: %v", err))
		}
		Expect(strings.TrimSpace(proxyURL)).ToNot(BeEmpty(), "UI proxy URL should not be empty")
		Expect(strings.ToLower(proxyURL)).To(ContainSubstring(uiExpectedProxyHost), "UI proxy URL should contain %q", uiExpectedProxyHost)

		uiBaseURL, uiClient, cleanup, err := startServiceAccess(uiServiceName, false, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start UI service access")
		defer cleanup()

		statusCode, body, contentType, err := testHarness.HTTPGetWithContentType(uiClient, uiBaseURL, "/", "")
		Expect(err).ToNot(HaveOccurred(), "UI root request failed")
		Expect(statusCode).To(BeElementOf(http.StatusOK, http.StatusBadRequest), "expected UI root status to be 200 or 400")
		Expect(body).ToNot(BeEmpty(), "UI root response body should not be empty")
		if contentType != "" {
			Expect(contentType).To(
				Or(
					ContainSubstring("text/html"),
					ContainSubstring(applicationJSON),
					ContainSubstring("text/plain"),
				),
				"unexpected UI root content type %q",
				contentType,
			)
		}
	})
})

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
	return result, nil
}

func restoreAdminLoginContext(h *e2e.Harness, ctx context.Context, defaultK8sContext string) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if defaultK8sContext == "" {
		return fmt.Errorf("default context is empty")
	}

	err := h.RestoreK8sContext(ctx, defaultK8sContext)
	if err != nil {
		return fmt.Errorf("failed to restore k8s context %q: %w", defaultK8sContext, err)
	}

	loginMethod := login.LoginToAPIWithToken(h)
	if loginMethod == login.AuthDisabled {
		return fmt.Errorf("auth is disabled while restoring admin login context")
	}
	return nil
}
