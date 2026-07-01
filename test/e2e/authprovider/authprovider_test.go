package authprovider_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	quadletinfra "github.com/flightctl/flightctl/test/e2e/infra/quadlet"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
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
	keycloakClientID             = "flightctl-client"
	keycloakLifecycleClientID    = "flightctl-client-lifecycle"
	keycloakOAuth2ProviderName   = "keycloak-oauth2-e2e"
	keycloakOAuth2ClientID       = "flightctl-oauth2-client"
	defaultOrganizationName      = "default"
	defaultAdminRole             = "flightctl-admin"
	staticOIDCProviderName       = "oidc"
	openshiftProviderName        = "openshift"
	aapProviderName              = "aap"
	openshiftDefaultUsername     = "kubeadmin"
	pamDefaultUsername           = "admin"
	pamDefaultPassword           = "flightctl-e2e"
	pamProviderUIName            = "pam"
	defaultCypressLoginScript    = "cypress/run-provider-login-cypress.sh"
	providerVisibilityArg        = "--show-providers"
	loginInsecureTLSArg          = "--insecure-skip-tls-verify"
	aapConfigSkipMessage         = "AAP quadlet tests require AAP_API_URL and either AAP_CLIENT_ID or AAP_TOKEN"
	aapCredentialSkipMessage     = "AAP browser login requires AAP_USERNAME and AAP_PASSWORD"
	openshiftPasswordMessage     = "OPENSHIFT_PASSWORD or KUBEADMIN_PASS must be set for OpenShift browser login"
	duplicateOIDCErrorSubstring  = "same issuer and clientId already exists"
	oauth2LoginSuccessOutput     = "Login successful."
	keycloakAccountAudience      = "account"
	maskedSecretValue            = "*****"
	pamIssuerIdentifier          = "pam-issuer"
	cypressMissingSubstring      = "Cypress is not installed"
	npmMissingSubstring          = "npm is not available"
	openshiftOAuthClientMissing  = "No Flight Control OAuthClient matches"
	openshiftOAuthClientMultiple = "Multiple Flight Control OAuthClients match"
	publicClientPlaceholderBytes = 32
	authConfigHTTPTimeout        = 10 * time.Second
	loginURLTimeout              = 30 * time.Second
	loginPipeDrainTimeout        = 500 * time.Millisecond
	chromedpTimeout              = 60 * time.Second
	chromedpCallbackPollInterval = 100 * time.Millisecond
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

	Context("dynamic OIDC provider", func() {
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
				buildOIDCAuthProviderYAML(keycloakDuplicateName, auxSvcs.Keycloak.IssuerURL(), keycloakClientID, auxiliary.KeycloakE2EClientSecret, true),
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

			apiEndpoint := harness.ApiEndpoint()
			providerPath := authProviderManifestPath(keycloakOAuth2ProviderName)
			registerAuthProviderManifestCleanup(harness, keycloakOAuth2ProviderName, providerPath)

			authProviderYAML, usePAMOAuth2, buildErr := buildOAuth2AuthProviderForDeployment(
				ctx,
				apiEndpoint,
				keycloakOAuth2ProviderName,
				auxSvcs.Keycloak.IssuerURL(),
				keycloakOAuth2ClientID,
				auxiliary.KeycloakE2EOAuth2ClientSecret,
			)
			Expect(buildErr).ToNot(HaveOccurred(), "build OAuth2 authprovider for deployment")

			By("creating a dynamic OAuth2 provider")
			out, err := writeAndApplyAuthProviderManifest(
				harness,
				providerPath,
				authProviderYAML,
			)
			Expect(err).ToNot(HaveOccurred(), "apply OAuth2 authprovider")
			Expect(out).To(ContainSubstring(keycloakOAuth2ProviderName), "apply OAuth2 authprovider should mention provider name")

			By("verifying the OAuth2 provider becomes available for login")
			Eventually(providerShownInLogin(harness, apiEndpoint, keycloakOAuth2ProviderName, true)).
				WithTimeout(authProviderLifecycleTimeout).WithPolling(authProviderPollingInterval).
				Should(BeTrue(), "OAuth2 provider must appear in login --show-providers")

			if usePAMOAuth2 {
				By("logging in through the PAM-backed OAuth2 password flow")
				scenario := pamBrowserScenario()
				out, err = runProviderPasswordLoginCLI(ctx, harness, apiEndpoint, keycloakOAuth2ProviderName, scenario.username, scenario.password)
				Expect(err).ToNot(HaveOccurred(), "CLI OAuth2 password login process should exit successfully")
				Expect(out).To(ContainSubstring(oauth2LoginSuccessOutput), "OAuth2 password login should report a successful login")
			} else {
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
			Expect(strings.ToLower(out)).To(ContainSubstring(openshiftProviderName), "OpenShift provider should be listed")
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
			err := runLoginWithCypressHarnessOrSkip(ctx, harness, apiEndpoint, scenario)
			Expect(err).To(Succeed())

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
			err := runLoginWithCypressHarnessOrSkip(ctx, harness, apiEndpoint, pamBrowserScenario())
			Expect(err).To(Succeed())

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
			Expect(strings.ToLower(out)).To(ContainSubstring(aapProviderName), "AAP provider should be listed")
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
			err := runLoginWithCypressHarnessOrSkip(ctx, harness, apiEndpoint, scenario)
			Expect(err).To(Succeed())

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
		name:         openshiftProviderName,
		providerName: openshiftProviderName,
		providerUI:   openshiftProviderName,
		username:     username,
		password:     password,
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
		name:         pamProviderUIName,
		providerName: staticOIDCProviderName,
		providerUI:   pamProviderUIName,
		username:     username,
		password:     password,
	}
}

// aapBrowserScenario returns the browser-login inputs for the AAP provider flow.
func aapBrowserScenario() browserLoginScenario {
	return browserLoginScenario{
		name:         aapProviderName,
		providerName: aapProviderName,
		providerUI:   aapProviderName,
		username:     strings.TrimSpace(os.Getenv("AAP_USERNAME")),
		password:     strings.TrimSpace(os.Getenv("AAP_PASSWORD")),
	}
}

// ensureQuadletAAPProviderConfiguredForCurrentSpec applies the AAP auth config required by the current quadlet spec.
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

// quadletAAPConfigFromEnv builds the quadlet AAP config from environment variables or returns a skip reason.
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

// configureQuadletAAPProviderForTest updates the quadlet service config for AAP and restores it after the spec.
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

// withQuadletAAPServiceConfig returns service config YAML updated with the requested AAP provider settings.
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

// ensureNestedMap returns an existing nested map or creates one at the requested key.
func ensureNestedMap(root map[string]interface{}, key string) map[string]interface{} {
	if existing, ok := root[key].(map[string]interface{}); ok {
		return existing
	}

	created := map[string]interface{}{}
	root[key] = created
	return created
}

// defaultAAPAuthorizationURL returns the standard AAP authorization endpoint for an API URL.
func defaultAAPAuthorizationURL(apiURL string) string {
	return strings.TrimRight(apiURL, "/") + "/o/authorize/"
}

// defaultAAPTokenURL returns the standard AAP token endpoint for an API URL.
func defaultAAPTokenURL(apiURL string) string {
	return strings.TrimRight(apiURL, "/") + "/o/token/"
}

// firstNonEmpty returns the first non-empty value after trimming whitespace.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// captureQuadletAAPClientIDSnapshot records the generated AAP client ID file before a spec mutates it.
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

// restoreQuadletAAPClientIDSnapshot restores or removes the generated AAP client ID file after a spec.
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

// isMissingHostFileError reports whether a remote host file operation failed because the file is absent.
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

// fetchAuthConfig retrieves the public auth configuration from the API server.
func fetchAuthConfig(ctx context.Context, apiEndpoint string) (*api.AuthConfig, error) {
	if strings.TrimSpace(apiEndpoint) == "" {
		return nil, fmt.Errorf("api endpoint is required")
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // E2E uses self-signed test certificates.
				MinVersion:         tls.VersionTLS12,
			},
		},
		Timeout: authConfigHTTPTimeout,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiEndpoint, "/")+"/api/v1/auth/config", nil)
	if err != nil {
		return nil, fmt.Errorf("create auth config request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get auth config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("get auth config returned %d and response body could not be read: %w", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("get auth config returned %d: %s", resp.StatusCode, string(body))
	}

	var authConfig api.AuthConfig
	if err := json.NewDecoder(resp.Body).Decode(&authConfig); err != nil {
		return nil, fmt.Errorf("decode auth config: %w", err)
	}
	return &authConfig, nil
}

// findAuthProvider returns a provider with the requested name from an AuthConfig response.
func findAuthProvider(authConfig *api.AuthConfig, providerName string) (*api.AuthProvider, error) {
	if authConfig == nil || authConfig.Providers == nil {
		return nil, fmt.Errorf("auth config does not include providers")
	}
	for i := range *authConfig.Providers {
		provider := &(*authConfig.Providers)[i]
		if provider.Metadata.Name != nil && *provider.Metadata.Name == providerName {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("auth provider %q not found", providerName)
}

// writeAndApplyAuthProviderManifest writes a provider manifest to disk and applies it through the harness.
func writeAndApplyAuthProviderManifest(harness *e2e.Harness, providerPath, providerYAML string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("worker harness is required")
	}
	if strings.TrimSpace(providerPath) == "" {
		return "", fmt.Errorf("provider manifest path is required")
	}
	if strings.TrimSpace(providerYAML) == "" {
		return "", fmt.Errorf("provider manifest content is required")
	}
	if err := os.WriteFile(providerPath, []byte(providerYAML), 0600); err != nil {
		return "", fmt.Errorf("write authprovider manifest %q: %w", providerPath, err)
	}
	if err := restoreAdminLoginForResourceManagement(harness); err != nil {
		return "", err
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
	if err := restoreAdminLoginForResourceManagement(harness); err != nil {
		return "", err
	}
	return harness.ManageResource("delete", "authprovider", providerName)
}

// restoreAdminLoginForResourceManagement resets the CLI session to an admin user before authprovider mutations.
func restoreAdminLoginForResourceManagement(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("worker harness is required")
	}
	if _, err := login.LoginToAPIWithToken(harness); err != nil {
		return fmt.Errorf("restore admin login for authprovider resource management: %w", err)
	}
	return nil
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

	sanitizedArgs := sanitizeCommandArgsForLog(cmdArgs, scenario.username, scenario.password)
	logrus.Infof("[authprovider] running Cypress login helper for %s: %s %s", scenario.name, scriptPath, strings.Join(sanitizedArgs, " "))
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		logrus.Infof("[authprovider] cypress login output:\n%s", redactAuthProviderCredentials(string(out), scenario.username, scenario.password))
	}
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("run cypress login helper %q for %s: %w\n%s", scriptPath, scenario.name, err, redactAuthProviderCredentials(string(out), scenario.username, scenario.password))
		}
		return fmt.Errorf("run cypress login helper %q for %s: %w", scriptPath, scenario.name, err)
	}
	return nil
}

// runLoginWithCypressHarnessOrSkip runs the Cypress harness and skips when browser test dependencies are unavailable.
func runLoginWithCypressHarnessOrSkip(ctx context.Context, harness *e2e.Harness, apiEndpoint string, scenario browserLoginScenario) error {
	err := runLoginWithCypressHarness(ctx, harness, apiEndpoint, scenario)
	if isCypressUnavailableError(err) || isOpenShiftOAuthClientPrerequisiteError(err, scenario) {
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

// isOpenShiftOAuthClientPrerequisiteError reports whether an OpenShift browser test cannot run because its OAuthClient is unavailable or ambiguous.
func isOpenShiftOAuthClientPrerequisiteError(err error, scenario browserLoginScenario) bool {
	if err == nil || scenario.providerUI != openshiftProviderName {
		return false
	}
	errText := err.Error()
	return strings.Contains(errText, openshiftOAuthClientMissing) || strings.Contains(errText, openshiftOAuthClientMultiple)
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

	var stderrBuf bytes.Buffer
	var stdoutBuf bytes.Buffer

	if err = cmd.Start(); err != nil {
		if closeErr := stdoutPipe.Close(); closeErr != nil {
			logrus.Warnf("[authprovider] failed to close login command stdout after start failure: %v", closeErr)
		}
		if closeErr := stderrPipe.Close(); closeErr != nil {
			logrus.Warnf("[authprovider] failed to close login command stderr after start failure: %v", closeErr)
		}
		return "", nil, err
	}

	var copyWg sync.WaitGroup
	copyWg.Add(2)
	go func() {
		defer copyWg.Done()
		if _, copyErr := io.Copy(&stderrBuf, stderrPipe); copyErr != nil {
			logrus.Warnf("[authprovider] failed to capture login command stderr: %v", copyErr)
		}
	}()

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
			waitErr = loginCommandErrorWithOutput("login command failed", waitErr, stdoutBuf.String(), stderrBuf.String())
		}
		doneCh <- waitErr
		close(doneCh)
	}()

	select {
	case authURL = <-urlCh:
		return authURL, doneCh, nil
	case waitErr := <-doneCh:
		err = fmt.Errorf("login command exited: %w", waitErr)
		waitForLoginCommandPipes(&copyWg)
		logLoginCommandOutput("login command exited before printing URL", err, stdoutBuf.String(), stderrBuf.String())
		return "", nil, err
	case <-ctx.Done():
		if killErr := cmd.Process.Kill(); killErr != nil {
			logrus.Warnf("[authprovider] failed to kill login command after context cancellation: %v", killErr)
		}
		err = ctx.Err()
		waitForLoginCommandPipes(&copyWg)
		logLoginCommandOutput("login command failed (context cancelled)", err, stdoutBuf.String(), stderrBuf.String())
		return "", nil, err
	case <-time.After(loginURLTimeout):
		if killErr := cmd.Process.Kill(); killErr != nil {
			logrus.Warnf("[authprovider] failed to kill login command after URL timeout: %v", killErr)
		}
		err = fmt.Errorf("timeout waiting for login URL")
		waitForLoginCommandPipes(&copyWg)
		logLoginCommandOutput("timeout waiting for login URL", err, stdoutBuf.String(), stderrBuf.String())
		return "", nil, err
	}
}

// runProviderPasswordLoginCLI logs in through an OAuth2 provider using password grant credentials.
func runProviderPasswordLoginCLI(ctx context.Context, harness *e2e.Harness, apiEndpoint, providerName, username, password string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("worker harness is required")
	}
	if strings.TrimSpace(apiEndpoint) == "" {
		return "", fmt.Errorf("api endpoint is required")
	}
	if strings.TrimSpace(providerName) == "" {
		return "", fmt.Errorf("provider name is required")
	}
	if strings.TrimSpace(username) == "" {
		return "", fmt.Errorf("username is required")
	}
	if password == "" {
		return "", fmt.Errorf("password is required")
	}

	args := []string{
		"login", apiEndpoint,
		loginInsecureTLSArg,
		"--provider", providerName,
		"-u", username,
		"-p", password,
	}
	flightctlPath := harness.GetFlightctlPath()
	logrus.Infof("[authprovider] provider password login command for %s: %s %s", providerName, flightctlPath, strings.Join(sanitizeCommandArgsForLog(args, username, password), " "))

	cmd := exec.CommandContext(ctx, flightctlPath, args...) //nolint:gosec // G204: command is suite-owned flightctl binary with fixed test arguments
	cmd.Env = append(os.Environ(), "API_ENDPOINT="+apiEndpoint)
	out, err := cmd.CombinedOutput()
	sanitizedOut := redactAuthProviderCredentials(string(out), username, password)
	if err != nil {
		return sanitizedOut, fmt.Errorf("run provider password login for %s: %w\n%s", providerName, err, sanitizedOut)
	}
	return sanitizedOut, nil
}

// runLoginCLIWithURL starts `flightctl login ... --web --no-browser` for the bootstrap Keycloak OIDC provider.
func runLoginCLIWithURL(ctx context.Context, harness *e2e.Harness, apiEndpoint string) (authURL string, done <-chan error, err error) {
	return runProviderLoginCLIWithURL(ctx, harness, apiEndpoint, keycloakAuthProviderName)
}

// loginCommandErrorWithOutput attaches captured CLI streams to a login command error.
func loginCommandErrorWithOutput(message string, err error, stdout, stderr string) error {
	if err == nil {
		return nil
	}

	var output []string
	sanitizedStdout := redactAuthProviderCredentials(stdout)
	sanitizedStderr := redactAuthProviderCredentials(stderr)
	if strings.TrimSpace(sanitizedStdout) != "" {
		output = append(output, "stdout:\n"+sanitizedStdout)
	}
	if strings.TrimSpace(sanitizedStderr) != "" {
		output = append(output, "stderr:\n"+sanitizedStderr)
	}
	if len(output) == 0 {
		return fmt.Errorf("%s: %w", message, err)
	}
	return fmt.Errorf("%s: %w\n%s", message, err, strings.Join(output, "\n"))
}

// waitForLoginCommandPipes gives stdout/stderr copy goroutines a short window to flush captured output.
func waitForLoginCommandPipes(copyWg *sync.WaitGroup) {
	if copyWg == nil {
		return
	}
	pipeDone := make(chan struct{})
	go func() {
		copyWg.Wait()
		close(pipeDone)
	}()
	select {
	case <-pipeDone:
	case <-time.After(loginPipeDrainTimeout):
	}
}

// logLoginCommandOutput logs sanitized login command output after an error.
func logLoginCommandOutput(message string, err error, stdout, stderr string) {
	logrus.Errorf("[authprovider] %s: %v", message, err)
	if strings.TrimSpace(stderr) != "" {
		logrus.Errorf("[authprovider] login command stderr:\n%s", redactAuthProviderCredentials(stderr))
	}
	if strings.TrimSpace(stdout) != "" {
		logrus.Errorf("[authprovider] login command stdout:\n%s", redactAuthProviderCredentials(stdout))
	}
}

// sanitizeCommandArgsForLog redacts credential values before logging a command line.
func sanitizeCommandArgsForLog(args []string, sensitiveValues ...string) []string {
	if len(args) == 0 {
		return nil
	}

	sensitive := map[string]struct{}{}
	for _, value := range sensitiveValues {
		if strings.TrimSpace(value) != "" {
			sensitive[value] = struct{}{}
		}
	}

	sanitizedArgs := make([]string, 0, len(args))
	maskNextValue := false
	for _, arg := range args {
		switch {
		case maskNextValue:
			sanitizedArgs = append(sanitizedArgs, "<REDACTED>")
			maskNextValue = false
		case arg == "-u" || arg == "-p" || arg == "--username" || arg == "--password":
			sanitizedArgs = append(sanitizedArgs, arg)
			maskNextValue = true
		case isSensitiveValue(arg, sensitive):
			sanitizedArgs = append(sanitizedArgs, "<REDACTED>")
		case strings.HasPrefix(arg, "--username="):
			sanitizedArgs = append(sanitizedArgs, "--username=<REDACTED>")
		case strings.HasPrefix(arg, "--password="):
			sanitizedArgs = append(sanitizedArgs, "--password=<REDACTED>")
		default:
			sanitizedArgs = append(sanitizedArgs, arg)
		}
	}

	return sanitizedArgs
}

// isSensitiveValue reports whether an argument exactly matches a known sensitive value.
func isSensitiveValue(arg string, sensitive map[string]struct{}) bool {
	_, ok := sensitive[arg]
	return ok
}

// redactAuthProviderCredentials redacts credential-looking values from captured command output.
func redactAuthProviderCredentials(output string, exactValues ...string) string {
	if output == "" {
		return ""
	}

	redacted := output
	for _, value := range exactValues {
		if strings.TrimSpace(value) == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, value, "<REDACTED>")
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b((?:[A-Z_]*USERNAME|[A-Z_]*PASSWORD|authProviderUsername|authProviderPassword)\b[[:space:]]*[:=][[:space:]]*)("[^"]*"|'[^']*'|[^[:space:]]+)`),
		regexp.MustCompile(`(?i)([[:space:]]-[up][[:space:]]+)([^[:space:]]+)`),
		regexp.MustCompile(`(?i)(--(?:username|password)(?:=|[[:space:]]+))([^[:space:]]+)`),
	}
	for _, pattern := range patterns {
		redacted = pattern.ReplaceAllString(redacted, `${1}<REDACTED>`)
	}

	return redacted
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
			tick := time.NewTicker(chromedpCallbackPollInterval)
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
	jwksURL := issuerURL + "/protocol/openid-connect/certs"

	return buildOAuth2AuthProviderYAML(name, issuerURL, authorizationURL, tokenURL, userinfoURL, jwksURL, clientID, clientSecret, clientID, keycloakAccountAudience)
}

// buildOAuth2AuthProviderForDeployment selects PAM-backed OAuth2 when the deployment advertises the bundled PAM issuer.
func buildOAuth2AuthProviderForDeployment(
	ctx context.Context,
	apiEndpoint, name, keycloakIssuerURL, keycloakClientID, keycloakClientSecret string,
) (string, bool, error) {
	keycloakYAML := buildKeycloakOAuth2AuthProviderYAML(name, keycloakIssuerURL, keycloakClientID, keycloakClientSecret)

	authConfig, err := fetchAuthConfig(ctx, apiEndpoint)
	if err != nil {
		return "", false, fmt.Errorf("detect deployment auth config: %w", err)
	}
	pamProvider, err := findPAMOIDCProvider(authConfig)
	if err != nil {
		logrus.Infof("[authprovider] bundled PAM issuer is not advertised; using Keycloak OAuth2: %v", err)
		return keycloakYAML, false, nil
	}
	pamSpec, err := pamProvider.Spec.AsOIDCProviderSpec()
	if err != nil {
		return "", false, fmt.Errorf("parse static OIDC provider spec: %w", err)
	}
	if !strings.Contains(strings.ToLower(pamSpec.Issuer), pamIssuerIdentifier) {
		logrus.Infof("[authprovider] static OIDC issuer %q is not the bundled PAM issuer; using Keycloak OAuth2", pamSpec.Issuer)
		return keycloakYAML, false, nil
	}

	pamYAML, err := buildPAMOAuth2AuthProviderYAML(name, pamSpec)
	if err != nil {
		return "", false, fmt.Errorf("render PAM-backed OAuth2 authprovider: %w", err)
	}
	return pamYAML, true, nil
}

// findPAMOIDCProvider returns the static OIDC provider backed by the bundled PAM issuer.
func findPAMOIDCProvider(authConfig *api.AuthConfig) (*api.AuthProvider, error) {
	if authConfig == nil || authConfig.Providers == nil {
		return nil, fmt.Errorf("auth config does not include providers")
	}
	for i := range *authConfig.Providers {
		provider := &(*authConfig.Providers)[i]
		pamSpec, err := provider.Spec.AsOIDCProviderSpec()
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(pamSpec.Issuer), pamIssuerIdentifier) {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("PAM OIDC provider with issuer containing %q not found", pamIssuerIdentifier)
}

// buildPAMOAuth2AuthProviderYAML renders an OAuth2 authprovider backed by the bundled PAM issuer.
func buildPAMOAuth2AuthProviderYAML(name string, pamSpec api.OIDCProviderSpec) (string, error) {
	clientSecret, err := resolvePAMClientSecret(pamSpec.ClientSecret)
	if err != nil {
		return "", fmt.Errorf("resolve PAM client secret: %w", err)
	}
	issuerURL := strings.TrimRight(pamSpec.Issuer, "/")
	return buildOAuth2AuthProviderYAML(
		name,
		issuerURL,
		issuerURL+"/authorize",
		issuerURL+"/token",
		issuerURL+"/userinfo",
		issuerURL+"/jwks",
		pamSpec.ClientId,
		clientSecret,
		pamSpec.ClientId,
	), nil
}

// resolvePAMClientSecret returns the configured PAM secret when public auth config hides or omits it.
func resolvePAMClientSecret(publicClientSecret string) (string, error) {
	if publicClientSecret != "" && publicClientSecret != maskedSecretValue {
		return publicClientSecret, nil
	}

	providers := setup.GetDefaultProviders()
	if providers == nil || providers.Infra == nil {
		return "", fmt.Errorf("infra provider is required to resolve masked PAM client secret")
	}
	apiConfigYAML, err := providers.Infra.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		return "", fmt.Errorf("read API service config for PAM client secret: %w", err)
	}

	var apiConfig struct {
		Auth struct {
			OIDC struct {
				ClientSecret string `yaml:"clientSecret"`
			} `yaml:"oidc"`
		} `yaml:"auth"`
	}
	if err := yaml.Unmarshal([]byte(apiConfigYAML), &apiConfig); err != nil {
		return "", fmt.Errorf("parse API service config for PAM client secret: %w", err)
	}
	if apiConfig.Auth.OIDC.ClientSecret != "" {
		return apiConfig.Auth.OIDC.ClientSecret, nil
	}
	return generatePublicClientPlaceholder()
}

// generatePublicClientPlaceholder creates a runtime-only value for public PAM clients whose secret is intentionally unset.
func generatePublicClientPlaceholder() (string, error) {
	placeholderBytes := make([]byte, publicClientPlaceholderBytes)
	if _, err := rand.Read(placeholderBytes); err != nil {
		return "", fmt.Errorf("generate public PAM client placeholder: %w", err)
	}
	return hex.EncodeToString(placeholderBytes), nil
}

// buildOAuth2AuthProviderYAML renders a dynamic OAuth2 authprovider manifest for this suite.
func buildOAuth2AuthProviderYAML(name, issuerURL, authorizationURL, tokenURL, userinfoURL, jwksURL, clientID, clientSecret string, audiences ...string) string {
	audienceYAML := ""
	for _, audience := range audiences {
		if strings.TrimSpace(audience) != "" {
			audienceYAML += fmt.Sprintf("      - %s\n", audience)
		}
	}

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
%s
  organizationAssignment:
    type: static
    organizationName: %s
  roleAssignment:
    type: static
    roles:
      - %s
`, name, name, issuerURL, authorizationURL, tokenURL, userinfoURL, clientID, clientSecret, jwksURL, audienceYAML, defaultOrganizationName, defaultAdminRole)
}
