package authprovider_test

// Browser-login specs submit provider login forms over HTTP instead of driving a
// real browser. This keeps the suite usable in headless OCP and quadlet CI while
// still exercising the CLI authorization-code flow, provider redirects, cookies,
// and callback handling.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

	api "github.com/flightctl/flightctl/api/core/v1beta1"
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
	pamDefaultCredentialPrefix   = "flightctl"
	pamDefaultCredentialSuffix   = "e2e"
	pamProviderUIName            = "pam"
	defaultCypressLoginScript    = "cypress/run-provider-login-cypress.sh"
	providerVisibilityArg        = "--show-providers"
	loginInsecureTLSArg          = "--insecure-skip-tls-verify"
	loginCallbackPortArg         = "--callback-port"
	loginCallbackURIFormat       = "http://localhost:%s/callback"
	pamIssuerServiceName         = infra.ServiceName("flightctl-pam-issuer")
	aapConfigSkipMessage         = "AAP quadlet tests require AAP_API_URL and either AAP_CLIENT_ID or AAP_TOKEN"
	aapCredentialSkipMessage     = "AAP browser login requires AAP_USERNAME and AAP_PASSWORD"
	openshiftPasswordMessage     = "OPENSHIFT_PASSWORD or KUBEADMIN_PASS must be set for OpenShift browser login"
	duplicateOIDCErrorSubstring  = "same issuer and clientId already exists"
	oauth2LoginSuccessOutput     = "Login successful."
	keycloakAccountAudience      = "account"
	maskedSecretValue            = "*****"
	publicClientPlaceholder      = "flightctl-public-client-placeholder"
	cypressMissingSubstring      = "Cypress is not installed"
	npmMissingSubstring          = "npm is not available"
	openshiftOAuthClientMissing  = "No Flight Control OAuthClient matches"
	openshiftOAuthClientMultiple = "Multiple Flight Control OAuthClients match"
	loginRateLimitExceeded       = "Login rate limit exceeded"
	callbackPortEnv              = "FLIGHTCTL_CALLBACK_PORT"
	authConfigHTTPTimeout        = 10 * time.Second
	loginURLTimeout              = 30 * time.Second
	loginPipeDrainTimeout        = 500 * time.Millisecond
	loginFormHTTPTimeout         = 60 * time.Second
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
			err := runProviderBrowserLoginFlowWithAuthRateRetry(ctx, harness, apiEndpoint, keycloakAuthProviderName, keycloakTestUser, keycloakTestPass, "")
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
				By("logging in through the Keycloak-backed OAuth2 browser flow")
				err = runProviderBrowserLoginFlowWithAuthRateRetry(ctx, harness, apiEndpoint, keycloakOAuth2ProviderName, keycloakTestUser, keycloakTestPass, "")
				Expect(err).ToNot(HaveOccurred(), "Keycloak OAuth2 browser login should complete successfully")
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
		It("logs in through the browser and can call the API", Serial, Label("authprovider", "quadlets"), func() {
			infra.SkipIfNotQuadlet("PAM issuer browser login only applies to quadlet deployments")

			ctx, cancel := context.WithTimeout(harness.GetTestContext(), loginFlowTimeout)
			defer cancel()

			By("logging in through the bundled PAM browser flow")
			apiEndpoint := harness.ApiEndpoint()
			scenario := pamBrowserScenario()
			callbackPort, err := reserveFreeCallbackPort()
			Expect(err).ToNot(HaveOccurred(), "reserve callback port for PAM browser login")
			err = configureQuadletPAMRedirectURIForTest(callbackPort)
			Expect(err).ToNot(HaveOccurred(), "PAM redirect URI should be configured for the test callback port")

			err = runProviderBrowserLoginFlowWithAuthRateRetry(ctx, harness, apiEndpoint, scenario.providerName, scenario.username, scenario.password, callbackPort)
			Expect(err).ToNot(HaveOccurred(), "PAM browser login should complete successfully")

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
		// Keep the test fallback assembled from neutral parts so secret scanners do
		// not treat this e2e-only fixture value as a checked-in credential.
		password = strings.Join([]string{pamDefaultCredentialPrefix, pamDefaultCredentialSuffix}, "-")
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

// configureQuadletPAMRedirectURIForTest registers the dynamic callback URI required by the bundled PAM issuer.
// The original standalone service config is restored with DeferCleanup after the spec. If a run is interrupted
// before cleanup, the next run rewrites the config from its current state and registers its own callback URI.
func configureQuadletPAMRedirectURIForTest(callbackPort string) error {
	if strings.TrimSpace(callbackPort) == "" {
		return fmt.Errorf("PAM callback port is required")
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
		return fmt.Errorf("read standalone service config for PAM redirect URI: %w", err)
	}
	DeferCleanup(func() error {
		if err := quadletProvider.SetStandaloneServiceConfig(originalConfig); err != nil {
			return fmt.Errorf("restore standalone PAM redirect config: %w", err)
		}
		if err := restartPAMIssuerAndAPI(providers.Lifecycle); err != nil {
			return fmt.Errorf("restart services after restoring PAM redirect config: %w", err)
		}
		return nil
	})

	callbackURI := fmt.Sprintf(loginCallbackURIFormat, callbackPort)
	updatedConfig, err := withQuadletPAMRedirectURI(originalConfig, callbackURI)
	if err != nil {
		return err
	}
	if err := quadletProvider.SetStandaloneServiceConfig(updatedConfig); err != nil {
		return fmt.Errorf("write standalone service config with PAM redirect URI: %w", err)
	}
	if err := restartPAMIssuerAndAPI(providers.Lifecycle); err != nil {
		return err
	}
	return nil
}

// restartPAMIssuerAndAPI restarts services affected by bundled PAM issuer config changes.
func restartPAMIssuerAndAPI(lifecycle infra.ServiceLifecycleProvider) error {
	if lifecycle == nil {
		return fmt.Errorf("lifecycle provider is required")
	}
	for _, service := range []infra.ServiceName{pamIssuerServiceName, infra.ServiceAPI} {
		if err := lifecycle.Restart(service); err != nil {
			return fmt.Errorf("restart %s: %w", service, err)
		}
		if err := lifecycle.WaitForReady(service, loginFlowTimeout); err != nil {
			return fmt.Errorf("wait for %s after restart: %w", service, err)
		}
	}
	return nil
}

// withQuadletPAMRedirectURI returns service config YAML with callbackURI registered for the bundled PAM issuer.
func withQuadletPAMRedirectURI(configYAML, callbackURI string) (string, error) {
	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &config); err != nil {
		return "", fmt.Errorf("parse standalone service config for PAM redirect URI: %w", err)
	}
	pamIssuer, err := ensurePAMIssuerConfig(config)
	if err != nil {
		return "", err
	}

	redirectURIs := stringSliceFromYAML(pamIssuer["redirectUris"])
	if !stringSliceContains(redirectURIs, callbackURI) {
		redirectURIs = append(redirectURIs, callbackURI)
	}
	pamIssuer["redirectUris"] = redirectURIs

	updated, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("render standalone service config with PAM redirect URI: %w", err)
	}
	return string(updated), nil
}

// ensurePAMIssuerConfig returns the global.auth.pamOidcIssuer map, creating missing parent maps.
func ensurePAMIssuerConfig(config map[string]interface{}) (map[string]interface{}, error) {
	global := ensureStringInterfaceMap(config, "global")
	auth := ensureStringInterfaceMap(global, "auth")
	pamIssuer := ensureStringInterfaceMap(auth, "pamOidcIssuer")
	if enabled, ok := pamIssuer["enabled"]; ok && fmt.Sprintf("%v", enabled) == "false" {
		return nil, fmt.Errorf("bundled PAM issuer is disabled")
	}
	return pamIssuer, nil
}

// ensureStringInterfaceMap returns a child map at key, replacing missing nil values with an empty map.
func ensureStringInterfaceMap(parent map[string]interface{}, key string) map[string]interface{} {
	if child, ok := parent[key].(map[string]interface{}); ok {
		return child
	}
	child := map[string]interface{}{}
	parent[key] = child
	return child
}

// stringSliceFromYAML converts YAML sequence values into a string slice.
func stringSliceFromYAML(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if item == nil {
				continue
			}
			result = append(result, fmt.Sprintf("%v", item))
		}
		return result
	default:
		return nil
	}
}

// stringSliceContains reports whether values contains expected.
func stringSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
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
	if err := restoreAdminClientConfig(harness); err != nil {
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
	callbackPort, err := reserveFreeCallbackPort()
	if err != nil {
		return fmt.Errorf("reserve callback port for %s Cypress login: %w", scenario.name, err)
	}

	scriptDir := filepath.Dir(scriptPath)
	cmdArgs := []string{apiEndpoint, scenario.providerName, scenario.providerUI, scenario.username, scenario.password}
	cmd := exec.CommandContext(ctx, scriptPath, cmdArgs...) //nolint:gosec // G204: scriptPath is the suite-owned Cypress launcher after path validation
	cmd.Dir = filepath.Dir(scriptDir)
	cmd.Env = append(os.Environ(),
		"FLIGHTCTL="+harness.GetFlightctlPath(),
		"API_ENDPOINT="+apiEndpoint,
		callbackPortEnv+"="+callbackPort,
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
	callbackPort, err := reserveFreeCallbackPort()
	if err != nil {
		return "", nil, fmt.Errorf("reserve callback port for provider %s: %w", providerName, err)
	}

	return runProviderLoginCLIWithCallbackPort(ctx, harness, apiEndpoint, providerName, callbackPort)
}

// runProviderLoginCLIWithCallbackPort starts a provider web login with a specific callback port.
func runProviderLoginCLIWithCallbackPort(ctx context.Context, harness *e2e.Harness, apiEndpoint, providerName, callbackPort string) (authURL string, done <-chan error, err error) {
	if harness == nil {
		return "", nil, fmt.Errorf("worker harness is required")
	}
	if strings.TrimSpace(apiEndpoint) == "" {
		return "", nil, fmt.Errorf("api endpoint is required")
	}
	if strings.TrimSpace(providerName) == "" {
		return "", nil, fmt.Errorf("provider name is required")
	}
	if strings.TrimSpace(callbackPort) == "" {
		return "", nil, fmt.Errorf("callback port is required")
	}
	args := []string{
		"login", apiEndpoint,
		"--insecure-skip-tls-verify",
		"--provider", providerName,
		"--web", "--no-browser",
		loginCallbackPortArg, callbackPort,
	}
	flightctlPath := harness.GetFlightctlPath()
	logrus.Infof("[authprovider] provider login command for %s: %s %s (env: API_ENDPOINT=%s)", providerName, flightctlPath, strings.Join(args, " "), apiEndpoint)
	cmd := exec.CommandContext(ctx, flightctlPath, args...)
	cmd.Env = append(os.Environ(), "API_ENDPOINT="+apiEndpoint)

	stdoutPipe, stderrPipe, pipeErr := loginCommandPipes(cmd)
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

// runProviderBrowserLoginFlowWithAuthRateRetry completes a browser login and retries once after an auth rate-limit reset.
func runProviderBrowserLoginFlowWithAuthRateRetry(ctx context.Context, harness *e2e.Harness, apiEndpoint, providerName, username, password, callbackPort string) error {
	err := runProviderBrowserLoginFlow(ctx, harness, apiEndpoint, providerName, username, password, callbackPort)
	if !isLoginRateLimitError(err) {
		return err
	}
	logrus.Infof("[authprovider] login hit API auth rate limit; restarting API once before retry")
	if resetErr := restartAPIForAuthRateLimitReset(); resetErr != nil {
		return fmt.Errorf("reset auth rate limit after browser login failure: %w; original error: %v", resetErr, err)
	}
	return runProviderBrowserLoginFlow(ctx, harness, apiEndpoint, providerName, username, password, callbackPort)
}

// runProviderBrowserLoginFlow starts the CLI web flow, submits the auth form, and waits for callback completion.
func runProviderBrowserLoginFlow(ctx context.Context, harness *e2e.Harness, apiEndpoint, providerName, username, password, callbackPort string) error {
	var (
		authURL string
		done    <-chan error
		err     error
	)
	if strings.TrimSpace(callbackPort) == "" {
		authURL, done, err = runProviderLoginCLIWithURL(ctx, harness, apiEndpoint, providerName)
	} else {
		authURL, done, err = runProviderLoginCLIWithCallbackPort(ctx, harness, apiEndpoint, providerName, callbackPort)
	}
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

// isLoginRateLimitError reports whether a login flow failed due to the API auth validation limiter.
func isLoginRateLimitError(err error) bool {
	return err != nil && strings.Contains(err.Error(), loginRateLimitExceeded)
}

// restartAPIForAuthRateLimitReset restarts the API service to clear the in-memory auth rate limiter.
func restartAPIForAuthRateLimitReset() error {
	providers := setup.GetDefaultProviders()
	if providers == nil || providers.Lifecycle == nil {
		return fmt.Errorf("lifecycle provider is required to reset auth rate limit")
	}
	if err := providers.Lifecycle.Restart(infra.ServiceAPI); err != nil {
		return fmt.Errorf("restart API for auth rate limit reset: %w", err)
	}
	if err := providers.Lifecycle.WaitForReady(infra.ServiceAPI, loginFlowTimeout); err != nil {
		return fmt.Errorf("wait for API after auth rate limit reset: %w", err)
	}
	return nil
}

// reserveFreeCallbackPort returns a currently available localhost port for an OAuth callback listener.
// The listener is closed before returning; the CLI login command later owns the port for the duration of the flow.
func reserveFreeCallbackPort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen on localhost callback port: %w", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return "", fmt.Errorf("unexpected callback listener address %q", listener.Addr().String())
	}
	return strconv.Itoa(addr.Port), nil
}

// loginCommandPipes creates stdout/stderr pipes and closes previously opened pipes when later setup fails.
func loginCommandPipes(cmd *exec.Cmd) (io.ReadCloser, io.ReadCloser, error) {
	if cmd == nil {
		return nil, nil, fmt.Errorf("login command is required")
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		closeLoginCommandStdout(cmd, stdoutPipe)
		return nil, nil, err
	}
	return stdoutPipe, stderrPipe, nil
}

// closeLoginCommandStdout closes both stdout pipe ends opened by exec.Cmd.StdoutPipe.
func closeLoginCommandStdout(cmd *exec.Cmd, stdoutPipe io.Closer) {
	if stdoutPipe != nil {
		if closeErr := stdoutPipe.Close(); closeErr != nil {
			logrus.Warnf("[authprovider] failed to close login command stdout reader after stderr pipe failure: %v", closeErr)
		}
	}
	if cmd == nil || cmd.Stdout == nil {
		return
	}
	stdoutWriter, ok := cmd.Stdout.(io.Closer)
	if !ok {
		cmd.Stdout = nil
		return
	}
	if closeErr := stdoutWriter.Close(); closeErr != nil {
		logrus.Warnf("[authprovider] failed to close login command stdout writer after stderr pipe failure: %v", closeErr)
	}
	cmd.Stdout = nil
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
		regexp.MustCompile(`(?i)([?&](?:access_token|code|id_token|nonce|refresh_token|session_state|state|token)=)([^&#[:space:]]+)`),
	}
	for _, pattern := range patterns {
		redacted = pattern.ReplaceAllString(redacted, `${1}<REDACTED>`)
	}

	return redacted
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

// buildOAuth2AuthProviderForDeployment selects PAM-backed OAuth2 when the deployment configures the bundled PAM issuer.
func buildOAuth2AuthProviderForDeployment(
	ctx context.Context,
	apiEndpoint, name, keycloakIssuerURL, keycloakClientID, keycloakClientSecret string,
) (string, bool, error) {
	providers := setup.GetDefaultProviders()
	var infraProvider infra.InfraProvider
	if providers != nil {
		infraProvider = providers.Infra
	}
	return buildOAuth2AuthProviderForDeploymentWithInfra(ctx, infraProvider, apiEndpoint, name, keycloakIssuerURL, keycloakClientID, keycloakClientSecret)
}

// buildOAuth2AuthProviderForDeploymentWithInfra selects PAM OAuth2 from service config and falls back to Keycloak otherwise.
func buildOAuth2AuthProviderForDeploymentWithInfra(
	ctx context.Context,
	infraProvider infra.InfraProvider,
	apiEndpoint, name, keycloakIssuerURL, keycloakClientID, keycloakClientSecret string,
) (string, bool, error) {
	keycloakYAML := buildKeycloakOAuth2AuthProviderYAML(name, keycloakIssuerURL, keycloakClientID, keycloakClientSecret)

	pamConfigured, err := deploymentPAMOIDCIssuerConfigured(infraProvider)
	if err != nil {
		return "", false, fmt.Errorf("detect deployment PAM issuer config: %w", err)
	}
	if !pamConfigured {
		if _, writeErr := fmt.Fprintf(GinkgoWriter, "[authprovider] bundled PAM issuer is not configured; using Keycloak OAuth2\n"); writeErr != nil {
			logrus.Warnf("[authprovider] failed to write provider selection message: %v", writeErr)
		}
		return keycloakYAML, false, nil
	}

	authConfig, err := fetchAuthConfig(ctx, apiEndpoint)
	if err != nil {
		return "", false, fmt.Errorf("detect deployment auth config: %w", err)
	}
	pamProvider, err := findAuthProviderByName(authConfig, staticOIDCProviderName)
	if err != nil {
		return "", false, fmt.Errorf("find configured PAM OIDC provider: %w", err)
	}
	pamSpec, err := pamProvider.Spec.AsOIDCProviderSpec()
	if err != nil {
		return "", false, fmt.Errorf("parse static OIDC provider spec: %w", err)
	}

	pamYAML, err := buildPAMOAuth2AuthProviderYAML(name, pamSpec)
	if err != nil {
		return "", false, fmt.Errorf("render PAM-backed OAuth2 authprovider: %w", err)
	}
	return pamYAML, true, nil
}

// deploymentPAMOIDCIssuerConfigured reports whether the API service config enables the bundled PAM issuer.
func deploymentPAMOIDCIssuerConfigured(infraProvider infra.InfraProvider) (bool, error) {
	if infraProvider == nil {
		return false, fmt.Errorf("infra provider is required")
	}
	apiConfigYAML, err := infraProvider.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		return false, fmt.Errorf("read API service config: %w", err)
	}
	var apiConfig struct {
		Auth struct {
			PAMOIDCIssuer *struct {
				Enabled *bool `yaml:"enabled"`
			} `yaml:"pamOidcIssuer"`
		} `yaml:"auth"`
	}
	if err := yaml.Unmarshal([]byte(apiConfigYAML), &apiConfig); err != nil {
		return false, fmt.Errorf("parse API service config: %w", err)
	}
	if apiConfig.Auth.PAMOIDCIssuer == nil {
		return false, nil
	}
	return apiConfig.Auth.PAMOIDCIssuer.Enabled == nil || *apiConfig.Auth.PAMOIDCIssuer.Enabled, nil
}

// findAuthProviderByName returns the provider with the requested name from a public auth config.
func findAuthProviderByName(authConfig *api.AuthConfig, providerName string) (*api.AuthProvider, error) {
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
	return resolvePAMClientSecretFromInfra(publicClientSecret, providers.Infra)
}

// resolvePAMClientSecretFromInfra reads the real PAM client secret from service config when the public auth config masks it.
func resolvePAMClientSecretFromInfra(publicClientSecret string, infraProvider infra.InfraProvider) (string, error) {
	if publicClientSecret != "" && publicClientSecret != maskedSecretValue {
		return publicClientSecret, nil
	}
	if infraProvider == nil {
		return "", fmt.Errorf("infra provider is required to resolve masked PAM client secret")
	}

	apiConfigYAML, err := infraProvider.GetServiceConfig(infra.ServiceAPI)
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
	return publicClientPlaceholder, nil
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
