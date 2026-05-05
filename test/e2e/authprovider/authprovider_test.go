package authprovider_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	quadletinfra "github.com/flightctl/flightctl/test/e2e/infra/quadlet"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
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
	loginURLTimeout              = 30 * time.Second
	chromedpTimeout              = 60 * time.Second
	loginFlowTimeout             = 2 * time.Minute
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
			authURL, done, err := runLoginCLIWithURL(ctx, harness, apiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			Expect(authURL).ToNot(BeEmpty())

			By("completing the Keycloak login form in a headless browser")
			err = fillKeycloakLoginForm(ctx, authURL, keycloakTestUser, keycloakTestPass)
			Expect(err).ToNot(HaveOccurred(), "chromedp Keycloak login form fill failed")

			By("waiting for the CLI callback to complete the login")
			select {
			case waitErr := <-done:
				Expect(waitErr).ToNot(HaveOccurred(), "CLI login process should exit successfully")
			case <-ctx.Done():
				Fail("CLI login did not complete within timeout")
			}

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
			authURL, done, err := runProviderLoginCLIWithURL(ctx, harness, apiEndpoint, keycloakOAuth2ProviderName)
			Expect(err).ToNot(HaveOccurred())
			Expect(authURL).ToNot(BeEmpty())

			By("completing the Keycloak login form for the OAuth2 provider")
			err = fillKeycloakLoginForm(ctx, authURL, keycloakTestUser, keycloakTestPass)
			Expect(err).ToNot(HaveOccurred(), "chromedp Keycloak login form fill failed for OAuth2 provider")

			By("waiting for the CLI callback to complete the OAuth2 login")
			select {
			case waitErr := <-done:
				Expect(waitErr).ToNot(HaveOccurred(), "CLI OAuth2 login process should exit successfully")
			case <-ctx.Done():
				Fail("CLI OAuth2 login did not complete within timeout")
			}

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
			Expect(runLoginWithCypressHarness(ctx, harness, apiEndpoint, scenario)).To(Succeed())

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
			Expect(runLoginWithCypressHarness(ctx, harness, apiEndpoint, pamBrowserScenario())).To(Succeed())

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
			Expect(runLoginWithCypressHarness(ctx, harness, apiEndpoint, scenario)).To(Succeed())

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
		return fmt.Errorf("run cypress login helper %q for %s: %w", scriptPath, scenario.name, err)
	}
	return nil
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

	args := []string{
		"login", apiEndpoint,
		"--insecure-skip-tls-verify",
		"--provider", providerName,
		"--web", "--no-browser",
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
		return "", nil, pipeErr
	}

	if err = cmd.Start(); err != nil {
		return "", nil, err
	}

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- cmd.Wait()
		close(doneCh)
	}()

	var copyWg sync.WaitGroup
	copyWg.Add(2)
	var stderrBuf bytes.Buffer
	go func() {
		defer copyWg.Done()
		_, _ = io.Copy(&stderrBuf, stderrPipe)
	}()

	var stdoutBuf bytes.Buffer
	urlCh := make(chan string, 1)
	go func() {
		defer copyWg.Done()
		scanner := bufio.NewScanner(io.TeeReader(stdoutPipe, &stdoutBuf))
		for scanner.Scan() {
			line := scanner.Text()
			if match := loginURLRe.FindStringSubmatch(line); len(match) > 1 {
				urlCh <- strings.TrimSpace(match[1])
				return
			}
		}
	}()

	logCommandError := func(what string, err error) {
		logrus.Errorf("[authprovider] %s: %v", what, err)
		if stderrBuf.Len() > 0 {
			logrus.Errorf("[authprovider] login command stderr:\n%s", stderrBuf.String())
		}
		if stdoutBuf.Len() > 0 {
			logrus.Errorf("[authprovider] login command stdout:\n%s", stdoutBuf.String())
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
	case <-time.After(loginURLTimeout):
		_ = cmd.Process.Kill()
		_, _, e := waitPipesThenLog("timeout waiting for login URL", fmt.Errorf("timeout waiting for login URL"))
		return "", nil, e
	}
}

// runLoginCLIWithURL starts `flightctl login ... --web --no-browser` for the bootstrap Keycloak OIDC provider.
func runLoginCLIWithURL(ctx context.Context, harness *e2e.Harness, apiEndpoint string) (authURL string, done <-chan error, err error) {
	return runProviderLoginCLIWithURL(ctx, harness, apiEndpoint, keycloakAuthProviderName)
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

// fillKeycloakLoginForm uses chromedp to navigate to the auth URL, fill username/password, and submit.
func fillKeycloakLoginForm(ctx context.Context, authURL, username, password string) error {
	headless := os.Getenv("E2E_HEADED") == ""
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	browserCtx, cancelTimeout := context.WithTimeout(browserCtx, chromedpTimeout)
	defer cancelTimeout()

	logrus.Infof("[authprovider] chromedp navigating to Keycloak login (headless=%v): %s", headless, authURL)
	return chromedp.Run(browserCtx,
		chromedp.Navigate(authURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.WaitVisible(`#username`, chromedp.ByQuery),
		chromedp.SendKeys(`#username`, username, chromedp.ByQuery),
		chromedp.SendKeys(`#password`, password, chromedp.ByQuery),
		chromedp.Click(`#kc-login`, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				var loc string
				if err := chromedp.Location(&loc).Do(ctx); err != nil {
					return err
				}
				if strings.Contains(loc, "/callback") {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tick.C:
				}
			}
		}),
	)
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
  organizationAssignment:
    type: static
    organizationName: %s
  roleAssignment:
    type: static
    roles:
      - %s
`, name, name, issuerURL, authorizationURL, tokenURL, userinfoURL, clientID, clientSecret, defaultOrganizationName, defaultAdminRole)
}
