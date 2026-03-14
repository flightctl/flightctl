package alertmanagerproxy_test

import (
	"encoding/json"
	"errors"
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
	errHarnessNil     = "harness is nil"
	errHTTPClientNil  = "http client is nil"
	errBaseURLEmpty   = "base URL is empty"
	errOrgIDEmpty     = "organization id should not be empty"
)

var (
	testHarness            *e2e.Harness
	testProviders          *infra.Providers
	testRuntimeContext     string
	defaultProxyNamespaces = []string{"flightctl-external", "flightctl", util.E2E_NAMESPACE}
	defaultUINamespaces    = []string{"flightctl-external", "flightctl", util.E2E_NAMESPACE}
	defaultPromNamespaces  = []string{util.E2E_NAMESPACE, "flightctl", "flightctl-external"}
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
		testProviders = setup.GetDefaultProviders()
		Expect(testProviders).ToNot(BeNil(), "default test providers should be initialized")
		envType := testProviders.Infra.GetEnvironmentType()
		testRuntimeContext = envType
		if envType != infra.EnvironmentKind && envType != infra.EnvironmentOCP {
			Skip(fmt.Sprintf("Kubernetes-backed test context required, got %q", envType))
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
		Expect(orgID).ToNot(BeEmpty(), errOrgIDEmpty)

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
		authMethod, err := login.LoginToAPIWithToken(testHarness)
		Expect(err).ToNot(HaveOccurred())
		authOrgID, authToken, err := resolveAuthenticatedAlertsContext(testHarness, authMethod)
		Expect(err).ToNot(HaveOccurred(), "failed to resolve authenticated org/token context")
		Expect(authOrgID).ToNot(BeEmpty(), "authenticated organization id should not be empty")
		Expect(authToken).ToNot(BeEmpty(), "authenticated token should not be empty")

		authAlertsPath := buildAlertsFilterPath(authOrgID, "", "")
		withAuthStatusCode, withAuthBody, authToken, err := getAlertsWithWorkingAuthToken(testHarness, client, baseURL, authAlertsPath, authToken)
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

		baseURL, client, cleanup, err := testHarness.StartServiceAccess(alertmanagerProxyServiceName, defaultProxyNamespaces, alertmanagerProxyServicePort, true, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start proxy service access")
		defer cleanup()

		clusterToken, _, err := login.LoginToEnv(testHarness, nonAdminUser, nonAdminPassword, "")
		if err != nil {
			Skip(fmt.Sprintf("unable to login as non-admin user %q for authz-denied test: %v", nonAdminUser, err))
		}
		err = login.LoginToFlightctl(testHarness, clusterToken)
		Expect(err).ToNot(HaveOccurred(), "failed to log non-admin user into flightctl")
		DeferCleanup(restoreAdminLoginForTest, testHarness)

		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve organization id")
		Expect(orgID).ToNot(BeEmpty(), errOrgIDEmpty)

		Expect(clusterToken).ToNot(BeEmpty(), "non-admin cluster token should not be empty")

		alertsPath := buildAlertsFilterPath(orgID, "", "")
		statusCode, body, err := testHarness.HTTPGet(client, baseURL, alertsPath, clusterToken)
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
		_, err := login.LoginToAPIWithToken(testHarness)
		Expect(err).ToNot(HaveOccurred())

		orgID, err := testHarness.GetOrganizationID()
		Expect(err).ToNot(HaveOccurred(), "failed to resolve organization id")
		Expect(orgID).ToNot(BeEmpty(), errOrgIDEmpty)

		query := fmt.Sprintf(prometheusQueryAlerts, orgID, alertNameDeviceDisconnected, alertNameDeviceDiskWarning)
		promURL, _, promToken, cleanup, usedBackend, err := testHarness.StartFirstAvailableBackendAccessWithQuery(prometheusBackends, query, httpClientTimeout)
		Expect(err).ToNot(HaveOccurred(), "failed to start prometheus backend access")
		defer cleanup()
		Expect(usedBackend.ServiceName).ToNot(BeEmpty(), "selected prometheus backend name should not be empty")
		Expect(promURL).ToNot(BeEmpty(), "prometheus base URL should not be empty")

		resp, err := testHarness.PromQueryWithToken(promURL, query, promToken)
		Expect(err).ToNot(HaveOccurred(), "prometheus query failed for %q", query)
		Expect(resp.Status).To(Equal(prometheusStatusOK), "expected prometheus query status %q", prometheusStatusOK)
		Expect(resp.Data.Result).ToNot(BeNil(), "prometheus query should return a valid result array")
	})

	It("verifies UI visibility wiring for alertmanager-proxy", Label("87774"), func() {
		proxyURL, err := testProviders.Infra.GetConfigValue(uiConfigMapName, "FLIGHTCTL_ALERTMANAGER_PROXY")
		if err != nil {
			Skip(fmt.Sprintf("ui config unavailable for this environment: %v", err))
		}
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

func waitForUIRootReachable(h *e2e.Harness, client *http.Client, baseURL string, statusCode *int, body *string, contentType *string) func() error {
	return func() error {
		if h == nil {
			return errors.New(errHarnessNil)
		}
		if client == nil {
			return errors.New(errHTTPClientNil)
		}
		if strings.TrimSpace(baseURL) == "" {
			return errors.New(errBaseURLEmpty)
		}
		if statusCode == nil || body == nil || contentType == nil {
			return fmt.Errorf("response output holders are nil")
		}
		s, b, c, reqErr := h.HTTPGetWithContentType(client, baseURL, "/", "")
		if reqErr != nil {
			return reqErr
		}
		if s != http.StatusOK && s != http.StatusBadRequest {
			return fmt.Errorf("ui root not ready yet: unexpected status %d", s)
		}
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("ui root not ready yet: empty body")
		}
		if c != "" {
			normalized := strings.ToLower(c)
			if !strings.Contains(normalized, "text/html") &&
				!strings.Contains(normalized, applicationJSON) &&
				!strings.Contains(normalized, "text/plain") {
				return fmt.Errorf("ui root not ready yet: unexpected content type %q", c)
			}
		}
		*statusCode = s
		*body = b
		*contentType = c
		return nil
	}
}

func resolveAuthenticatedAlertsContext(h *e2e.Harness, authMethod login.AuthMethod) (string, string, error) {
	if h == nil {
		return "", "", errors.New(errHarnessNil)
	}
	if testProviders == nil {
		return "", "", fmt.Errorf("default providers are not initialized")
	}
	if err := h.RefreshClient(); err != nil {
		return "", "", fmt.Errorf("failed to refresh client before resolving authenticated context: %w", err)
	}

	orgID, err := h.GetOrganizationID()
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve organization id: %w", err)
	}
	if strings.TrimSpace(orgID) == "" {
		return "", "", fmt.Errorf("organization id is empty")
	}

	token, tokenErr := h.GetClientAccessToken()
	if tokenErr == nil && strings.TrimSpace(token) != "" {
		return orgID, token, nil
	}

	if authMethod == login.AuthToken {
		token, err = testProviders.Infra.GetAPILoginToken()
		if err == nil && strings.TrimSpace(token) != "" {
			return orgID, token, nil
		}
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve authenticated token: client token error (%v), cluster token error (%w)", tokenErr, err)
		}
	}

	if tokenErr != nil {
		return "", "", fmt.Errorf("failed to resolve authenticated token: %w", tokenErr)
	}
	return "", "", fmt.Errorf("authenticated token is empty")
}

func getAlertsWithWorkingAuthToken(
	h *e2e.Harness,
	client *http.Client,
	baseURL, path string,
	preferredToken string,
) (int, string, string, error) {
	if h == nil {
		return 0, "", "", errors.New(errHarnessNil)
	}
	if client == nil {
		return 0, "", "", errors.New(errHTTPClientNil)
	}
	if strings.TrimSpace(baseURL) == "" {
		return 0, "", "", errors.New(errBaseURLEmpty)
	}
	if strings.TrimSpace(path) == "" {
		return 0, "", "", fmt.Errorf("path is empty")
	}
	if testProviders == nil {
		return 0, "", "", fmt.Errorf("default providers are not initialized")
	}

	tokenCandidates := make([]string, 0, 3)
	tokenCandidates = appendUniqueNonEmptyToken(tokenCandidates, preferredToken)
	if token, err := h.GetClientAccessToken(); err == nil {
		tokenCandidates = appendUniqueNonEmptyToken(tokenCandidates, token)
	}
	if token, err := testProviders.Infra.GetAPILoginToken(); err == nil {
		tokenCandidates = appendUniqueNonEmptyToken(tokenCandidates, token)
	}
	if len(tokenCandidates) == 0 {
		return 0, "", "", fmt.Errorf("no candidate auth tokens available for authenticated alerts request")
	}

	var lastStatus int
	var lastBody string
	for _, token := range tokenCandidates {
		status, body, err := h.HTTPGet(client, baseURL, path, token)
		if err != nil {
			return 0, "", "", err
		}
		if status == http.StatusOK {
			return status, body, token, nil
		}
		lastStatus = status
		lastBody = body
		if status != http.StatusUnauthorized {
			return status, body, token, nil
		}
	}

	if lastStatus == http.StatusUnauthorized {
		return lastStatus, lastBody, tokenCandidates[len(tokenCandidates)-1], fmt.Errorf(
			"authenticated alerts request remained unauthorized after trying %d token candidates; last body: %q",
			len(tokenCandidates),
			truncateForError(lastBody, 200),
		)
	}
	return lastStatus, lastBody, tokenCandidates[len(tokenCandidates)-1], nil
}

func appendUniqueNonEmptyToken(existing []string, token string) []string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return existing
	}
	for _, current := range existing {
		if current == trimmed {
			return existing
		}
	}
	return append(existing, trimmed)
}

func truncateForError(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if max <= 0 || len(trimmed) <= max {
		return trimmed
	}
	return trimmed[:max] + "..."
}

func restoreAdminLoginForTest(h *e2e.Harness) error {
	if h == nil {
		return errors.New(errHarnessNil)
	}
	if _, err := login.LoginToAPIWithToken(h); err != nil {
		return fmt.Errorf("failed to restore admin login: %w", err)
	}
	return nil
}
