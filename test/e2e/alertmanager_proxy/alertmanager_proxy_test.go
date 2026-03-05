package alertmanagerproxy_test

import (
	"context"
	"encoding/json"
	"fmt"
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

	proxyHealthPath          = "/health"
	proxyStatusPath          = "/api/v2/status"
	proxyAlertsPath          = "/api/v2/alerts"
	proxyInvalidOrgID        = "not-a-uuid"
	healthBodyOK             = "OK"
	applicationJSON          = "application/json"
	statusKindField          = "\"kind\":\"Status\""
	statusFailure            = "\"status\":\"Failure\""
	authTokenErrMsg          = "failed to get auth token"
	statusForbidden          = "Forbidden"
	nonAdminUser             = "demouser1"
	nonAdminPassword         = "demouser1"
	denyServiceAccountPrefix = "am-proxy-deny"
	authDisabledMsg          = "auth is disabled in this environment"
	alertsLabelName          = "alertname"
	alertsLabelOrgID         = "org_id"
	alertsLabelRes           = "resource"

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
	testHarness            *e2e.Harness
	defaultProxyNamespaces = []string{"flightctl-external", "flightctl", util.E2E_NAMESPACE}
	defaultPromNamespaces  = []string{util.E2E_NAMESPACE, "flightctl", "flightctl-external"}
	defaultUINamespaces    = []string{"flightctl-external", "flightctl", util.E2E_NAMESPACE}
	defaultUWMNamespaces   = []string{"openshift-user-workload-monitoring"}
	defaultOCPNamespace    = []string{"openshift-monitoring"}
)

type alertmanagerAlertResponse struct {
	Labels map[string]string `json:"labels"`
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

		ctxName, err := e2e.GetContext()
		Expect(err).ToNot(HaveOccurred())
		if ctxName != util.KIND && ctxName != util.OCP {
			Skip(fmt.Sprintf("Kubernetes-backed test context required, got %q", ctxName))
		}
		if ctxName == util.OCP {
			_, _, err = testHarness.ResolveClusterLoginContext(testHarness.Context)
			Expect(err).ToNot(HaveOccurred(), "failed to resolve cluster login context")
		}
	})

	It("Lets a user discover and query alerts through the proxy", Label("87773", "sanity"), func() {
		By("Opening the alertmanager proxy endpoint")
		baseURL, client, cleanup, err := testHarness.StartServiceAccess(alertmanagerProxyServiceName, defaultProxyNamespaces, alertmanagerProxyServicePort, true, httpClientTimeout)
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
		authOrgID, authToken, err := resolveAuthenticatedAlertsContext(testHarness, authMethod)
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
		proxyNamespace, err := testHarness.ResolveServiceNamespace(alertmanagerProxyServiceName, defaultProxyNamespaces)
		if err != nil {
			Skip(fmt.Sprintf("unable to resolve proxy namespace for authz-denied test: %v", err))
		}

		baseURL, client, cleanup, err := testHarness.StartServiceAccess(alertmanagerProxyServiceName, []string{proxyNamespace}, alertmanagerProxyServicePort, true, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start proxy service access")
		defer cleanup()

		defaultK8sContext, err := testHarness.SH("kubectl", "config", "current-context")
		Expect(err).ToNot(HaveOccurred(), "failed to resolve current admin k8s context")
		defaultK8sContext = strings.TrimSpace(defaultK8sContext)
		Expect(defaultK8sContext).ToNot(BeEmpty(), "current admin k8s context should not be empty")

		k8sAPIEndpoint, err := testHarness.GetK8sApiEndpoint(testHarness.Context, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "failed to resolve current cluster api endpoint")
		k8sAPIEndpoint = strings.TrimSpace(k8sAPIEndpoint)
		Expect(k8sAPIEndpoint).ToNot(BeEmpty(), "current cluster api endpoint should not be empty")

		DeferCleanup(restoreAdminLoginContext, testHarness, testHarness.Context, defaultK8sContext)

		token, authCleanup, err := resolveUnauthorizedAlertsToken(
			testHarness,
			nonAdminUser,
			nonAdminPassword,
			k8sAPIEndpoint,
			proxyNamespace,
			defaultK8sContext,
		)
		if err != nil {
			Skip(fmt.Sprintf("unable to resolve unauthorized token for authz-denied test: %v", err))
		}
		DeferCleanup(authCleanup)
		Expect(token).ToNot(BeEmpty(), "non-admin token should not be empty")

		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve organization id")
		Expect(orgID).ToNot(BeEmpty(), "organization id should not be empty")

		alertsPath := buildAlertsFilterPath(orgID, "", "")
		statusCode, body, err := testHarness.HTTPGet(client, baseURL, alertsPath, token)
		Expect(err).ToNot(HaveOccurred(), "non-admin alerts request failed")
		Expect(statusCode).To(
			BeElementOf(http.StatusForbidden, http.StatusUnauthorized),
			"expected non-admin alerts request to return %d or %d",
			http.StatusForbidden,
			http.StatusUnauthorized,
		)
		Expect(body).ToNot(BeEmpty(), "forbidden response body should not be empty")
		Expect(body).To(
			Or(
				ContainSubstring(statusForbidden),
				ContainSubstring("access denied"),
				ContainSubstring("Unauthorized"),
				ContainSubstring("failed to validate token"),
			),
			"denied response should include forbidden/access-denied/unauthorized context",
			statusForbidden,
		)
	})

	It("verifies Prometheus query path for common alert series", Label("87775"), func() {
		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve organization id")
		Expect(orgID).ToNot(BeEmpty(), "organization id should not be empty")

		query := fmt.Sprintf(prometheusQueryAlerts, orgID, alertNameDeviceDisconnected, alertNameDeviceDiskWarning)
		promURL, promClient, promToken, cleanup, usedBackend, err := testHarness.StartFirstAvailableBackendAccessWithQuery(prometheusBackends, query, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start prometheus backend access")
		defer cleanup()
		Expect(usedBackend.ServiceName).ToNot(BeEmpty(), "selected prometheus backend name should not be empty")
		Expect(promURL).ToNot(BeEmpty(), "prometheus base URL should not be empty")
		Expect(promClient).ToNot(BeNil(), "prometheus client should not be nil")

		resp, err := testHarness.PromQueryWithClientToken(promClient, promURL, query, promToken)
		Expect(err).ToNot(HaveOccurred(), "prometheus query failed for %q", query)
		Expect(resp.Status).To(Equal(prometheusStatusOK), "expected prometheus query status %q", prometheusStatusOK)
		Expect(resp.Data.Result).ToNot(BeNil(), "prometheus query should return a valid result array")
	})

	It("verifies UI visibility wiring for alertmanager-proxy", Label("87774"), func() {
		uiNamespace, err := testHarness.ResolveServiceNamespace(uiServiceName, defaultUINamespaces)
		if err != nil {
			Skip(fmt.Sprintf("ui service unavailable for this environment: %v", err))
		}
		Expect(uiNamespace).ToNot(BeEmpty(), "resolved UI namespace should not be empty")

		proxyURL, err := util.GetConfigMapDataByJSONPath(uiNamespace, uiConfigMapName, uiConfigProxyJSONPath)
		Expect(err).ToNot(HaveOccurred(), "failed to resolve UI config map proxy URL")
		Expect(strings.TrimSpace(proxyURL)).ToNot(BeEmpty(), "UI proxy URL should not be empty")
		Expect(strings.ToLower(proxyURL)).To(ContainSubstring(uiExpectedProxyHost), "UI proxy URL should contain %q", uiExpectedProxyHost)

		uiBaseURL, uiClient, cleanup, err := testHarness.StartServiceAccess(uiServiceName, defaultUINamespaces, uiServicePort, false, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start UI service access")
		defer cleanup()

		statusCode := 0
		body := ""
		contentType := ""
		Eventually(waitForUIRootReachable(testHarness, uiClient, uiBaseURL, &statusCode, &body, &contentType), util.TIMEOUT, util.POLLING).
			ShouldNot(HaveOccurred(), "UI root endpoint should become reachable")

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

func waitForUIRootReachable(h *e2e.Harness, client *http.Client, baseURL string, statusCode *int, body *string, contentType *string) func() error {
	return func() error {
		if h == nil {
			return fmt.Errorf("harness is nil")
		}
		if client == nil {
			return fmt.Errorf("http client is nil")
		}
		if strings.TrimSpace(baseURL) == "" {
			return fmt.Errorf("base URL is empty")
		}
		if statusCode == nil || body == nil || contentType == nil {
			return fmt.Errorf("response output holders are nil")
		}
		s, b, c, reqErr := h.HTTPGetWithContentType(client, baseURL, "/", "")
		if reqErr != nil {
			return reqErr
		}
		*statusCode = s
		*body = b
		*contentType = c
		return nil
	}
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

func resolveAuthenticatedAlertsContext(h *e2e.Harness, authMethod login.AuthMethod) (string, string, error) {
	if h == nil {
		return "", "", fmt.Errorf("harness is nil")
	}

	orgID, err := h.GetOrganizationID()
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve organization id: %w", err)
	}
	if strings.TrimSpace(orgID) == "" {
		return "", "", fmt.Errorf("organization id is empty")
	}

	var token string
	switch authMethod {
	case login.AuthToken:
		token, err = h.GetOpenShiftToken()
		if err != nil || strings.TrimSpace(token) == "" {
			if err != nil && !isMissingOpenShiftTokenError(err) {
				return "", "", fmt.Errorf("failed to resolve authenticated token: %w", err)
			}
			token, err = h.GetClientAccessToken()
		}
	default:
		token, err = h.GetClientAccessToken()
	}
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve authenticated token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return "", "", fmt.Errorf("authenticated token is empty")
	}
	return orgID, token, nil
}

func isMissingOpenShiftTokenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "oc not found") ||
		strings.Contains(msg, "oc binary not found") ||
		strings.Contains(msg, "failed to get openshift token") ||
		strings.Contains(msg, "executable file not found")
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

	if err := h.RefreshClient(); err != nil {
		return fmt.Errorf("failed to refresh client after restoring admin login context: %w", err)
	}

	return nil
}

func resolveUnauthorizedAlertsToken(h *e2e.Harness, username, password, k8sAPIEndpoint, namespace, adminK8sContext string) (string, func(), error) {
	if h == nil {
		return "", noopCleanup, fmt.Errorf("harness is nil")
	}
	if namespace == "" {
		return "", noopCleanup, fmt.Errorf("namespace is empty")
	}
	if adminK8sContext == "" {
		return "", noopCleanup, fmt.Errorf("admin k8s context is empty")
	}

	err := login.LoginAsNonAdmin(h, username, password, adminK8sContext, k8sAPIEndpoint)
	token := ""
	if err == nil {
		token, err = h.GetClientAccessToken()
		if err != nil {
			return "", noopCleanup, fmt.Errorf("non-admin login succeeded but failed to read client access token: %w", err)
		}
	}
	if err == nil && strings.TrimSpace(token) != "" {
		return token, noopCleanup, nil
	}

	token, cleanup, saErr := createTemporaryDeniedServiceAccountToken(h, namespace, adminK8sContext)
	if saErr != nil {
		return "", noopCleanup, fmt.Errorf("failed non-admin login (%v) and failed temporary serviceaccount token fallback (%w)", err, saErr)
	}
	return token, cleanup, nil
}

func createTemporaryDeniedServiceAccountToken(h *e2e.Harness, namespace, kubeContext string) (string, func(), error) {
	if h == nil {
		return "", noopCleanup, fmt.Errorf("harness is nil")
	}
	if namespace == "" {
		return "", noopCleanup, fmt.Errorf("namespace is empty")
	}
	if kubeContext == "" {
		return "", noopCleanup, fmt.Errorf("kube context is empty")
	}

	saName := fmt.Sprintf("%s-%d", denyServiceAccountPrefix, time.Now().UnixNano())
	if _, err := h.SH("kubectl", "--context", kubeContext, "-n", namespace, "create", "serviceaccount", saName); err != nil {
		return "", noopCleanup, fmt.Errorf("failed to create temporary serviceaccount %q in namespace %q: %w", saName, namespace, err)
	}

	cleanup := func() {
		_, _ = h.SH("kubectl", "--context", kubeContext, "-n", namespace, "delete", "serviceaccount", saName, "--ignore-not-found=true")
	}

	token, err := h.SH("kubectl", "--context", kubeContext, "-n", namespace, "create", "token", saName)
	if err != nil {
		cleanup()
		return "", noopCleanup, fmt.Errorf("failed to create token for temporary serviceaccount %q: %w", saName, err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		cleanup()
		return "", noopCleanup, fmt.Errorf("temporary serviceaccount token is empty for %q", saName)
	}

	return token, cleanup, nil
}

func noopCleanup() {}
