package login_test

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testlogin "github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	providerPAM       = "pam-issuer"
	providerK8s       = "kubernetes-token"
	providerOpenShift = "openshift"
	providerAAP       = "aap"
	providerOIDC      = "oidc"
	providerOAuth2    = "oauth2"

	loginSuccessfulOutput = "Login successful"
	autoSelectOrgOutput   = "Auto-selected organization 000"
	certNotTrustedOutput  = "certificate not trusted"
	forbiddenStatusOutput = "response status: 403"

	notAuthenticatedMsg = "you must log in to perform this operation"
	authDisabledMsg     = "auth is disabled"
	getDevicesCmd       = "devices"

	pamAdminPass    = "mypassword"
	pamNonAdminPass = "mypassword"

	tlsPromptTimeout = 45 * time.Second
)

var invalidTokenErrorSubstrings = []string{
	"the token provided is invalid or expired",
	"failed to validate token",
	"invalid jwt",
	"invalid token",
	"unauthorized",
	"invalid",
}

var requiredDeviceHeaderColumns = []string{
	"NAME",
	"ALIAS",
	"OWNER",
	"SYSTEM",
	"UPDATED",
	"APPLICATIONS",
	"LAST",
	"SEEN",
}

type cmdResult struct {
	Stdout   string
	Stderr   string
	Combined string
	ExitCode int
}

type envCapabilities struct {
	Context           string
	IsOpenShift       bool
	IsKind            bool
	HasOc             bool
	OcWhoamiOK        bool
	HasKubectl        bool
	KubeClusterInfoOK bool
	HasPodman         bool
	HasPamContainer   bool
	IsDisconnected    bool
	IsStandalone      bool
	IsQuadlets        bool
	IsACMInstalled    bool
	AuthProviderInfo  map[string]string
}

type loginSpec struct {
	ProviderName string
	UseToken     bool
	Token        string
	Username     string
	Password     string
}

var _ = Describe("Login providers", Ordered, Label("login-providers"), func() {
	var (
		harness     *e2e.Harness
		caps        envCapabilities
		apiEndpoint string
	)

	BeforeAll(func() {
		apiEndpoint = strings.TrimSpace(os.Getenv("API_ENDPOINT"))
		Expect(apiEndpoint).ToNot(BeEmpty(), "API_ENDPOINT must be set by e2e runner")
	})

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		caps = detectEnvCapabilities(harness, apiEndpoint)
		GinkgoWriter.Printf("auth-provider env summary: %s\n", util.MustJSON(caps))

		checkCfgDir, err := createConfigDir("auth-enabled-check")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, checkCfgDir)
		if harness.IsAuthDisabled(apiEndpoint, checkCfgDir) {
			Skip(authDisabledMsg)
		}
	})

	It("runs PAM positive and negative auth flow", Label("88164", "sanity", "Agent"), func() {
		if caps.IsACMInstalled {
			Skip("PAM issuer flow is not applicable when ACM is installed")
		}
		if !caps.IsStandalone && !caps.IsQuadlets {
			Skip("PAM issuer flow requires standalone or quadlets environment")
		}
		if !caps.HasPamContainer {
			Skip("PAM issuer container not detected; skipping pam-issuer auth automation")
		}
		providerName := util.EnvFirst("PAM_PROVIDER_NAME")
		if providerName == "" {
			providerName = findPAMProviderName(apiEndpoint)
		}
		if providerName == "" {
			Skip("no PAM-capable OIDC provider discovered for PAM flow; set PAM_PROVIDER_NAME")
		}

		cfgDir, err := createConfigDir("pam")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, cfgDir)
		pamAdminUser, pamNonAdminUser := buildPAMTestUsers(harness)
		DeferCleanup(func() {
			cleanupErr := harness.DeletePAMUser(pamIssuerContainerName(), pamNonAdminUser)
			Expect(cleanupErr).ToNot(HaveOccurred(), "failed to cleanup pam non-admin user %q", pamNonAdminUser)
		})
		DeferCleanup(func() {
			cleanupErr := harness.DeletePAMUser(pamIssuerContainerName(), pamAdminUser)
			Expect(cleanupErr).ToNot(HaveOccurred(), "failed to cleanup pam admin user %q", pamAdminUser)
		})

		err = harness.ProvisionPAMUser(pamIssuerContainerName(), pamAdminUser, pamAdminPass, []string{"flightctl-admin"})
		Expect(err).ToNot(HaveOccurred())
		err = harness.ProvisionPAMUser(pamIssuerContainerName(), pamNonAdminUser, pamNonAdminPass, nil)
		Expect(err).ToNot(HaveOccurred())

		ranTLS, err := runTLSNegativePromptTest(harness, cfgDir, apiEndpoint)
		logTLSSkipIfNeeded(providerPAM, ranTLS)
		Expect(err).ToNot(HaveOccurred())

		positive := loginSpec{ProviderName: providerName, Username: pamAdminUser, Password: pamAdminPass}
		err = runAuthenticatedProviderFlow(harness, cfgDir, apiEndpoint, positive, caps)
		Expect(err).ToNot(HaveOccurred())
		err = forceLogout(cfgDir)
		Expect(err).ToNot(HaveOccurred())

		By("invalid credentials fail")
		bad := newCmdResult(harness.CLIWithConfigAndStdinExitCode(cfgDir, "", "login", apiEndpoint, "--provider", providerName, "-u", pamAdminUser, "-p", pamAdminPass+"-bad", "--insecure-skip-tls-verify"))
		expectNonZeroWithTrace(providerPAM, "invalid credentials", bad, caps, map[string]string{"provider": providerName, "username": pamAdminUser})
		Expect(strings.ToLower(bad.Combined)).To(
			Or(
				ContainSubstring("invalid"),
				ContainSubstring("unauthorized"),
				ContainSubstring("authentication"),
			),
		)

		By("non-admin user is denied privileged read")
		nonAdminLogin := newCmdResult(harness.CLIWithConfigAndStdinExitCode(cfgDir, "", "login", apiEndpoint, "--provider", providerName, "-u", pamNonAdminUser, "-p", pamNonAdminPass, "--insecure-skip-tls-verify"))
		expectZeroWithTrace(providerPAM, "non-admin login", nonAdminLogin, caps, map[string]string{"provider": providerName, "username": pamNonAdminUser})

		denied := newCmdResult(harness.CLIWithConfigAndStdinExitCode(cfgDir, "", "get", getDevicesCmd))
		if denied.ExitCode == 0 {
			Fail(withTrace(providerPAM, "expected non-admin device list denial", denied, caps, map[string]string{"provider": providerName, "username": pamNonAdminUser}))
		}
		Expect(strings.ToLower(denied.Combined)).To(
			Or(
				ContainSubstring(forbiddenStatusOutput),
				ContainSubstring("forbidden"),
				ContainSubstring("permission"),
				ContainSubstring("denied"),
			),
		)
		Expect(hasDeviceHeaderLine(denied.Combined)).To(BeFalse(), "non-admin output must not include device table header")
		Expect(denied.Combined).ToNot(ContainSubstring(loginSuccessfulOutput))
	})

	It("runs Kubernetes positive and negative token flow", Label("88166", "sanity", "Agent"), func() {
		if !caps.HasKubectl {
			Skip("kubectl is not available")
		}
		if !caps.KubeClusterInfoOK {
			Skip("kubectl cluster-info failed; skipping kubernetes token flow")
		}
		k8sProvider := util.EnvFirst("K8S_PROVIDER_NAME")
		if k8sProvider == "" {
			k8sProvider = findProviderNameByType(apiEndpoint, "k8s")
		}
		if k8sProvider == "" {
			Skip("kubernetes auth provider not discovered; skipping kubernetes token flow")
		}

		cfgDir, err := createConfigDir("k8s")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, cfgDir)
		ns, err := testlogin.ResolveFlightctlNamespace(harness)
		Expect(err).ToNot(HaveOccurred())
		Expect(ns).ToNot(BeEmpty())
		tokenOut, err := util.CreateK8UserToken(ns, "flightctl-admin", "1h")
		Expect(err).ToNot(HaveOccurred())
		token := strings.TrimSpace(tokenOut)
		Expect(token).ToNot(BeEmpty())

		ranTLS, err := runTLSNegativePromptTest(harness, cfgDir, apiEndpoint)
		logTLSSkipIfNeeded(providerK8s, ranTLS)
		Expect(err).ToNot(HaveOccurred())

		err = runAuthenticatedProviderFlow(harness, cfgDir, apiEndpoint, loginSpec{UseToken: true, Token: token}, caps)
		Expect(err).ToNot(HaveOccurred())
		err = forceLogout(cfgDir)
		Expect(err).ToNot(HaveOccurred())

		By("invalid token fails")
		bad := newCmdResult(harness.CLIWithConfigAndStdinExitCode(cfgDir, "", "login", apiEndpoint, "--token", token+"broken", "--insecure-skip-tls-verify"))
		expectNonZeroWithTrace(providerK8s, "invalid token", bad, caps, map[string]string{"namespace": ns})
		Expect(util.ContainsAnySubstring(strings.ToLower(bad.Combined), invalidTokenErrorSubstrings)).To(BeTrue(), "invalid token flow missing expected error markers")
		Expect(bad.Combined).ToNot(ContainSubstring(loginSuccessfulOutput))

		By("authorization denial with unbound serviceaccount")
		denyNs := "flightctl-auth-e2e-" + harness.GetTestIDFromContext()
		_, _ = harness.SH("kubectl", "create", "namespace", denyNs)
		defer harness.CleanupNamespace(denyNs)
		_, _ = harness.SH("kubectl", "-n", denyNs, "create", "sa", "deny-user")
		denyTokenOut, err := util.CreateK8UserToken(denyNs, "deny-user", "1h")
		Expect(err).ToNot(HaveOccurred())
		denyToken := strings.TrimSpace(denyTokenOut)
		Expect(denyToken).ToNot(BeEmpty())
		loginRes := newCmdResult(harness.CLIWithConfigAndStdinExitCode(cfgDir, "", "login", apiEndpoint, "--token", denyToken, "--insecure-skip-tls-verify"))
		expectZeroWithTrace(providerK8s, "deny-user login", loginRes, caps, map[string]string{"namespace": denyNs})
		denied := newCmdResult(harness.CLIWithConfigAndStdinExitCode(cfgDir, "", "get", getDevicesCmd))
		Expect(denied.ExitCode).ToNot(Equal(0), withTrace(providerK8s, "expected authorization denial", denied, caps, map[string]string{"namespace": denyNs}))
		Expect(strings.ToLower(denied.Combined)).To(
			Or(
				ContainSubstring(forbiddenStatusOutput),
				ContainSubstring("forbidden"),
				ContainSubstring("permission"),
				ContainSubstring("denied"),
			),
		)
		Expect(hasDeviceHeaderLine(denied.Combined)).To(BeFalse(), "denied output must not include device table header")
	})

	It("runs OpenShift positive and negative token flow", Label("83576", "sanity", "Agent"), func() {
		if !caps.IsOpenShift || !caps.HasOc {
			Skip("OpenShift context with oc is required")
		}
		if !caps.OcWhoamiOK {
			Skip("oc whoami failed; skipping openshift token flow")
		}
		openShiftProvider := util.EnvFirst("OPENSHIFT_PROVIDER_NAME")
		if openShiftProvider == "" {
			openShiftProvider = findProviderNameByType(apiEndpoint, "openshift")
		}
		if openShiftProvider == "" {
			Skip("openshift auth provider not discovered")
		}
		cfgDir, err := createConfigDir("ocp")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, cfgDir)
		tokenOut, err := harness.SH("oc", "whoami", "-t")
		Expect(err).ToNot(HaveOccurred())
		token := strings.TrimSpace(tokenOut)
		Expect(token).ToNot(BeEmpty())

		ranTLS, err := runTLSNegativePromptTest(harness, cfgDir, apiEndpoint)
		logTLSSkipIfNeeded(providerOpenShift, ranTLS)
		Expect(err).ToNot(HaveOccurred())
		err = runAuthenticatedProviderFlow(harness, cfgDir, apiEndpoint, loginSpec{UseToken: true, Token: token}, caps)
		Expect(err).ToNot(HaveOccurred())
		err = forceLogout(cfgDir)
		Expect(err).ToNot(HaveOccurred())

		bad := newCmdResult(harness.CLIWithConfigAndStdinExitCode(cfgDir, "", "login", apiEndpoint, "--token", token+"broken", "--insecure-skip-tls-verify"))
		expectNonZeroWithTrace(providerOpenShift, "invalid openshift token", bad, caps, nil)
		Expect(util.ContainsAnySubstring(strings.ToLower(bad.Combined), invalidTokenErrorSubstrings)).To(BeTrue(), "invalid token flow missing expected error markers")
		Expect(bad.Combined).ToNot(ContainSubstring(loginSuccessfulOutput))
	})

	It("runs AAP positive and negative flow when credentials are configured", Label("88169", "sanity", "Agent"), func() {
		providerName := util.EnvFirst("AAP_PROVIDER_NAME")
		if providerName == "" {
			providerName = findProviderNameByType(apiEndpoint, "aap")
		}
		if providerName == "" {
			Skip("AAP provider not discovered; set AAP_PROVIDER_NAME")
		}
		cfgDir, err := createConfigDir("aap")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, cfgDir)
		ranTLS, err := runTLSNegativePromptTest(harness, cfgDir, apiEndpoint)
		logTLSSkipIfNeeded(providerAAP, ranTLS)
		Expect(err).ToNot(HaveOccurred())

		spec, ok := providerLoginSpecFromEnv("AAP", providerName)
		if !ok {
			Skip("AAP credentials/token not configured; set AAP_TEST_TOKEN or AAP_TEST_USERNAME/AAP_TEST_PASSWORD")
		}
		err = runAuthenticatedProviderFlow(harness, cfgDir, apiEndpoint, spec, caps)
		Expect(err).ToNot(HaveOccurred())
		err = forceLogout(cfgDir)
		Expect(err).ToNot(HaveOccurred())
		err = runInvalidCredsNegativeIfPossible(providerAAP, harness, cfgDir, apiEndpoint, spec, caps)
		Expect(err).ToNot(HaveOccurred())
	})

	It("runs OIDC positive and negative flow when credentials are configured", Label("88168", "sanity", "Agent"), func() {
		providerName := util.EnvFirst("OIDC_PROVIDER_NAME")
		if providerName == "" {
			providerName = findProviderNameByType(apiEndpoint, "oidc")
		}
		if providerName == "" {
			Skip("OIDC provider not discovered; set OIDC_PROVIDER_NAME")
		}
		cfgDir, err := createConfigDir("oidc")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, cfgDir)
		ranTLS, err := runTLSNegativePromptTest(harness, cfgDir, apiEndpoint)
		logTLSSkipIfNeeded(providerOIDC, ranTLS)
		Expect(err).ToNot(HaveOccurred())

		spec, ok := providerLoginSpecFromEnv("OIDC", providerName)
		if !ok {
			Skip("OIDC credentials/token not configured; set OIDC_TEST_TOKEN or OIDC_TEST_USERNAME/OIDC_TEST_PASSWORD")
		}
		err = runAuthenticatedProviderFlow(harness, cfgDir, apiEndpoint, spec, caps)
		Expect(err).ToNot(HaveOccurred())
		err = forceLogout(cfgDir)
		Expect(err).ToNot(HaveOccurred())
		err = runInvalidCredsNegativeIfPossible(providerOIDC, harness, cfgDir, apiEndpoint, spec, caps)
		Expect(err).ToNot(HaveOccurred())
	})

	It("runs OAuth2 positive and negative flow when credentials are configured", Label("88167", "sanity", "Agent"), func() {
		providerName := util.EnvFirst("OAUTH2_PROVIDER_NAME")
		if providerName == "" {
			providerName = findProviderNameByType(apiEndpoint, "oauth2")
		}
		if providerName == "" {
			Skip("OAuth2 provider not discovered; set OAUTH2_PROVIDER_NAME")
		}
		cfgDir, err := createConfigDir("oauth2")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, cfgDir)
		ranTLS, err := runTLSNegativePromptTest(harness, cfgDir, apiEndpoint)
		logTLSSkipIfNeeded(providerOAuth2, ranTLS)
		Expect(err).ToNot(HaveOccurred())

		spec, ok := providerLoginSpecFromEnv("OAUTH2", providerName)
		if !ok {
			Skip("OAuth2 credentials/token not configured; set OAUTH2_TEST_TOKEN or OAUTH2_TEST_USERNAME/OAUTH2_TEST_PASSWORD")
		}
		err = runAuthenticatedProviderFlow(harness, cfgDir, apiEndpoint, spec, caps)
		Expect(err).ToNot(HaveOccurred())
		err = forceLogout(cfgDir)
		Expect(err).ToNot(HaveOccurred())
		err = runInvalidCredsNegativeIfPossible(providerOAuth2, harness, cfgDir, apiEndpoint, spec, caps)
		Expect(err).ToNot(HaveOccurred())
	})
})

func runAuthenticatedProviderFlow(h *e2e.Harness, cfgDir, apiEndpoint string, spec loginSpec, caps envCapabilities) error {
	By("login succeeds")
	login := runFlightctlCmd(h, cfgDir, "", loginCommandArgs(apiEndpoint, spec)...)
	if login.ExitCode != 0 {
		return fmt.Errorf("%s", withTrace(spec.ProviderName, "positive login expected success", login, caps, map[string]string{"provider": spec.ProviderName}))
	}
	if !strings.Contains(login.Combined, loginSuccessfulOutput) {
		return fmt.Errorf("login output missing success marker: %s", withTrace(spec.ProviderName, "positive login", login, caps, nil))
	}

	By("organization auto-selection message is present")
	if err := validateOrgAutoSelectionMessage(login.Combined); err != nil {
		return err
	}

	By("authenticated command succeeds")
	if err := validateAuthenticatedCommands(h, cfgDir, spec.ProviderName, caps); err != nil {
		return err
	}

	By("logout equivalent then unauthenticated access fails")
	return validateLogoutRemovesAuth(h, cfgDir, spec.ProviderName, caps)
}

func runInvalidCredsNegativeIfPossible(provider string, h *e2e.Harness, cfgDir, apiEndpoint string, spec loginSpec, caps envCapabilities) error {
	By("invalid credentials or token fail")
	if spec.UseToken {
		bad := runFlightctlCmd(h, cfgDir, "", "login", apiEndpoint, "--token", spec.Token+"broken", "--insecure-skip-tls-verify")
		if bad.ExitCode == 0 {
			return fmt.Errorf("%s", withTrace(provider, "invalid token expected failure", bad, caps, nil))
		}
		lower := strings.ToLower(bad.Combined)
		if !util.ContainsAnySubstring(lower, invalidTokenErrorSubstrings) {
			return fmt.Errorf("invalid token flow missing expected unauthorized/invalid output")
		}
		if strings.Contains(bad.Combined, loginSuccessfulOutput) {
			return fmt.Errorf("invalid token flow must not contain %q", loginSuccessfulOutput)
		}
		return nil
	}
	if spec.Username == "" || spec.Password == "" {
		return nil
	}
	bad := runFlightctlCmd(h, cfgDir, "", "login", apiEndpoint, "--provider", spec.ProviderName, "-u", spec.Username, "-p", spec.Password+"-bad", "--insecure-skip-tls-verify")
	if bad.ExitCode == 0 {
		return fmt.Errorf("%s", withTrace(provider, "invalid credentials expected failure", bad, caps, nil))
	}
	if strings.Contains(bad.Combined, loginSuccessfulOutput) {
		return fmt.Errorf("invalid credentials flow must not contain %q", loginSuccessfulOutput)
	}
	return nil
}

func runTLSNegativePromptTest(h *e2e.Harness, cfgDir, apiEndpoint string) (bool, error) {
	By("TLS prompt with rejecting untrusted certificate")
	fullArgs := []string{"login", apiEndpoint, "--show-providers"}
	if cfgDir != "" {
		fullArgs = append(fullArgs, "--config-dir", cfgDir)
	}
	combined, err := h.RunInteractiveCLIWithInput(tlsPromptTimeout, "n\n", fullArgs...)
	if err != nil {
		return false, err
	}
	if !strings.Contains(strings.ToLower(combined), "certificate") {
		return false, nil
	}
	lower := strings.ToLower(combined)
	if !strings.Contains(lower, certNotTrustedOutput) {
		return true, fmt.Errorf("TLS rejection output missing expected trust failure text %q", certNotTrustedOutput)
	}
	if !strings.Contains(combined, "Do you want to continue? (y/n):") && !strings.Contains(combined, "Use insecure connections? (y/N):") {
		return true, fmt.Errorf("TLS rejection output missing expected prompt")
	}
	if !strings.Contains(lower, "certificate-authority") || !strings.Contains(lower, "insecure-skip-tls-verify") {
		return true, fmt.Errorf("TLS rejection output missing expected remediation flags")
	}
	if strings.Contains(combined, loginSuccessfulOutput) {
		return true, fmt.Errorf("TLS rejection flow must not contain %q", loginSuccessfulOutput)
	}
	return true, nil
}

func logTLSSkipIfNeeded(provider string, ranTLS bool) {
	if ranTLS {
		return
	}
	GinkgoWriter.Printf("tls negative prompt check skipped for provider %s: environment did not trigger trust prompt\n", provider)
}

func detectEnvCapabilities(h *e2e.Harness, apiEndpoint string) envCapabilities {
	ctx, _ := e2e.GetContext()
	caps := envCapabilities{
		Context:           ctx,
		IsOpenShift:       ctx == util.OCP,
		IsKind:            ctx == util.KIND,
		HasOc:             util.BinaryExistsOnPath("oc"),
		OcWhoamiOK:        false,
		HasKubectl:        util.BinaryExistsOnPath("kubectl"),
		KubeClusterInfoOK: false,
		HasPodman:         util.BinaryExistsOnPath("podman"),
		IsDisconnected:    util.IsTruthy(util.EnvFirst("FLIGHTCTL_DISCONNECTED", "DISCONNECTED", "AIRGAPPED")),
		IsStandalone:      util.IsTruthy(util.EnvFirst("FLIGHTCTL_STANDALONE", "STANDALONE")),
		IsQuadlets:        util.IsTruthy(util.EnvFirst("FLIGHTCTL_QUADLETS", "QUADLETS")),
		AuthProviderInfo:  map[string]string{},
	}
	if caps.HasOc {
		if _, err := h.SH("oc", "whoami"); err == nil {
			caps.OcWhoamiOK = true
		}
	}
	if caps.HasKubectl {
		if _, err := h.SH("kubectl", "cluster-info"); err == nil {
			caps.KubeClusterInfoOK = true
		}
	}
	if _, running, err := util.IsAcmInstalled(); err == nil {
		caps.IsACMInstalled = running
	}
	caps.HasPamContainer = h.IsPodmanContainerRunning(pamIssuerContainerName())
	if cfg, err := fetchAuthConfig(apiEndpoint); err == nil {
		for _, p := range cfg {
			caps.AuthProviderInfo[p.Name] = p.Type
		}
	}
	return caps
}

type discoveredProvider struct {
	Name        string
	Type        string
	DisplayName string
	Issuer      string
	Enabled     bool
}

func fetchAuthConfig(apiEndpoint string) ([]discoveredProvider, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec
			MinVersion:         tls.VersionTLS12,
		},
	}
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}
	resp, err := client.Get(strings.TrimRight(apiEndpoint, "/") + "/api/v1/auth/config")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var authCfg v1beta1.AuthConfig
	if err := json.Unmarshal(body, &authCfg); err != nil {
		return nil, err
	}
	out := make([]discoveredProvider, 0)
	if authCfg.Providers == nil {
		return out, nil
	}
	for _, p := range *authCfg.Providers {
		name := ""
		if p.Metadata.Name != nil {
			name = *p.Metadata.Name
		}
		typ, _ := p.Spec.Discriminator()
		discovered := discoveredProvider{
			Name:    name,
			Type:    strings.ToLower(typ),
			Enabled: true,
		}
		if strings.EqualFold(discovered.Type, "oidc") {
			if oidcSpec, err := p.Spec.AsOIDCProviderSpec(); err == nil {
				if oidcSpec.DisplayName != nil {
					discovered.DisplayName = strings.TrimSpace(*oidcSpec.DisplayName)
				}
				discovered.Issuer = strings.TrimSpace(oidcSpec.Issuer)
				if oidcSpec.Enabled != nil {
					discovered.Enabled = *oidcSpec.Enabled
				}
			}
		}
		out = append(out, discovered)
	}
	return out, nil
}

func findProviderNameByType(apiEndpoint, providerType string) string {
	providers, err := fetchAuthConfig(apiEndpoint)
	if err != nil {
		return ""
	}
	for _, p := range providers {
		if strings.EqualFold(p.Type, providerType) && p.Name != "" {
			return p.Name
		}
	}
	return ""
}

func findPAMProviderName(apiEndpoint string) string {
	providers, err := fetchAuthConfig(apiEndpoint)
	if err != nil {
		return ""
	}
	for _, p := range providers {
		if !strings.EqualFold(p.Type, "oidc") || p.Name == "" || !p.Enabled {
			continue
		}
		if isLikelyPAMProvider(p) {
			return p.Name
		}
	}
	return ""
}

func isLikelyPAMProvider(p discoveredProvider) bool {
	check := strings.ToLower(strings.Join([]string{p.Name, p.DisplayName, p.Issuer}, " "))
	return strings.Contains(check, "pam-issuer") || strings.Contains(check, "pam issuer") || strings.Contains(check, "/pam")
}

func providerLoginSpecFromEnv(prefix, providerName string) (loginSpec, bool) {
	token := util.EnvFirst(prefix+"_TEST_TOKEN", "FLIGHTCTL_"+prefix+"_TEST_TOKEN")
	if token != "" {
		return loginSpec{ProviderName: providerName, UseToken: true, Token: token}, true
	}
	user := util.EnvFirst(prefix+"_TEST_USERNAME", "FLIGHTCTL_"+prefix+"_TEST_USERNAME")
	pass := util.EnvFirst(prefix+"_TEST_PASSWORD", "FLIGHTCTL_"+prefix+"_TEST_PASSWORD")
	if user != "" && pass != "" {
		return loginSpec{ProviderName: providerName, Username: user, Password: pass}, true
	}
	return loginSpec{}, false
}

func loginCommandArgs(apiEndpoint string, spec loginSpec) []string {
	args := []string{"login", apiEndpoint, "--insecure-skip-tls-verify"}
	if spec.UseToken {
		return append(args, "--token", spec.Token)
	}
	return append(args, "--provider", spec.ProviderName, "-u", spec.Username, "-p", spec.Password)
}

func validateOrgAutoSelectionMessage(loginOutput string) error {
	if strings.Contains(loginOutput, autoSelectOrgOutput) {
		return nil
	}
	// fallback to validating shape for non-default org ids while preserving canonical assert
	matched, err := regexp.MatchString(`Auto-selected organization:?\s+[0-9a-fA-F-]{3,}`, loginOutput)
	if err != nil || !matched {
		return fmt.Errorf("login output missing expected org auto-selection marker %q", autoSelectOrgOutput)
	}
	return nil
}

func validateAuthenticatedCommands(h *e2e.Harness, cfgDir, provider string, caps envCapabilities) error {
	orgs := runFlightctlCmd(h, cfgDir, "", "get", "organization", "-o", "name")
	if orgs.ExitCode != 0 {
		return fmt.Errorf("%s", withTrace(provider, "get organization expected success", orgs, caps, nil))
	}

	devices := runFlightctlCmd(h, cfgDir, "", "get", getDevicesCmd)
	if devices.ExitCode != 0 {
		return fmt.Errorf("%s", withTrace(provider, "get device expected success", devices, caps, nil))
	}
	if !hasDeviceHeaderLine(devices.Stdout) {
		return fmt.Errorf("device output missing expected header columns: %v", requiredDeviceHeaderColumns)
	}
	return nil
}

func validateLogoutRemovesAuth(h *e2e.Harness, cfgDir, provider string, caps envCapabilities) error {
	if err := forceLogout(cfgDir); err != nil {
		return fmt.Errorf("failed to remove client config for logout: %w", err)
	}
	unauth := runFlightctlCmd(h, cfgDir, "", "get", "organization")
	if unauth.ExitCode == 0 {
		return fmt.Errorf("%s", withTrace(provider, "expected unauthenticated command failure", unauth, caps, nil))
	}
	if !strings.Contains(strings.ToLower(unauth.Combined), notAuthenticatedMsg) {
		return fmt.Errorf("unauthenticated error output missing expected text: %q", notAuthenticatedMsg)
	}
	return nil
}

func newCmdResult(out string, code int, err error) cmdResult {
	stderr := ""
	if err != nil {
		stderr = err.Error()
	}
	if code == 0 {
		stderr = ""
	}
	return cmdResult{Stdout: out, Stderr: stderr, Combined: out + stderr, ExitCode: code}
}

func runFlightctlCmd(h *e2e.Harness, cfgDir, stdin string, args ...string) cmdResult {
	return newCmdResult(h.CLIWithConfigAndStdinExitCode(cfgDir, stdin, args...))
}

func expectZeroWithTrace(provider, step string, res cmdResult, caps envCapabilities, meta map[string]string) {
	if res.ExitCode != 0 {
		Fail(withTrace(provider, step+" expected success", res, caps, meta))
	}
}

func expectNonZeroWithTrace(provider, step string, res cmdResult, caps envCapabilities, meta map[string]string) {
	if res.ExitCode == 0 {
		Fail(withTrace(provider, step+" expected failure", res, caps, meta))
	}
}

func withTrace(provider, step string, res cmdResult, caps envCapabilities, meta map[string]string) string {
	return fmt.Sprintf(
		"provider=%s step=%s exit=%d\nstdout:\n%s\nstderr:\n%s\nenv:%s\nmeta:%s",
		provider,
		step,
		res.ExitCode,
		res.Stdout,
		res.Stderr,
		util.MustJSON(caps),
		util.MustJSON(meta),
	)
}

func createConfigDir(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", "flightctl-auth-"+prefix+"-")
	if err != nil {
		return "", err
	}
	return dir, nil
}

func forceLogout(configDir string) error {
	err := os.Remove(filepath.Join(configDir, "client.yaml"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func pamIssuerContainerName() string {
	return util.DefaultIfEmpty(util.EnvFirst("PAM_ISSUER_CONTAINER"), "flightctl-pam-issuer")
}

func hasDeviceHeaderLine(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != len(requiredDeviceHeaderColumns) {
			continue
		}
		matches := true
		for i := range requiredDeviceHeaderColumns {
			if fields[i] != requiredDeviceHeaderColumns[i] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func buildPAMTestUsers(h *e2e.Harness) (string, string) {
	suffix := "default"
	if h != nil {
		id := strings.ToLower(strings.TrimSpace(h.GetTestIDFromContext()))
		if id != "" {
			suffix = sanitizeUnixSuffix(id)
		}
	}
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return "pamalice" + suffix, "pambob" + suffix
}

func sanitizeUnixSuffix(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "default"
	}
	return out
}
