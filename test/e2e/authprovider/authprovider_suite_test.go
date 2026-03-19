package authprovider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	keycloakAuthProviderName = "keycloak-e2e"
	authProviderApplyTimeout = 15 * time.Second
)

func TestAuthprovider(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth Provider E2E Suite")
}

var auxSvcs *auxiliary.Services

var _ = BeforeSuite(func() {
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	// Start only Keycloak (not all aux services)
	ctx := context.Background()
	var err error
	auxSvcs, err = auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceKeycloak})
	Expect(err).ToNot(HaveOccurred(), "failed to start Keycloak")
	Expect(auxSvcs.Keycloak.URL).ToNot(BeEmpty())

	// Use harness without VM (authprovider tests only need API + CLI)
	_, _, err = e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to setup harness")

	harness := e2e.GetWorkerHarness()

	// Bootstrap: login as admin (k8s or PAM) and apply AuthProvider CR
	_, err = login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred(), "bootstrap login failed")

	authProviderYAML := buildKeycloakAuthProviderYAML(auxSvcs.Keycloak.IssuerURL(), auxiliary.KeycloakE2EClientSecret)
	authProviderPath := filepath.Join(os.TempDir(), "authprovider-keycloak-e2e.yaml")
	Expect(os.WriteFile(authProviderPath, []byte(authProviderYAML), 0600)).To(Succeed())
	defer func() { _ = os.Remove(authProviderPath) }()

	Eventually(func() error {
		_, applyErr := harness.CLI("apply", "-f", authProviderPath)
		return applyErr
	}).WithTimeout(authProviderApplyTimeout).WithPolling(2*time.Second).Should(Succeed(), "apply AuthProvider CR")

	// Wait until the API's loader has picked up the new provider with the current issuer
	// (auth config is served from cache; without this the CLI would get a stale issuer from a previous run)
	apiEndpoint := harness.ApiEndpoint()
	currentIssuer := auxSvcs.Keycloak.IssuerURL()
	Eventually(func() bool {
		out, err := harness.CLI("login", apiEndpoint, "--insecure-skip-tls-verify", "--show-providers")
		if err != nil {
			return false
		}
		if !strings.Contains(out, keycloakAuthProviderName) {
			return false
		}
		return strings.Contains(out, currentIssuer)
	}).WithTimeout(15*time.Second).WithPolling(2*time.Second).Should(BeTrue(), "provider %q with issuer %q must appear in login --show-providers", keycloakAuthProviderName, currentIssuer)
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)
})

func buildKeycloakAuthProviderYAML(issuerURL, clientSecret string) string {
	return fmt.Sprintf(`apiVersion: flightctl.io/v1beta1
kind: AuthProvider
metadata:
  name: %s
spec:
  providerType: oidc
  displayName: Keycloak E2E
  issuer: %s
  clientId: flightctl-client
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
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-admin
`, keycloakAuthProviderName, issuerURL, clientSecret)
}
