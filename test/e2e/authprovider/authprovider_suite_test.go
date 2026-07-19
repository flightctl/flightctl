package authprovider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	keycloakAuthProviderName      = "keycloak-e2e"
	authProviderKeycloakContainer = "e2e-keycloak"
	authProviderApplyTimeout      = 15 * time.Second
	loginRateLimitExceeded        = "Login rate limit exceeded"
)

func TestAuthprovider(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth Provider E2E Suite")
}

var auxSvcs *auxiliary.Services
var originalClientConfig *clientConfigSnapshot
var adminClientConfig *clientConfigSnapshot

var _ = BeforeSuite(func() {
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	// Start only Keycloak (not all aux services)
	ctx := context.Background()
	var err error
	removeStaleAuthProviderKeycloak()
	auxSvcs, err = auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceKeycloak})
	Expect(err).ToNot(HaveOccurred(), "failed to start Keycloak")
	Expect(auxSvcs.Keycloak.URL).ToNot(BeEmpty())

	// Use harness without VM (authprovider tests only need API + CLI)
	_, _, err = e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to setup harness")

	harness := e2e.GetWorkerHarness()

	originalClientConfig, err = captureClientConfigSnapshot()
	Expect(err).ToNot(HaveOccurred(), "failed to capture original client config")

	// Bootstrap: login as admin (k8s or PAM) and apply AuthProvider CR
	err = bootstrapLoginWithAuthRateRetry(harness)
	Expect(err).ToNot(HaveOccurred(), "bootstrap login failed")
	adminClientConfig, err = captureClientConfigSnapshot()
	Expect(err).ToNot(HaveOccurred(), "failed to capture admin client config")

	authProviderYAML := buildOIDCAuthProviderYAML(
		keycloakAuthProviderName,
		auxSvcs.Keycloak.IssuerURL(),
		"flightctl-client",
		auxiliary.KeycloakE2EClientSecret,
		true,
	)
	authProviderPath := filepath.Join(os.TempDir(), "authprovider-keycloak-e2e.yaml")
	DeferCleanup(os.Remove, authProviderPath)

	Eventually(func() error {
		_, applyErr := writeAndApplyAuthProviderManifest(harness, authProviderPath, authProviderYAML)
		return applyErr
	}).WithTimeout(authProviderApplyTimeout).WithPolling(authProviderPollingInterval).Should(Succeed(), "apply AuthProvider CR")

	// Wait until the API's loader has picked up the new provider with the current issuer
	// (auth config is served from cache; without this the CLI would get a stale issuer from a previous run)
	apiEndpoint := harness.ApiEndpoint()
	Eventually(func() bool {
		out, err := showLoginProviders(harness, apiEndpoint)
		if err != nil {
			return false
		}
		if !strings.Contains(out, keycloakAuthProviderName) {
			return false
		}
		return strings.Contains(out, auxSvcs.Keycloak.IssuerURL())
	}).WithTimeout(authProviderApplyTimeout).WithPolling(authProviderPollingInterval).Should(BeTrue(), "provider %q with issuer %q must appear in login --show-providers", keycloakAuthProviderName, auxSvcs.Keycloak.IssuerURL())
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	err := restoreAdminClientConfig(harness)
	Expect(err).ToNot(HaveOccurred(), "restore admin login before spec")
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Capture logs if test failed
	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	harness.SetTestContext(suiteCtx)
})

var _ = AfterSuite(func() {
	harness := e2e.GetWorkerHarness()

	// Restore admin authentication before cleanup
	err := restoreAdminClientConfig(harness)
	if err != nil {
		logrus.Warnf("Failed to restore admin login: %v", err)
	} else {
		// Clean up the Keycloak AuthProvider CR to prevent interfering with subsequent test suites
		_, err = deleteAuthProviderByName(harness, keycloakAuthProviderName)
		if err != nil {
			logrus.Warnf("Failed to delete authprovider %s: %v", keycloakAuthProviderName, err)
		} else {
			logrus.Infof("Deleted authprovider %s", keycloakAuthProviderName)
		}
	}

	if err = restoreClientConfigSnapshot(harness, originalClientConfig); err != nil {
		logrus.Warnf("Failed to restore original client config: %v", err)
	}

	// Clean up Keycloak container
	if auxSvcs != nil {
		ctx := context.Background()
		auxSvcs.Cleanup(ctx)
	}
})

// removeStaleAuthProviderKeycloak removes the suite-owned Keycloak container so each run imports a fresh realm.
func removeStaleAuthProviderKeycloak() {
	if !containers.ContainerExistsByName(authProviderKeycloakContainer) {
		return
	}
	logrus.Infof("[authprovider] removing stale %s container before suite setup", authProviderKeycloakContainer)
	if err := containers.RemoveContainerByName(authProviderKeycloakContainer); err != nil {
		logrus.Warnf("[authprovider] failed to remove stale %s container: %v", authProviderKeycloakContainer, err)
	}
}

// bootstrapLoginWithAuthRateRetry logs in as admin and retries once after resetting the API auth rate limiter.
func bootstrapLoginWithAuthRateRetry(harness *e2e.Harness) error {
	if existingErr := verifyExistingAdminClientConfig(harness); existingErr == nil {
		logrus.Infof("[authprovider] using existing admin client config for suite bootstrap")
		return nil
	} else {
		logrus.Infof("[authprovider] existing admin client config is not usable for suite bootstrap: %v", existingErr)
	}

	_, err := login.LoginToAPIWithToken(harness)
	if err == nil {
		return nil
	}
	logrus.Infof("[authprovider] bootstrap login failed; restarting API once before retry: %v", err)
	if resetErr := restartAPIForAuthRateLimitReset(); resetErr != nil {
		return fmt.Errorf("reset API after bootstrap login failure: %w; original error: %v", resetErr, err)
	}
	_, err = login.LoginToAPIWithToken(harness)
	return err
}

// verifyExistingAdminClientConfig checks whether the current client config can already perform admin operations.
func verifyExistingAdminClientConfig(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("worker harness is required")
	}
	if err := harness.RefreshClient(); err != nil {
		return fmt.Errorf("refresh existing client config: %w", err)
	}
	out, err := harness.CLI("get", "authproviders")
	if err != nil {
		return fmt.Errorf("get authproviders with existing client config: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// restoreAdminClientConfig restores the suite's saved admin login config and refreshes the harness client.
func restoreAdminClientConfig(harness *e2e.Harness) error {
	if adminClientConfig == nil {
		return fmt.Errorf("admin client config snapshot is nil")
	}
	return restoreClientConfigSnapshot(harness, adminClientConfig)
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

// clientConfigSnapshot stores the original local client config so the suite can restore it after bootstrap login.
type clientConfigSnapshot struct {
	path    string
	exists  bool
	content []byte
}

// captureClientConfigSnapshot records the current client config file contents, if any.
func captureClientConfigSnapshot() (*clientConfigSnapshot, error) {
	configPath, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve default client config path: %w", err)
	}

	snapshot := &clientConfigSnapshot{path: configPath}
	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshot, nil
		}
		return nil, fmt.Errorf("read client config %q: %w", configPath, err)
	}

	snapshot.exists = true
	snapshot.content = append([]byte(nil), content...)
	return snapshot, nil
}

// restoreClientConfigSnapshot restores the original client config file and refreshes the harness client when appropriate.
func restoreClientConfigSnapshot(harness *e2e.Harness, snapshot *clientConfigSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("client config snapshot is nil")
	}
	if strings.TrimSpace(snapshot.path) == "" {
		return fmt.Errorf("client config snapshot path is empty")
	}

	if snapshot.exists {
		if err := os.MkdirAll(filepath.Dir(snapshot.path), 0o755); err != nil {
			return fmt.Errorf("create client config directory for %q: %w", snapshot.path, err)
		}
		if err := os.WriteFile(snapshot.path, snapshot.content, 0o600); err != nil {
			return fmt.Errorf("restore client config %q: %w", snapshot.path, err)
		}
		logrus.Infof("Restored original client config at %s", snapshot.path)
		if harness != nil {
			if err := harness.RefreshClient(); err != nil {
				return fmt.Errorf("refresh client after restoring config %q: %w", snapshot.path, err)
			}
		}
		return nil
	}

	if err := os.Remove(snapshot.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove generated client config %q: %w", snapshot.path, err)
	}
	logrus.Infof("Removed generated client config at %s to restore original disconnected state", snapshot.path)
	return nil
}
