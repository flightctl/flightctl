package authprovider_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	quadletinfra "github.com/flightctl/flightctl/test/e2e/infra/quadlet"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/html"
	"sigs.k8s.io/yaml"
)

const (
	keycloakTestUser             = "testuser"
	keycloakTestPass             = "testpass"
	keycloakDuplicateName        = "keycloak-e2e-duplicate"
	keycloakLifecycleName        = "keycloak-e2e-lifecycle"
	keycloakLifecycleClientID    = "flightctl-client-lifecycle"
	keycloakOAuth2ProviderName   = "keycloak-oauth2-e2e"
	keycloakOAuth2ClientID       = "flightctl-oauth2-client"
	keycloakAccountAudience      = "account"
	defaultOrganizationName      = "default"
	defaultAdminRole             = "flightctl-admin"
	openshiftDefaultUsername     = "kubeadmin"
	pamDefaultUsername           = "admin"
	pamDefaultPassword           = "flightctl-e2e"
	defaultCypressLoginScript    = "cypress/run-provider-login-cypress.sh"
	providerVisibilityArg        = "--show-providers"
	loginInsecureTLSArg          = "--insecure-skip-tls-verify"
	aapConfigSkipMessage         = "AAP quadlet tests require AAP_API_URL and either AAP_CLIENT_ID or AAP_TOKEN"
	aapCredentialSkipMessage     = "AAP browser login requires AAP_USERNAME and AAP_PASSWORD"
	openshiftPasswordMessage     = "OPENSHIFT_PASSWORD or KUBEADMIN_PASS must be set for OpenShift browser login"
	duplicateOIDCErrorSubstring  = "same issuer and clientId already exists"
	cypressMissingSubstring      = "Cypress is not installed"
	npmMissingSubstring          = "npm is not available"
	loginFlowTimeout             = 5 * time.Minute
	loginFormHTTPTimeout         = loginFlowTimeout
	authProviderLifecycleTimeout = 30 * time.Second
	authProviderPollingInterval  = 2 * time.Second
)

var loginURLRe = regexp.MustCompile(`(?:Opening login URL in default browser|Please open this URL in your browser):\s*(.+)`)

// browserLoginScenario defines the provider-specific inputs needed by the browser-login helpers.
type browserLoginScenario struct {
	name         string
	providerName string
	providerUI   string
	username     string
	password     string
}

type quadletAAPConfig struct {
	APIURL           string
	AuthorizationURL string
	TokenURL         string
	ClientID         string
	Token            string
	AppName          string
	OrganizationID   int
}

type quadletAAPClientIDSnapshot struct {
	exists  bool
	content string
}

var _ = Describe("Auth provider browser login", func() {
	var harness *e2e.Harness

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
	})

	Context("dynamic OIDC provider on kind and quadlet", func() {
		It("logs in via OAuth --web --no-browser with Keycloak and can call the API", Label("88539", "authprovider"), func() {
			ctx, cancel := context.WithTimeout(harness.GetTestContext(), loginFlowTimeout)
			defer cancel()

			By("starting a Keycloak-backed browser login flow from the CLI")
			apiEndpoint := harness.ApiEndpoint()
			err := runProviderBrowserLoginFlowWithAuthRateRetry(ctx, harness, apiEndpoint, keycloakAuthProviderName, keycloakTestUser, keycloakTestPass)
			Expect(err).ToNot(HaveOccurred(), "Keycloak browser login should complete successfully")

			By("calling the devices API through the logged-in CLI session")
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "get devices after Keycloak login should succeed")
			Expect(strings.TrimSpace(out)).ToNot(BeEmpty(), "get devices after Keycloak login should produce CLI output")
		})

		It("validates dynamic OIDC provider visibility and enable-disable lifecycle", Label("88168", "authprovider"), func() {
			apiEndpoint := harness.ApiEndpoint()
			providerPath := authProviderManifestPath(keycloakLifecycleName)

			By("showing the bootstrap Keycloak provider in login --show-providers")
			out, err := showLoginProviders(harness, apiEndpoint)
			Expect(err).ToNot(HaveOccurred(), "login --show-providers should succeed")
			Expect(out).To(ContainSubstring(keycloakAuthProviderName))
			Expect(out).To(ContainSubstring(auxSvcs.Keycloak.IssuerURL()))

			registerAuthProviderManifestCleanup(harness, keycloakLifecycleName, providerPath)

			By("creating a disabled dynamic OIDC provider")
			out, err = writeAndApplyAuthProviderManifest(
				harness,
				providerPath,
				buildOIDCAuthProviderYAML(keycloakLifecycleName, auxSvcs.Keycloak.IssuerURL(), keycloakLifecycleClientID, auxiliary.KeycloakE2EClientSecret, false),
			)
			Expect(err).ToNot(HaveOccurred(), "apply disabled authprovider")
			Expect(out).To(ContainSubstring(keycloakLifecycleName), "apply disabled authprovider should mention provider name")

			By("verifying a disabled provider is not shown for login")
			Eventually(providerShownInLogin(harness, apiEndpoint, keycloakLifecycleName, false)).
				WithTimeout(authProviderLifecycleTimeout).WithPolling(authProviderPollingInterval).
				Should(BeTrue(), "disabled provider must not appear in login --show-providers")

			By("enabling the provider without redeploying the service")
			out, err = writeAndApplyAuthProviderManifest(
				harness,
				providerPath,
				buildOIDCAuthProviderYAML(keycloakLifecycleName, auxSvcs.Keycloak.IssuerURL(), keycloakLifecycleClientID, auxiliary.KeycloakE2EClientSecret, true),
			)
			Expect(err).ToNot(HaveOccurred(), "apply enabled authprovider")
			Expect(out).To(ContainSubstring(keycloakLifecycleName), "apply enabled authprovider should mention provider name")

			By("verifying the enabled provider becomes available for login")
			Eventually(providerShownInLogin(harness, apiEndpoint, keycloakLifecycleName, true)).
				WithTimeout(authProviderLifecycleTimeout).WithPolling(authProviderPollingInterval).
				Should(BeTrue(), "enabled provider must appear in login --show-providers")

			By("deleting the provider and confirming it disappears")
			out, err = deleteAuthProviderByName(harness, keycloakLifecycleName)
			Expect(err).ToNot(HaveOccurred(), "delete authprovider")
			Expect(out).To(ContainSubstring(keycloakLifecycleName), "delete authprovider should mention provider name")

			Eventually(providerShownInLogin(harness, apiEndpoint, keycloakLifecycleName, false)).
				WithTimeout(authProviderLifecycleTimeout).WithPolling(authProviderPollingInterval).
				Should(BeTrue(), "deleted provider must disappear from login --show-providers")
		})

		It("rejects duplicate dynamic OIDC provider configuration", Label("88165", "authprovider"), func() {
			providerPath := authProviderManifestPath(keycloakDuplicateName)

			DeferCleanup(os.Remove, providerPath)

			By("attempting to create a second provider with the same issuer and client ID")
			out, err := writeAndApplyAuthProviderManifest(
				harness,
				providerPath,
				buildOIDCAuthProviderYAML(keycloakDuplicateName, auxSvcs.Keycloak.IssuerURL(), "flightctl-client", auxiliary.KeycloakE2EClientSecret, true),
			)
			Expect(err).To(HaveOccurred(), "duplicate authprovider creation must fail")
			Expect(out).To(
				ContainSubstring(duplicateOIDCErrorSubstring),
				"duplicate provider creation should report a duplicate issuer/clientId conflict",
			)
		})

		It("logs in through a dynamically created OAuth2 provider and can call the API", Label("88167", "authprovider"), func() {
			ctx, cancel := context.WithTimeout(harness.GetTestContext(), loginFlowTimeout)
			defer cancel()

			providerPath := authProviderManifestPath(keycloakOAuth2ProviderName)
			registerAuthProviderManifestCleanup(harness, keycloakOAuth2ProviderName, providerPath)

			By("creating a dynamic Keycloak-backed OAuth2 provider")
			out, err := writeAndApplyAuthProviderManifest(
				harness,
				providerPath,
				buildKeycloakOAuth2AuthProviderYAML(
					keycloakOAuth2ProviderName,
					auxSvcs.Keycloak.IssuerURL(),
					keycloakOAuth2ClientID,
					auxiliary.KeycloakE2EOAuth2ClientSecret,
				),
			)
			Expect(err).ToNot(HaveOccurred(), "apply OAuth2 authprovider")
			Expect(out).To(ContainSubstring(keycloakOAuth2ProviderName), "apply OAuth2 authprovider should mention provider name")

			apiEndpoint := harness.ApiEndpoint()
			By("verifying the OAuth2 provider becomes available for login")
			Eventually(providerShownInLogin(harness, apiEndpoint, keycloakOAuth2ProviderName, true)).
				WithTimeout(authProviderLifecycleTimeout).WithPolling(authProviderPollingInterval).
				Should(BeTrue(), "OAuth2 provider must appear in login --show-providers")

			By("starting an OAuth2 browser login flow from the CLI")
			err = runProviderBrowserLoginFlowWithAuthRateRetry(ctx, harness, apiEndpoint, keycloakOAuth2ProviderName, keycloakTestUser, keycloakTestPass)
			Expect(err).ToNot(HaveOccurred(), "Keycloak OAuth2 browser login should complete successfully")

			By("calling the devices API through the logged-in CLI session")
			out, err = harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "get devices after OAuth2 login should succeed")
			Expect(strings.TrimSpace(out)).ToNot(BeEmpty(), "get devices after OAuth2 login should produce CLI output")
		})
	})

	Context("OpenShift auth on OCP", func() {
		It("is visible as an available auth provider", Label("83576", "authprovider"), func() {
			if infra.DetectEnvironment() != infra.EnvironmentOCP {
				Skip(fmt.Sprintf("OpenShift provider only applies to OCP deployments (current environment: %s)", infra.DetectEnvironment()))
			}

			By("checking that OpenShift appears in --show-providers")
			apiEndpoint := harness.ApiEndpoint()
			out, err := showLoginProviders(harness, apiEndpoint)
			Expect(err).ToNot(HaveOccurred(), "login --show-providers should succeed")
			Expect(strings.ToLower(out)).To(ContainSubstring("openshift"), "OpenShift provider should be listed")
		})

		It("logs in through the browser and can call the API", Label("83576", "authprovider"), func() {
			if infra.DetectEnvironment() != infra.EnvironmentOCP {
				Skip(fmt.Sprintf("OpenShift browser login only applies to OCP deployments (current environment: %s)", infra.DetectEnvironment()))
			}

			scenario := openshiftBrowserScenario()
			Expect(scenario.password).ToNot(BeEmpty(), openshiftPasswordMessage+". Set these environment variables to test OpenShift browser login flow.")

			ctx, cancel := context.WithTimeout(harness.GetTestContext(), loginFlowTimeout)
			defer cancel()

			By("logging in through the OpenShift browser flow")
			apiEndpoint := harness.ApiEndpoint()
			Expect(runLoginWithCypressHarnessOrSkip(ctx, harness, apiEndpoint, scenario)).To(Succeed())

			By("calling the devices API through the logged-in CLI session")
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "get devices after OpenShift login should succeed")
			Expect(strings.TrimSpace(out)).ToNot(BeEmpty(), "get devices after OpenShift login should produce CLI output")
		})
	})

	Context("bundled PAM issuer on quadlet", func() {
		It("logs in through the browser and can call the API", Label("authprovider", "quadlets"), func() {
			infra.SkipIfNotQuadlet("PAM issuer browser login only applies to quadlet deployments")

			ctx, cancel := context.WithTimeout(harness.GetTestContext(), loginFlowTimeout)
			defer cancel()

			By("logging in through the bundled PAM browser flow")
			apiEndpoint := harness.ApiEndpoint()
			Expect(runLoginWithCypressHarnessOrSkip(ctx, harness, apiEndpoint, pamBrowserScenario())).To(Succeed())

			By("calling the devices API through the logged-in CLI session")
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "get devices after PAM login should succeed")
			Expect(strings.TrimSpace(out)).ToNot(BeEmpty(), "get devices after PAM login should produce CLI output")
		})
	})

	Context("AAP auth on quadlet", func() {
		It("is visible as an available auth provider", Label("authprovider", "quadlets"), func() {
			infra.SkipIfNotQuadlet("AAP provider only applies to quadlet deployments")
			Expect(ensureQuadletAAPProviderConfiguredForCurrentSpec()).To(Succeed())

			By("checking that AAP appears in --show-providers")
			apiEndpoint := harness.ApiEndpoint()
			out, err := showLoginProviders(harness, apiEndpoint)
			Expect(err).ToNot(HaveOccurred(), "login --show-providers should succeed")
			Expect(strings.ToLower(out)).To(ContainSubstring("aap"), "AAP provider should be listed")
		})

		It("logs in through the browser and can call the API", Label("authprovider", "quadlets"), func() {
			infra.SkipIfNotQuadlet("AAP browser login only applies to quadlet deployments")

			scenario := aapBrowserScenario()
			if scenario.username == "" || scenario.password == "" {
				Skip(aapCredentialSkipMessage + ". Set these environment variables to test AAP browser login flow. " +
					"Credentials must match a user in the AAP instance configured in the quadlet deployment.")
			}
			Expect(ensureQuadletAAPProviderConfiguredForCurrentSpec()).To(Succeed())

			ctx, cancel := context.WithTimeout(harness.GetTestContext(), loginFlowTimeout)
			defer cancel()

			By("logging in through the AAP browser flow")
			apiEndpoint := harness.ApiEndpoint()
			Expect(runLoginWithCypressHarnessOrSkip(ctx, harness, apiEndpoint, scenario)).To(Succeed())

			By("calling the devices API through the logged-in CLI session")
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "get devices after AAP login should succeed")
			Expect(strings.TrimSpace(out)).ToNot(BeEmpty(), "get devices after AAP login should produce CLI output")
		})
	})
})

// openshiftBrowserScenario returns the browser-login inputs for the OpenShift provider flow.
func openshiftBrowserScenario() browserLoginScenario {
	username := os.Getenv("OPENSHIFT_USERNAME")
	if username == "" {
		username = openshiftDefaultUsername
	}

	password := os.Getenv("OPENSHIFT_PASSWORD")
	if password == "" {
		password = os.Getenv("KUBEADMIN_PASS")
	}

	return browserLoginScenario{
		name:       "openshift",
		providerUI: "openshift",
		username:   username,
		password:   password,
	}
}

// pamBrowserScenario returns the browser-login inputs for the bundled PAM provider flow.
func pamBrowserScenario() browserLoginScenario {
	username := os.Getenv("E2E_PAM_USER")
	if username == "" {
		username = pamDefaultUsername
	}

	password := os.Getenv("E2E_PAM_PASSWORD")
	if password == "" {
		password = os.Getenv("E2E_DEFAULT_PAM_PASSWORD")
	}
	if password == "" {
		password = pamDefaultPassword //nolint:gosec // G101: test-only fallback password
	}

	return browserLoginScenario{
		name:       "pam",
		providerUI: "pam",
		username:   username,
		password:   password,
	}
}

// aapBrowserScenario returns the browser-login inputs for the AAP provider flow.
func aapBrowserScenario() browserLoginScenario {
	return browserLoginScenario{
		name:       "aap",
		providerUI: "aap",
		username:   strings.TrimSpace(os.Getenv("AAP_USERNAME")),
		password:   strings.TrimSpace(os.Getenv("AAP_PASSWORD")),
	}
}

func ensureQuadletAAPProviderConfiguredForCurrentSpec() error {
	aapConfig, skipMessage, err := quadletAAPConfigFromEnv()
	if err != nil {
		return err
	}
	if skipMessage != "" {
		Skip(skipMessage + ". Set AAP_API_URL and either AAP_CLIENT_ID or AAP_TOKEN for the quadlet deployment.")
	}
	return configureQuadletAAPProviderForTest(aapConfig)
}

func quadletAAPConfigFromEnv() (*quadletAAPConfig, string, error) {
	apiURL := strings.TrimSpace(os.Getenv("AAP_API_URL"))
	clientID := strings.TrimSpace(os.Getenv("AAP_CLIENT_ID"))
	token := strings.TrimSpace(os.Getenv("AAP_TOKEN"))
	if apiURL == "" || (clientID == "" && token == "") {
		return nil, aapConfigSkipMessage, nil
	}

	organizationID := 0
	if raw := strings.TrimSpace(os.Getenv("AAP_ORGANIZATION_ID")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, "", fmt.Errorf("parse AAP_ORGANIZATION_ID %q: %w", raw, err)
		}
		organizationID = parsed
	}

	return &quadletAAPConfig{
		APIURL:           apiURL,
		AuthorizationURL: firstNonEmpty(strings.TrimSpace(os.Getenv("AAP_AUTHORIZATION_URL")), defaultAAPAuthorizationURL(apiURL)),
		TokenURL:         firstNonEmpty(strings.TrimSpace(os.Getenv("AAP_TOKEN_URL")), defaultAAPTokenURL(apiURL)),
		ClientID:         clientID,
		Token:            token,
		AppName:          strings.TrimSpace(os.Getenv("AAP_APP_NAME")),
		OrganizationID:   organizationID,
	}, "", nil
}

func configureQuadletAAPProviderForTest(aapConfig *quadletAAPConfig) error {
	if aapConfig == nil {
		return fmt.Errorf("AAP config is required")
	}

	providers := setup.GetDefaultProviders()
	if providers == nil || providers.Infra == nil || providers.Lifecycle == nil {
		return fmt.Errorf("quadlet providers are not initialized")
	}

	quadletProvider, ok := providers.Infra.(*quadletinfra.InfraProvider)
	if !ok {
		return fmt.Errorf("infra provider %T is not quadlet", providers.Infra)
	}

	originalConfig, err := quadletProvider.GetStandaloneServiceConfig()
	if err != nil {
		return fmt.Errorf("read standalone service config: %w", err)
	}
	clientIDSnapshot, err := captureQuadletAAPClientIDSnapshot(quadletProvider)
	if err != nil {
		return fmt.Errorf("capture AAP client ID snapshot: %w", err)
	}

	DeferCleanup(func() {
		if err := restoreQuadletAAPClientIDSnapshot(quadletProvider, clientIDSnapshot); err != nil {
			logrus.Warnf("[authprovider] failed to restore AAP client ID file: %v", err)
		}
		if err := quadletProvider.SetStandaloneServiceConfig(originalConfig); err != nil {
			logrus.Warnf("[authprovider] failed to restore standalone service config: %v", err)
			return
		}
		if err := providers.Lifecycle.Restart(infra.ServiceAPI); err != nil {
			logrus.Warnf("[authprovider] failed to restart API after restoring standalone service config: %v", err)
			return
		}
		if err := providers.Lifecycle.WaitForReady(infra.ServiceAPI, loginFlowTimeout); err != nil {
			logrus.Warnf("[authprovider] API not ready after restoring standalone service config: %v", err)
		}
	})

	updatedConfig, err := withQuadletAAPServiceConfig(originalConfig, aapConfig)
	if err != nil {
		return err
	}
	if err := quadletProvider.SetStandaloneServiceConfig(updatedConfig); err != nil {
		return fmt.Errorf("write standalone service config: %w", err)
	}
	if aapConfig.Token != "" && aapConfig.ClientID == "" {
		if _, err := quadletProvider.RunCommand("flightctl-standalone", "aap", "create-oauth-application"); err != nil {
			return fmt.Errorf("create AAP OAuth application: %w", err)
		}
	}
	if err := providers.Lifecycle.Restart(infra.ServiceAPI); err != nil {
		return fmt.Errorf("restart API with AAP config: %w", err)
	}
	if err := providers.Lifecycle.WaitForReady(infra.ServiceAPI, loginFlowTimeout); err != nil {
		return fmt.Errorf("wait for API after AAP config: %w", err)
	}

	return nil
}

func withQuadletAAPServiceConfig(configYAML string, aapConfig *quadletAAPConfig) (string, error) {
	if aapConfig == nil {
		return "", fmt.Errorf("AAP config is required")
	}

	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &root); err != nil {
		return "", fmt.Errorf("parse standalone service config: %w", err)
	}

	global := ensureNestedMap(root, "global")
	auth := ensureNestedMap(global, "auth")
	aap := ensureNestedMap(auth, "aap")

	auth["type"] = "aap"
	auth["insecureSkipTlsVerify"] = true

	if pam, ok := auth["pamOidcIssuer"].(map[string]interface{}); ok {
		pam["enabled"] = false
	}

	aap["enabled"] = true
	aap["apiUrl"] = aapConfig.APIURL
	aap["authorizationUrl"] = aapConfig.AuthorizationURL
	aap["tokenUrl"] = aapConfig.TokenURL

	if aapConfig.ClientID != "" {
		aap["clientId"] = aapConfig.ClientID
	} else {
		delete(aap, "clientId")
	}

	if aapConfig.Token != "" {
		aap["token"] = aapConfig.Token
	} else {
		delete(aap, "token")
	}

	if aapConfig.AppName != "" {
		aap["appName"] = aapConfig.AppName
	} else {
		delete(aap, "appName")
	}

	if aapConfig.OrganizationID > 0 {
		aap["organizationId"] = aapConfig.OrganizationID
	} else {
		delete(aap, "organizationId")
	}

	updated, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("marshal standalone service config: %w", err)
	}
	return string(updated), nil
}

func ensureNestedMap(root map[string]interface{}, key string) map[string]interface{} {
	if existing, ok := root[key].(map[string]interface{}); ok {
		return existing
	}

	created := map[string]interface{}{}
	root[key] = created
	return created
}

func defaultAAPAuthorizationURL(apiURL string) string {
	return strings.TrimRight(apiURL, "/") + "/o/authorize/"
}

func defaultAAPTokenURL(apiURL string) string {
	return strings.TrimRight(apiURL, "/") + "/o/token/"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func captureQuadletAAPClientIDSnapshot(provider *quadletinfra.InfraProvider) (*quadletAAPClientIDSnapshot, error) {
	if provider == nil {
		return nil, fmt.Errorf("quadlet provider is required")
	}

	content, err := provider.ReadHostFile(renderer.DefaultAAPClientIDPath)
	if err != nil {
		if isMissingHostFileError(err) {
			return &quadletAAPClientIDSnapshot{}, nil
		}
		return nil, err
	}

	return &quadletAAPClientIDSnapshot{
		exists:  true,
		content: content,
	}, nil
}

func restoreQuadletAAPClientIDSnapshot(provider *quadletinfra.InfraProvider, snapshot *quadletAAPClientIDSnapshot) error {
	if provider == nil {
		return fmt.Errorf("quadlet provider is required")
	}
	if snapshot == nil {
		return fmt.Errorf("AAP client ID snapshot is required")
	}

	if !snapshot.exists {
		return provider.RemoveHostFile(renderer.DefaultAAPClientIDPath)
	}

	return provider.WriteHostFile(renderer.DefaultAAPClientIDPath, []byte(snapshot.content))
}

func isMissingHostFileError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "No such file or directory")
}

// authProviderManifestPath returns the temp-file path used for a provider manifest in this suite.
func authProviderManifestPath(providerName string) string {
	return filepath.Join(os.TempDir(), providerName+".yaml")
}

// registerAuthProviderManifestCleanup removes a dynamic authprovider and its manifest after the spec finishes.
func registerAuthProviderManifestCleanup(harness *e2e.Harness, providerName, providerPath string) {
	DeferCleanup(cleanupAuthProviderManifest, harness, providerName, providerPath)
}

// cleanupAuthProviderManifest removes a dynamic authprovider and its manifest file.
func cleanupAuthProviderManifest(harness *e2e.Harness, providerName, providerPath string) {
	if harness != nil && providerName != "" {
		if out, err := deleteAuthProviderByName(harness, providerName); err != nil {
			logrus.Warnf("[authprovider] cleanup delete failed for %s: %v\n%s", providerName, err, out)
		}
	}
	if providerPath != "" {
		if err := os.Remove(providerPath); err != nil && !os.IsNotExist(err) {
			logrus.Warnf("[authprovider] cleanup remove manifest failed for %s: %v", providerPath, err)
		}
	}
}

// showLoginProviders runs `flightctl login --show-providers` for the current API endpoint.
func showLoginProviders(harness *e2e.Harness, apiEndpoint string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("worker harness is required")
	}
	if strings.TrimSpace(apiEndpoint) == "" {
		return "", fmt.Errorf("api endpoint is required")
	}

	return harness.CLI("login", apiEndpoint, loginInsecureTLSArg, providerVisibilityArg)
}

// providerShownInLogin returns a poll function that reports whether a provider is visible in login output.
func providerShownInLogin(harness *e2e.Harness, apiEndpoint, providerName string, wantVisible bool) func() (bool, error) {
	return func() (bool, error) {
		out, err := showLoginProviders(harness, apiEndpoint)
		if err != nil {
			return false, err
		}
		isVisible := strings.Contains(out, providerName)
		return isVisible == wantVisible, nil
	}
}

// writeAndApplyAuthProviderManifest writes a provider manifest to disk and applies it through the harness.
func writeAndApplyAuthProviderManifest(harness *e2e.Harness, providerPath, providerYAML string) (string, error) {
	if strings.TrimSpace(providerPath) == "" {
		return "", fmt.Errorf("provider manifest path is required")
	}
	if strings.TrimSpace(providerYAML) == "" {
		return "", fmt.Errorf("provider manifest content is required")
	}
	if err := os.WriteFile(providerPath, []byte(providerYAML), 0600); err != nil {
		return "", fmt.Errorf("write authprovider manifest %q: %w", providerPath, err)
	}
	if harness == nil {
		return "", fmt.Errorf("worker harness is required")
	}
	return harness.ApplyResource(providerPath)
}

// deleteAuthProviderByName deletes a dynamic authprovider through the harness resource API.
func deleteAuthProviderByName(harness *e2e.Harness, providerName string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("worker harness is required")
	}
	if strings.TrimSpace(providerName) == "" {
		return "", fmt.Errorf("provider name is required")
	}
	return harness.ManageResource("delete", "authprovider", providerName)
}

// runLoginWithCypressHarness runs the suite-local Cypress harness for browser-based provider login.
func runLoginWithCypressHarness(ctx context.Context, harness *e2e.Harness, apiEndpoint string, scenario browserLoginScenario) error {
	if harness == nil {
		return fmt.Errorf("worker harness is required")
	}
	if strings.TrimSpace(apiEndpoint) == "" {
		return fmt.Errorf("api endpoint is required")
	}
	if strings.TrimSpace(scenario.name) == "" {
		return fmt.Errorf("browser login scenario name is required")
	}
	if strings.TrimSpace(scenario.providerUI) == "" {
		return fmt.Errorf("browser login provider UI is required for scenario %q", scenario.name)
	}

	scriptPath, err := resolveCypressScriptPath(defaultCypressLoginScript)
	if err != nil {
		return err
	}

	scriptDir := filepath.Dir(scriptPath)
	cmdArgs := []string{apiEndpoint, scenario.providerName, scenario.providerUI, scenario.username, scenario.password}
	cmd := exec.CommandContext(ctx, scriptPath, cmdArgs...) //nolint:gosec // G204: scriptPath is the suite-owned Cypress launcher after path validation
	cmd.Dir = filepath.Dir(scriptDir)
	cmd.Env = append(os.Environ(),
		"FLIGHTCTL="+harness.GetFlightctlPath(),
		"API_ENDPOINT="+apiEndpoint,
	)

	sanitizedArgs := sanitizeCommandArgsForLog(cmdArgs, scenario.password)
	logrus.Infof("[authprovider] running Cypress login helper for %s: %s %s", scenario.name, scriptPath, strings.Join(sanitizedArgs, " "))
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		logrus.Infof("[authprovider] cypress login output:\n%s", string(out))
	}
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("run cypress login helper %q for %s: %w\n%s", scriptPath, scenario.name, err, string(out))
		}
		return fmt.Errorf("run cypress login helper %q for %s: %w", scriptPath, scenario.name, err)
	}
	return nil
}

// runLoginWithCypressHarnessOrSkip runs the Cypress harness and skips when browser test dependencies are unavailable.
func runLoginWithCypressHarnessOrSkip(ctx context.Context, harness *e2e.Harness, apiEndpoint string, scenario browserLoginScenario) error {
	err := runLoginWithCypressHarness(ctx, harness, apiEndpoint, scenario)
	if isCypressUnavailableError(err) {
		Skip(err.Error())
	}
	return err
}

// isCypressUnavailableError reports whether the suite-local browser helper cannot run because Cypress and npm are unavailable.
func isCypressUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	errText := err.Error()
	return strings.Contains(errText, cypressMissingSubstring) && strings.Contains(errText, npmMissingSubstring)
}

// runProviderLoginCLIWithURL starts `flightctl login ... --web --no-browser` for the given provider.
// When ctx is cancelled, the process is killed. Returns the printed auth URL and a channel
// that receives the process exit error when the CLI exits. done is nil when err != nil.
func runProviderLoginCLIWithURL(ctx context.Context, harness *e2e.Harness, apiEndpoint, providerName string) (authURL string, done <-chan error, err error) {
	if harness == nil {
		return "", nil, fmt.Errorf("worker harness is required")
	}
	if strings.TrimSpace(apiEndpoint) == "" {
		return "", nil, fmt.Errorf("api endpoint is required")
	}
	if strings.TrimSpace(providerName) == "" {
		return "", nil, fmt.Errorf("provider name is required")
	}
	callbackPort, err := harness.GetFreeLocalPort()
	if err != nil {
		return "", nil, fmt.Errorf("reserve callback port for provider %s: %w", providerName, err)
	}

	args := []string{
		"login", apiEndpoint,
		"--insecure-skip-tls-verify",
		"--provider", providerName,
		"--web", "--no-browser",
		"--callback-port", strconv.Itoa(callbackPort),
	}
	flightctlPath := harness.GetFlightctlPath()
	logrus.Infof("[authprovider] provider login command for %s: %s %s (env: API_ENDPOINT=%s)", providerName, flightctlPath, strings.Join(args, " "), apiEndpoint)
	cmd := exec.CommandContext(ctx, flightctlPath, args...)
	cmd.Env = append(os.Environ(), "API_ENDPOINT="+apiEndpoint)

	stdoutPipe, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return "", nil, pipeErr
	}
	stderrPipe, pipeErr := cmd.StderrPipe()
	if pipeErr != nil {
		_ = stdoutPipe.Close()
		if stdoutWriter, ok := cmd.Stdout.(io.Closer); ok {
			_ = stdoutWriter.Close()
		}
		cmd.Stdout = nil
		return "", nil, pipeErr
	}

	if err = cmd.Start(); err != nil {
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return "", nil, err
	}

	var copyWg sync.WaitGroup
	copyWg.Add(2)
	var stderrBuf bytes.Buffer
	go func() {
		defer copyWg.Done()
		if _, copyErr := io.Copy(&stderrBuf, stderrPipe); copyErr != nil {
			logrus.Warnf("[authprovider] failed to capture login command stderr: %v", copyErr)
		}
	}()

	var stdoutBuf bytes.Buffer
	urlCh := make(chan string, 1)
	go func() {
		defer copyWg.Done()
		urlSent := false
		scanner := bufio.NewScanner(io.TeeReader(stdoutPipe, &stdoutBuf))
		for scanner.Scan() {
			line := scanner.Text()
			if match := loginURLRe.FindStringSubmatch(line); !urlSent && len(match) > 1 {
				urlCh <- strings.TrimSpace(match[1])
				urlSent = true
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			logrus.Warnf("[authprovider] failed to scan login command stdout: %v", scanErr)
		}
	}()

	doneCh := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		copyWg.Wait()
		if waitErr != nil {
			waitErr = loginCommandErrorWithOutput(waitErr, stdoutBuf.String(), stderrBuf.String())
		}
		doneCh <- waitErr
		close(doneCh)
	}()

	logCommandError := func(what string, err error) {
		logrus.Errorf("[authprovider] %s: %v", what, err)
		if stderrBuf.Len() > 0 {
			logrus.Errorf("[authprovider] login command stderr:\n%s", redactAuthProviderCredentials(stderrBuf.String()))
		}
		if stdoutBuf.Len() > 0 {
			logrus.Errorf("[authprovider] login command stdout:\n%s", redactAuthProviderCredentials(stdoutBuf.String()))
		}
	}

	waitPipesThenLog := func(what string, err error) (string, <-chan error, error) {
		pipeDone := make(chan struct{})
		go func() { copyWg.Wait(); close(pipeDone) }()
		select {
		case <-pipeDone:
		case <-time.After(500 * time.Millisecond):
		}
		logCommandError(what, err)
		return "", nil, err
	}

	select {
	case authURL = <-urlCh:
		return authURL, doneCh, nil
	case waitErr := <-doneCh:
		return waitPipesThenLog("login command exited before printing URL", fmt.Errorf("login command exited: %w", waitErr))
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_, _, e := waitPipesThenLog("login command failed (context cancelled)", ctx.Err())
		return "", nil, e
	case <-time.After(loginFlowTimeout):
		_ = cmd.Process.Kill()
		_, _, e := waitPipesThenLog("timeout waiting for login URL", fmt.Errorf("timeout waiting for login URL"))
		return "", nil, e
	}
}

// loginCommandErrorWithOutput wraps a login command error with sanitized captured output.
func loginCommandErrorWithOutput(err error, stdout, stderr string) error {
	return fmt.Errorf("login command failed: %w\nstdout:\n%s\nstderr:\n%s", err, redactAuthProviderCredentials(stdout), redactAuthProviderCredentials(stderr))
}

// runProviderBrowserLoginFlowWithAuthRateRetry completes a browser login and retries once after an auth rate-limit reset.
func runProviderBrowserLoginFlowWithAuthRateRetry(ctx context.Context, harness *e2e.Harness, apiEndpoint, providerName, username, password string) error {
	err := runProviderBrowserLoginFlow(ctx, harness, apiEndpoint, providerName, username, password)
	if !isLoginRateLimitError(err) {
		return err
	}
	logrus.Infof("[authprovider] login hit API auth rate limit; restarting API once before retry")
	if resetErr := restartAPIForAuthRateLimitReset(); resetErr != nil {
		return fmt.Errorf("reset auth rate limit after browser login failure: %w; original error: %v", resetErr, err)
	}
	return runProviderBrowserLoginFlow(ctx, harness, apiEndpoint, providerName, username, password)
}

// runProviderBrowserLoginFlow starts the CLI web flow, submits the auth form, and waits for callback completion.
func runProviderBrowserLoginFlow(ctx context.Context, harness *e2e.Harness, apiEndpoint, providerName, username, password string) error {
	authURL, done, err := runProviderLoginCLIWithURL(ctx, harness, apiEndpoint, providerName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(authURL) == "" {
		return fmt.Errorf("provider %s login did not print an auth URL", providerName)
	}
	if err := submitAuthLoginForm(ctx, authURL, username, password); err != nil {
		return fmt.Errorf("submit auth login form for provider %s: %w", providerName, err)
	}
	return waitForProviderLoginDone(ctx, done, providerName)
}

// waitForProviderLoginDone waits for the CLI login process to finish.
func waitForProviderLoginDone(ctx context.Context, done <-chan error, providerName string) error {
	if done == nil {
		return fmt.Errorf("provider %s login completion channel is nil", providerName)
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("provider %s login command did not complete successfully: %w", providerName, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("provider %s login did not complete within timeout: %w", providerName, ctx.Err())
	}
}

func sanitizeCommandArgsForLog(args []string, password string) []string {
	if len(args) == 0 {
		return nil
	}

	sanitizedArgs := make([]string, 0, len(args))
	maskNextValue := false
	for _, arg := range args {
		switch {
		case maskNextValue:
			sanitizedArgs = append(sanitizedArgs, "<REDACTED>")
			maskNextValue = false
		case arg == "--password":
			sanitizedArgs = append(sanitizedArgs, arg)
			maskNextValue = true
		case password != "" && arg == password:
			sanitizedArgs = append(sanitizedArgs, "<REDACTED>")
		case strings.HasPrefix(arg, "--password="):
			sanitizedArgs = append(sanitizedArgs, "--password=<REDACTED>")
		default:
			sanitizedArgs = append(sanitizedArgs, arg)
		}
	}

	return sanitizedArgs
}

type loginForm struct {
	action string
	method string
	values url.Values
}

// submitAuthLoginForm submits a username/password login form and follows the OAuth redirect to the CLI callback.
func submitAuthLoginForm(ctx context.Context, authURL, username, password string) error {
	if strings.TrimSpace(authURL) == "" {
		return fmt.Errorf("auth URL is required")
	}
	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("username is required")
	}
	if password == "" {
		return fmt.Errorf("password is required")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("create login form cookie jar: %w", err)
	}
	httpClient := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			Proxy: nil,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // E2E follows self-signed local auth endpoints.
				MinVersion:         tls.VersionTLS12,
			},
		},
		Timeout: loginFormHTTPTimeout,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return fmt.Errorf("create login form request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("get login form %s: %w", sanitizedAuthURLForLog(authURL), err)
	}
	defer resp.Body.Close()
	if strings.Contains(resp.Request.URL.Path, "/callback") {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read login form response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("login form request returned %d from %s: %s", resp.StatusCode, sanitizedAuthURLForLog(resp.Request.URL.String()), loginFormResponseSnippet(body, username, password))
	}

	form, err := parseLoginForm(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("parse login form from %s: %w; response snippet: %s", sanitizedAuthURLForLog(resp.Request.URL.String()), err, loginFormResponseSnippet(body, username, password))
	}

	formURL, err := resolveLoginFormAction(resp.Request.URL, form.action)
	if err != nil {
		return fmt.Errorf("resolve login form action: %w", err)
	}
	if err := validateLoginFormTarget(resp.Request.URL, formURL); err != nil {
		return err
	}
	form.values.Set("username", username)
	form.values.Set("password", password)

	method := strings.ToUpper(strings.TrimSpace(form.method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost {
		return fmt.Errorf("unsupported login form method %q", method)
	}

	logrus.Infof("[authprovider] submitting auth login form: %s", sanitizedAuthURLForLog(formURL.String()))
	postReq, err := http.NewRequestWithContext(ctx, method, formURL.String(), strings.NewReader(form.values.Encode()))
	if err != nil {
		return fmt.Errorf("create login form submit request: %w", err)
	}
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Referer", resp.Request.URL.String())

	submitResp, err := httpClient.Do(postReq)
	if err != nil {
		return fmt.Errorf("submit login form %s: %w", sanitizedAuthURLForLog(formURL.String()), err)
	}
	defer submitResp.Body.Close()

	body, err = io.ReadAll(submitResp.Body)
	if err != nil {
		return fmt.Errorf("read login form submit response: %w", err)
	}
	if submitResp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("login form submit returned %d: %s", submitResp.StatusCode, redactAuthProviderCredentials(string(body), username, password))
	}
	if !strings.Contains(submitResp.Request.URL.Path, "/callback") {
		redirectURL, ok, err := loginRedirectFromBody(formURL, body)
		if err != nil {
			return fmt.Errorf("parse login redirect response: %w", err)
		}
		if ok {
			if err := validateLoginFormTarget(formURL, redirectURL); err != nil {
				return err
			}
			redirectReq, err := http.NewRequestWithContext(ctx, http.MethodGet, redirectURL.String(), nil)
			if err != nil {
				return fmt.Errorf("create post-login redirect request: %w", err)
			}
			redirectResp, err := httpClient.Do(redirectReq)
			if err != nil {
				return fmt.Errorf("follow post-login redirect %s: %w", sanitizedAuthURLForLog(redirectURL.String()), err)
			}
			defer redirectResp.Body.Close()
			if redirectResp.StatusCode >= http.StatusBadRequest {
				body, readErr := io.ReadAll(redirectResp.Body)
				if readErr != nil {
					return fmt.Errorf("read post-login redirect response: %w", readErr)
				}
				return fmt.Errorf("post-login redirect returned %d: %s", redirectResp.StatusCode, redactAuthProviderCredentials(string(body), username, password))
			}
			if !strings.Contains(redirectResp.Request.URL.Path, "/callback") {
				return fmt.Errorf("post-login redirect did not reach callback; final URL %s", sanitizedAuthURLForLog(redirectResp.Request.URL.String()))
			}
			return nil
		}
		if looksLikeLoginForm(body) {
			return fmt.Errorf("login form submit did not reach callback; final URL %s still looks like a login page", sanitizedAuthURLForLog(submitResp.Request.URL.String()))
		}
	}
	return nil
}

// parseLoginForm returns the first form with username and password fields from an HTML document.
func parseLoginForm(reader io.Reader) (*loginForm, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}
	form := findLoginForm(doc)
	if form == nil {
		return nil, fmt.Errorf("login form with username and password fields was not found")
	}
	return form, nil
}

// findLoginForm walks an HTML tree and returns the first username/password form.
func findLoginForm(node *html.Node) *loginForm {
	if node == nil {
		return nil
	}
	if node.Type == html.ElementNode && node.Data == "form" {
		form := loginForm{
			method: "POST",
			values: url.Values{},
		}
		for _, attr := range node.Attr {
			switch strings.ToLower(attr.Key) {
			case "action":
				form.action = attr.Val
			case "method":
				form.method = attr.Val
			}
		}
		collectFormInputs(node, form.values)
		usernameField := firstExistingFormField(form.values, "username", "user", "login", "email")
		passwordField := firstExistingFormField(form.values, "password", "passwd")
		if usernameField != "" && passwordField != "" {
			normalizeLoginFormField(form.values, usernameField, "username")
			normalizeLoginFormField(form.values, passwordField, "password")
			return &form
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if form := findLoginForm(child); form != nil {
			return form
		}
	}
	return nil
}

// collectFormInputs adds named input default values to the form submission payload.
func collectFormInputs(node *html.Node, values url.Values) {
	if node == nil {
		return
	}
	if node.Type == html.ElementNode && node.Data == "input" {
		var name, id, value string
		for _, attr := range node.Attr {
			switch strings.ToLower(attr.Key) {
			case "name":
				name = attr.Val
			case "id":
				id = attr.Val
			case "value":
				value = attr.Val
			}
		}
		if strings.TrimSpace(name) == "" {
			name = id
		}
		if strings.TrimSpace(name) != "" {
			values.Set(name, value)
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectFormInputs(child, values)
	}
}

// resolveLoginFormAction resolves a form action URL relative to the page that served it.
func resolveLoginFormAction(baseURL *url.URL, action string) (*url.URL, error) {
	if baseURL == nil {
		return nil, fmt.Errorf("base login URL is required")
	}
	if strings.TrimSpace(action) == "" {
		if path.Base(baseURL.Path) == "authorize" {
			parsedAction, err := url.Parse("login")
			if err != nil {
				return nil, fmt.Errorf("parse default PAM login action: %w", err)
			}
			return baseURL.ResolveReference(parsedAction), nil
		}
		return baseURL, nil
	}
	parsedAction, err := url.Parse(action)
	if err != nil {
		return nil, fmt.Errorf("parse action %q: %w", action, err)
	}
	return baseURL.ResolveReference(parsedAction), nil
}

// validateLoginFormTarget prevents the test helper from submitting credentials to a different origin than the provider page.
func validateLoginFormTarget(pageURL, targetURL *url.URL) error {
	if pageURL == nil {
		return fmt.Errorf("login page URL is required")
	}
	if targetURL == nil {
		return fmt.Errorf("login form target URL is required")
	}
	if !sameOrigin(pageURL, targetURL) {
		return fmt.Errorf("login form target %s does not match provider origin %s", sanitizedAuthURLForLog(targetURL.String()), sanitizedAuthURLForLog(pageURL.String()))
	}
	return nil
}

// sameOrigin reports whether two URLs share scheme and host.
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// loginRedirectFromBody returns the JavaScript-driven redirect URL emitted by the PAM login endpoint.
func loginRedirectFromBody(baseURL *url.URL, body []byte) (*url.URL, bool, error) {
	if baseURL == nil {
		return nil, false, fmt.Errorf("base login URL is required")
	}
	redirectText := strings.TrimSpace(string(body))
	if redirectText == "" {
		return nil, false, nil
	}
	if !strings.HasPrefix(redirectText, "http://") &&
		!strings.HasPrefix(redirectText, "https://") &&
		!strings.HasPrefix(redirectText, "/") &&
		!strings.HasPrefix(redirectText, "authorize?") {
		return nil, false, nil
	}
	redirectURL, err := url.Parse(redirectText)
	if err != nil {
		return nil, false, fmt.Errorf("parse redirect URL %q: %w", sanitizedAuthURLForLog(redirectText), err)
	}
	return baseURL.ResolveReference(redirectURL), true, nil
}

// looksLikeLoginForm reports whether an HTML response still contains username and password fields.
func looksLikeLoginForm(body []byte) bool {
	return strings.Contains(string(body), `name="username"`) && strings.Contains(string(body), `name="password"`)
}

// firstExistingFormField returns the first requested form key present in values.
func firstExistingFormField(values url.Values, candidates ...string) string {
	for _, candidate := range candidates {
		if _, ok := values[candidate]; ok {
			return candidate
		}
	}
	return ""
}

// normalizeLoginFormField copies a provider-specific field name to the expected generic field name.
func normalizeLoginFormField(values url.Values, from, to string) {
	if from == to {
		return
	}
	current := values.Get(from)
	values.Del(from)
	values.Set(to, current)
}

// loginFormResponseSnippet returns a short sanitized response body snippet for form parsing diagnostics.
func loginFormResponseSnippet(body []byte, sensitiveValues ...string) string {
	snippet := strings.TrimSpace(redactAuthProviderCredentials(string(body), sensitiveValues...))
	snippet = strings.Join(strings.Fields(snippet), " ")
	if len(snippet) > 500 {
		return snippet[:500] + "..."
	}
	return snippet
}

// sanitizedAuthURLForLog returns the auth URL origin/path without OAuth query or fragment session data.
func sanitizedAuthURLForLog(authURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(authURL))
	if err != nil {
		return "<invalid auth URL>"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// redactAuthProviderCredentials redacts credential-looking values from captured command output.
func redactAuthProviderCredentials(output string, exactValues ...string) string {
	if output == "" {
		return ""
	}

	redacted := output
	for _, value := range exactValues {
		if strings.TrimSpace(value) != "" {
			redacted = strings.ReplaceAll(redacted, value, "<REDACTED>")
		}
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b((?:[A-Z_]*USERNAME|[A-Z_]*PASSWORD|authProviderUsername|authProviderPassword)\b[[:space:]]*[:=][[:space:]]*)("[^"]*"|'[^']*'|[^[:space:]]+)`),
		regexp.MustCompile(`(?i)([[:space:]]-[up][[:space:]]+)([^[:space:]]+)`),
		regexp.MustCompile(`(?i)(--(?:username|password)(?:=|[[:space:]]+))([^[:space:]]+)`),
		regexp.MustCompile(`(?i)([?&](?:access_token|code|id_token|nonce|refresh_token|session_state|state|token)=)([^&#[:space:]]+)`),
	}
	for _, pattern := range patterns {
		redacted = pattern.ReplaceAllString(redacted, `${1}<REDACTED>`)
	}
	return redacted
}

// resolveCypressScriptPath resolves the configured Cypress harness path to an executable file.
func resolveCypressScriptPath(script string) (string, error) {
	if filepath.IsAbs(script) {
		if _, err := os.Stat(script); err != nil {
			return "", fmt.Errorf("stat cypress login helper %q: %w", script, err)
		}
		return script, nil
	}

	if strings.ContainsRune(script, os.PathSeparator) {
		absPath := filepath.Join(authProviderPackageDir(), script)
		if _, err := os.Stat(absPath); err != nil {
			return "", fmt.Errorf("stat cypress login helper %q: %w", absPath, err)
		}
		return absPath, nil
	}

	scriptPath, err := exec.LookPath(script)
	if err != nil {
		return "", fmt.Errorf("resolve cypress login helper %q: %w", script, err)
	}
	return scriptPath, nil
}

// authProviderPackageDir returns the absolute path to this package directory.
func authProviderPackageDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(file)
}

// buildOIDCAuthProviderYAML renders a dynamic OIDC authprovider manifest for this suite.
func buildOIDCAuthProviderYAML(name, issuerURL, clientID, clientSecret string, enabled bool) string {
	return fmt.Sprintf(`apiVersion: flightctl.io/v1beta1
kind: AuthProvider
metadata:
  name: %s
spec:
  providerType: oidc
  displayName: %s
  issuer: %s
  clientId: %s
  clientSecret: %s
  enabled: %t
  scopes:
    - openid
    - profile
    - email
  usernameClaim:
    - preferred_username
  organizationAssignment:
    type: static
    organizationName: %s
  roleAssignment:
    type: static
    roles:
      - %s
`, name, name, issuerURL, clientID, clientSecret, enabled, defaultOrganizationName, defaultAdminRole)
}

// buildKeycloakOAuth2AuthProviderYAML renders a Keycloak-backed dynamic OAuth2 authprovider manifest for this suite.
func buildKeycloakOAuth2AuthProviderYAML(name, issuerURL, clientID, clientSecret string) string {
	authorizationURL := issuerURL + "/protocol/openid-connect/auth"
	tokenURL := issuerURL + "/protocol/openid-connect/token"
	userinfoURL := issuerURL + "/protocol/openid-connect/userinfo"
	jwksURL := issuerURL + "/protocol/openid-connect/certs"

	return fmt.Sprintf(`apiVersion: flightctl.io/v1beta1
kind: AuthProvider
metadata:
  name: %s
spec:
  providerType: oauth2
  displayName: %s
  issuer: %s
  authorizationUrl: %s
  tokenUrl: %s
  userinfoUrl: %s
  clientId: %s
  clientSecret: %s
  enabled: true
  scopes:
    - openid
    - profile
    - email
  usernameClaim:
    - preferred_username
  introspection:
    type: jwt
    jwksUrl: %s
    audience:
      - %s
      - %s
  organizationAssignment:
    type: static
    organizationName: %s
  roleAssignment:
    type: static
    roles:
      - %s
`, name, name, issuerURL, authorizationURL, tokenURL, userinfoURL, clientID, clientSecret, jwksURL, clientID, keycloakAccountAudience, defaultOrganizationName, defaultAdminRole)
}
