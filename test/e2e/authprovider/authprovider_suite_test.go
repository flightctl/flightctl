package authprovider_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	keycloakAuthProviderName = "keycloak-e2e"
	authProviderApplyTimeout = 15 * time.Second
	keycloakOCPName          = "e2e-keycloak"
	keycloakOCPImageEnv      = "E2E_KEYCLOAK_IMAGE"
	keycloakOCPDefaultImage  = "quay.io/keycloak/keycloak:26.5.5"
	keycloakOCPRolloutWait   = "3m"
	keycloakOCPReadyTimeout  = 2 * time.Minute
	keycloakOCPPollInterval  = 2 * time.Second
	keycloakOCPHTTPTimeout   = 5 * time.Second
	keycloakOCPLogTailLines  = "100"
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

	// Use harness without VM (authprovider tests only need API + CLI)
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to setup harness")

	harness := e2e.GetWorkerHarness()

	// Start only Keycloak (not all aux services). On OCP this runs in-cluster so the
	// API pod and the browser use the same reachable route issuer.
	ctx := context.Background()
	auxSvcs, err = setupKeycloakForSuite(ctx, harness)
	Expect(err).ToNot(HaveOccurred(), "failed to start Keycloak")
	Expect(auxSvcs.Keycloak.URL).ToNot(BeEmpty())

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
		keycloakClientID,
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

	// Restore admin authentication before cleanup.
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

	ctx := context.Background()
	if infra.DetectEnvironment() == infra.EnvironmentOCP {
		if err := cleanupOCPKeycloak(ctx, harness); err != nil {
			logrus.Warnf("Failed to clean up OCP Keycloak: %v", err)
		}
	} else if auxSvcs != nil {
		// Clean up Keycloak container
		auxSvcs.Cleanup(ctx)
	}
})

// bootstrapLoginWithAuthRateRetry logs in as admin and retries once after resetting the API auth rate limiter.
func bootstrapLoginWithAuthRateRetry(harness *e2e.Harness) error {
	_, err := login.LoginToAPIWithToken(harness)
	if !isLoginRateLimitError(err) {
		return err
	}
	logrus.Infof("[authprovider] bootstrap login hit API auth rate limit; restarting API once before retry")
	if resetErr := restartAPIForAuthRateLimitReset(); resetErr != nil {
		return fmt.Errorf("reset auth rate limit after bootstrap login failure: %w; original error: %v", resetErr, err)
	}
	_, err = login.LoginToAPIWithToken(harness)
	return err
}

// setupKeycloakForSuite returns a Keycloak endpoint usable by both the test browser and Flight Control API.
func setupKeycloakForSuite(ctx context.Context, harness *e2e.Harness) (*auxiliary.Services, error) {
	if infra.DetectEnvironment() == infra.EnvironmentOCP {
		return setupOCPKeycloak(ctx, harness)
	}
	return auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceKeycloak})
}

// setupOCPKeycloak deploys the suite-owned Keycloak realm in OpenShift and exposes it with a route.
func setupOCPKeycloak(ctx context.Context, harness *e2e.Harness) (*auxiliary.Services, error) {
	providers := setup.GetDefaultProviders()
	if providers == nil || providers.Infra == nil {
		return nil, fmt.Errorf("infra provider is required to deploy OCP Keycloak")
	}
	namespace := providers.Infra.GetExternalNamespace()
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("external namespace is required to deploy OCP Keycloak")
	}

	manifest, err := buildOCPKeycloakManifest(namespace)
	if err != nil {
		return nil, err
	}
	if out, err := harness.SHWithStdin(manifest, "oc", true, "apply", "-f", "-"); err != nil {
		return nil, fmt.Errorf("apply OCP Keycloak manifest in namespace %s: %w: %s", namespace, err, strings.TrimSpace(out))
	}
	if out, err := harness.SH("oc", "rollout", "status", "deployment/"+keycloakOCPName, "-n", namespace, "--timeout="+keycloakOCPRolloutWait); err != nil {
		return nil, fmt.Errorf("wait for OCP Keycloak rollout for deployment %s in namespace %s: %w: %s%s", keycloakOCPName, namespace, err, strings.TrimSpace(out), ocpKeycloakDebugInfo(harness, namespace))
	}

	routeHost, err := ocOutput(harness, "get", "route", keycloakOCPName, "-n", namespace, "-o", "jsonpath={.spec.host}")
	if err != nil {
		return nil, fmt.Errorf("get OCP Keycloak route host: %w", err)
	}
	keycloakURL := "http://" + strings.TrimSpace(routeHost)
	if err := waitForKeycloakIssuer(ctx, keycloakURL+"/realms/flightctl"); err != nil {
		return nil, fmt.Errorf("%w%s", err, ocpKeycloakDebugInfo(harness, namespace))
	}

	logrus.Infof("OCP Keycloak route ready: %s", keycloakURL)
	return &auxiliary.Services{Keycloak: &auxiliary.Keycloak{URL: keycloakURL}}, nil
}

// ocpKeycloakDebugInfo returns best-effort commands and logs for OCP Keycloak setup failures.
func ocpKeycloakDebugInfo(harness *e2e.Harness, namespace string) string {
	if harness == nil || strings.TrimSpace(namespace) == "" {
		return ""
	}
	var details []string
	if out, err := harness.SH("oc", "get", "pods", "-n", namespace, "-l", "app="+keycloakOCPName, "-o", "wide"); err == nil && strings.TrimSpace(out) != "" {
		details = append(details, "pods:\n"+strings.TrimSpace(out))
	}
	if out, err := harness.SH("oc", "describe", "deployment/"+keycloakOCPName, "-n", namespace); err == nil && strings.TrimSpace(out) != "" {
		details = append(details, "deployment:\n"+strings.TrimSpace(out))
	}
	if out, err := harness.SH("oc", "logs", "deployment/"+keycloakOCPName, "-n", namespace, "--tail="+keycloakOCPLogTailLines); err == nil && strings.TrimSpace(out) != "" {
		details = append(details, "logs:\n"+strings.TrimSpace(out))
	}
	if len(details) == 0 {
		return fmt.Sprintf("\nDebug manually with: oc get pods -n %s -l app=%s; oc describe deployment/%s -n %s", namespace, keycloakOCPName, keycloakOCPName, namespace)
	}
	return "\n" + strings.Join(details, "\n")
}

// cleanupOCPKeycloak removes the temporary OCP Keycloak resources created by this suite.
func cleanupOCPKeycloak(_ context.Context, harness *e2e.Harness) error {
	providers := setup.GetDefaultProviders()
	if providers == nil || providers.Infra == nil {
		return nil
	}
	namespace := providers.Infra.GetExternalNamespace()
	if strings.TrimSpace(namespace) == "" {
		return nil
	}
	out, err := harness.SH("oc", "delete", "route,svc,deployment,configmap", keycloakOCPName, "-n", namespace, "--ignore-not-found")
	if err != nil {
		return fmt.Errorf("delete OCP Keycloak resources: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// buildOCPKeycloakManifest renders the OpenShift resources for the suite-owned Keycloak instance.
func buildOCPKeycloakManifest(namespace string) (string, error) {
	realmJSON, err := os.ReadFile(filepath.Join(util.GetTopLevelDir(), "test", "e2e", "infra", "auxiliary", "keycloak", "flightctl-realm.json"))
	if err != nil {
		return "", fmt.Errorf("read Keycloak realm fixture: %w", err)
	}

	image := strings.TrimSpace(os.Getenv(keycloakOCPImageEnv))
	if image == "" {
		image = keycloakOCPDefaultImage
	}
	adminUser := testFixtureValue("keycloak", "admin")
	adminPassword := testFixtureValue("keycloak", "bootstrap")

	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %[1]s
  namespace: %[2]s
data:
  flightctl-realm.json: |
%[3]s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[1]s
  template:
    metadata:
      labels:
        app: %[1]s
    spec:
      containers:
        - name: keycloak
          image: %[4]s
          imagePullPolicy: IfNotPresent
          command:
            - /bin/bash
            - -c
          args:
            - /opt/keycloak/bin/kc.sh build --health-enabled=true && /opt/keycloak/bin/kc.sh start-dev --import-realm
          env:
            - name: KC_BOOTSTRAP_ADMIN_USERNAME
              value: %[5]s
            - name: KC_BOOTSTRAP_ADMIN_PASSWORD
              value: %[6]s
            - name: KC_HEALTH_ENABLED
              value: "true"
          ports:
            - name: http
              containerPort: 8080
            - name: management
              containerPort: 9000
          readinessProbe:
            httpGet:
              path: /health/ready
              port: management
            periodSeconds: 5
            failureThreshold: 24
          volumeMounts:
            - name: realm
              mountPath: /opt/keycloak/data/import/flightctl-realm.json
              subPath: flightctl-realm.json
      volumes:
        - name: realm
          configMap:
            name: %[1]s
---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  selector:
    app: %[1]s
  ports:
    - name: http
      port: 8080
      targetPort: http
---
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  to:
    kind: Service
    name: %[1]s
  port:
    targetPort: http
`, keycloakOCPName, namespace, indentBlock(string(realmJSON), 4), image, adminUser, adminPassword), nil
}

// waitForKeycloakIssuer waits until the public issuer discovery endpoint is reachable.
func waitForKeycloakIssuer(ctx context.Context, issuerURL string) error {
	discoveryURL := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	client := &http.Client{Timeout: keycloakOCPHTTPTimeout}
	deadline := time.Now().Add(keycloakOCPReadyTimeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
		if err != nil {
			return fmt.Errorf("build Keycloak discovery request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for OCP Keycloak issuer: %w", ctx.Err())
		case <-time.After(keycloakOCPPollInterval):
		}
	}
	return fmt.Errorf("keycloak realm not reachable at %s after %s", discoveryURL, keycloakOCPReadyTimeout)
}

// ocOutput runs oc and returns trimmed output with command context on failure.
func ocOutput(harness *e2e.Harness, args ...string) (string, error) {
	out, err := harness.SH("oc", args...)
	if err != nil {
		return "", fmt.Errorf("oc %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// indentBlock indents each line of a YAML block scalar by spaces.
func indentBlock(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
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

// restoreAdminClientConfig restores the suite's cached admin CLI config without creating a new login token.
func restoreAdminClientConfig(harness *e2e.Harness) error {
	if adminClientConfig == nil {
		return fmt.Errorf("admin client config snapshot is not initialized")
	}
	return restoreClientConfigSnapshot(harness, adminClientConfig)
}
