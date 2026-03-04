package login

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

type AuthMethod int

const (
	// AuthDisabled indicates that authentication is not enabled for the deployment
	AuthDisabled AuthMethod = iota

	// AuthToken indicates authentication using an OpenShift token.
	AuthToken

	// AuthUsernamePassword indicates authentication using a username and password.
	AuthUsernamePassword

	// AuthPAM indicates authentication using PAM (for Quadlet deployments)
	AuthPAM
)

const (
	openshift = "oc"
)

func baseLoginArgs() []string {
	return []string{"login", "${API_ENDPOINT}", "--insecure-skip-tls-verify"}
}

// LoginToAPIWithToken attempts to log in to the flightctl API via different methods depending on what is available.
// If auth is disabled then this is largely a noop. This method panics if login fails
func LoginToAPIWithToken(harness *e2e.Harness) AuthMethod {
	if !isAuthEnabled(harness) {
		return AuthDisabled
	}

	// For Quadlet deployments, use PAM authentication
	if infra.IsQuadletEnvironment() {
		return loginWithPAM(harness)
	}

	ocExists := util.BinaryExistsOnPath(openshift)
	if ocExists {
		// If openshift token login fails then fallback to username/password
		if err := loginWithOpenshiftToken(harness); err == nil {
			return AuthToken
		}
	}
	return WithK8Token(harness)
}

// WithK8Token attempts to log in to the flightctl API with only the user/password flow.
// If auth is disabled then this is largely a noop. This method panics if login fails
func WithK8Token(harness *e2e.Harness) AuthMethod {
	if !isAuthEnabled(harness) {
		return AuthDisabled
	}

	// For Quadlet deployments, use PAM authentication instead of K8s token
	if infra.IsQuadletEnvironment() {
		return loginWithPAM(harness)
	}

	authMethod, err := loginWithK8Token(harness)
	Expect(err).ToNot(HaveOccurred(), "Authentication was unsuccessful")
	return authMethod
}

// loginWithPAM authenticates using PAM credentials for Quadlet deployments
func loginWithPAM(harness *e2e.Harness) AuthMethod {
	// Get PAM credentials from environment or use defaults
	pamUser := os.Getenv("E2E_PAM_USER")
	if pamUser == "" {
		pamUser = "admin"
	}
	pamPassword := os.Getenv("E2E_PAM_PASSWORD")
	if pamPassword == "" {
		pamPassword = os.Getenv("E2E_DEFAULT_PAM_PASSWORD")
	}
	if pamPassword == "" {
		pamPassword = "flightctl-e2e" //nolint:gosec // G101: Test-only default password, not production credentials
	}

	logrus.Infof("Attempting PAM login with user: %s", pamUser)

	loginArgs := append(baseLoginArgs(), "-u", pamUser, "-p", pamPassword)
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		logrus.Warnf("PAM login failed: %v, output: %s", err, out)
		// Don't panic - return AuthDisabled and let test handle it
		Expect(err).ToNot(HaveOccurred(), "PAM authentication was unsuccessful: %s", out)
		return AuthDisabled
	}

	if isLoginSuccessful(out) {
		logrus.Info("PAM login successful")
		return AuthPAM
	}

	logrus.Warnf("PAM login did not succeed: %s", out)
	return AuthDisabled
}

func isAuthEnabled(harness *e2e.Harness) bool {
	// run the basic login method first to see if auth is disabled
	testForDisabledArgs := append(baseLoginArgs(), "--token", "fake-token")
	out, _ := harness.CLI(testForDisabledArgs...)
	return !strings.EqualFold(strings.TrimSpace(out), "auth is disabled")
}

func isLoginSuccessful(cmdOutput string) bool {
	out := strings.ToLower(cmdOutput)
	return strings.Contains(out, "auth is disabled") ||
		strings.Contains(out, "login successful")
}

func resolveFlightctlNamespace(harness *e2e.Harness) (string, error) {
	flightCtlNs := os.Getenv("FLIGHTCTL_NS")
	if flightCtlNs != "" {
		_, err := harness.SH("kubectl", "get", "namespace", flightCtlNs)
		if err != nil {
			return "", fmt.Errorf("FLIGHTCTL_NS=%s but namespace does not exist or is not accessible: %w", flightCtlNs, err)
		}
		return flightCtlNs, nil
	}
	wellKnownNs := []string{"flightctl", "flightctl-external"}
	for _, ns := range wellKnownNs {
		_, err := harness.SH("kubectl", "get", "namespace", ns)
		if err == nil {
			return ns, nil
		}
	}
	return "", fmt.Errorf("unable to resolve flightctl namespace: set FLIGHTCTL_NS or ensure one of %v exists", wellKnownNs)
}

func loginWithK8Token(harness *e2e.Harness) (AuthMethod, error) {
	namespace, err := resolveFlightctlNamespace(harness)
	Expect(err).ToNot(HaveOccurred(), "error resolving flightctl namespace")
	Expect(namespace).NotTo(BeEmpty(), "Unable to determine the namespace associated with the demo user")

	// Get Kubernetes service account token (long duration so it does not expire mid-e2e)
	token, err := harness.SH("kubectl", "-n", namespace, "create", "token", "flightctl-admin", "--duration=8h", "--context", "kind-kind")
	if err != nil {
		return AuthDisabled, fmt.Errorf("error creating service account token: %w", err)
	}
	token = strings.TrimSpace(token)
	Expect(token).ToNot(BeEmpty(), "Token from 'kubectl create token' should not be empty")

	// Login with the retrieved token
	loginArgs := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		return AuthDisabled, fmt.Errorf("error executing login: %w", err)
	}
	if isLoginSuccessful(out) {
		logrus.Info("Logged in to flightctl API with K8 service account token")
		return AuthToken, nil
	}
	return AuthDisabled, errors.New("failed to sign in with token")
}

func getOpenShiftServer(harness *e2e.Harness) (string, error) {
	out, err := harness.SH(openshift, "whoami", "--show-server")
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	out, err = harness.SH("kubectl", "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
	if err != nil {
		return "", fmt.Errorf("could not get OpenShift server from oc whoami or kubeconfig: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func loginWithOpenshiftToken(harness *e2e.Harness) error {
	if kubeadminPass := os.Getenv("KUBEADMIN_PASS"); kubeadminPass != "" {
		server, err := getOpenShiftServer(harness)
		if err != nil {
			return fmt.Errorf("getting OpenShift server for oc login: %w", err)
		}
		_, err = harness.SH(openshift, "login", "-u", "kubeadmin", "-p", kubeadminPass, server, "--insecure-skip-tls-verify")
		if err != nil {
			return fmt.Errorf("oc login as kubeadmin: %w", err)
		}
		logrus.Info("Logged in to OpenShift cluster as kubeadmin (refreshed session for full token lifetime)")
	}
	token, err := harness.SH(openshift, "whoami", "-t")
	if err != nil {
		return fmt.Errorf("calling oc whoami: %w", err)
	}
	token = strings.TrimSpace(token)
	Expect(token).ToNot(BeEmpty(), "Token from 'oc whoami' should not be empty")
	loginArgsOcp := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgsOcp...)
	if err != nil {
		return fmt.Errorf("failed to sign in with OpenShift token: %w", err)
	}
	if isLoginSuccessful(out) {
		logrus.Info("Logged in to flightctl API with OpenShift token")
		return nil
	}
	return errors.New("failed to sign in with OpenShift token")
}

// loginWithCurrentOpenShiftToken logs in to the flightctl API using the current oc user's token.
// Used by LoginAsNonAdmin so we do not overwrite the session with kubeadmin.
func loginWithCurrentOpenShiftToken(harness *e2e.Harness) error {
	token, err := harness.SH(openshift, "whoami", "-t")
	if err != nil {
		return fmt.Errorf("oc whoami -t: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("oc whoami -t returned empty token")
	}
	loginArgs := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		return fmt.Errorf("flightctl login with current OpenShift token: %w", err)
	}
	if !isLoginSuccessful(out) {
		return errors.New("flightctl login did not succeed with current OpenShift token")
	}
	logrus.Info("Logged in to flightctl API with OpenShift token")
	return nil
}

func LoginAsNonAdmin(harness *e2e.Harness, user string, password string, k8sContext string, k8sApiEndpoint string) error {
	if !util.BinaryExistsOnPath("oc") {
		return fmt.Errorf("oc not found on PATH")
	}
	loginCommand := fmt.Sprintf("oc login -u %s -p %s %s", user, password, k8sApiEndpoint)
	cmd := exec.Command("bash", "-c", loginCommand)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("Failed to login to Kubernetes cluster as non-admin: %v", err)
	}
	logrus.Infof("✅ Logged in to Kubernetes cluster as non-admin: %s", user)

	// Use current oc user's token directly; do not call isAuthEnabled (it runs login with fake-token and is redundant when auth is always enabled).
	if err := loginWithCurrentOpenShiftToken(harness); err != nil {
		return fmt.Errorf("login to flightctl API as non-admin: %w", err)
	}

	err = harness.RefreshClient()
	if err != nil {
		return fmt.Errorf("failed to refresh client after login: %w", err)
	}
	return nil
}
