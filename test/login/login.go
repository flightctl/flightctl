package login

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
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

func loginWithToken(harness *e2e.Harness) (token string, err error) {
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return "", fmt.Errorf("infra provider not set (call setup.EnsureDefaultProviders() first)")
	}
	token, err = p.Infra.GetAPILoginToken()
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("API login token is empty")
	}
	return token, nil
}

func loginWithK8Token(harness *e2e.Harness) (AuthMethod, error) {
	token, err := loginWithToken(harness)
	if err != nil {
		return AuthDisabled, fmt.Errorf("error getting API login token: %w", err)
	}
	loginArgs := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		return AuthDisabled, fmt.Errorf("error executing login: %w", err)
	}
	if isLoginSuccessful(out) {
		return AuthToken, nil
	}
	return AuthDisabled, errors.New("failed to sign in with token")
}

func loginWithOpenshiftToken(harness *e2e.Harness) error {
	token, err := loginWithToken(harness)
	if err != nil {
		return fmt.Errorf("getting API login token: %w", err)
	}
	loginArgsOcp := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgsOcp...)
	if err != nil {
		return fmt.Errorf("failed to sign in with OpenShift token: %w", err)
	}
	if isLoginSuccessful(out) {
		return nil
	}
	return errors.New("failed to sign in with OpenShift token")
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
	} else {
		logrus.Infof("✅ Logged in to Kubernetes cluster as non-admin: %s", user)
	}

	method := LoginToAPIWithToken(harness)
	Expect(method).ToNot(Equal(AuthDisabled))
	if method == AuthDisabled {
		return errors.New("Login is disabled")
	}

	// Refresh the harness client to pick up the updated organization from the config file
	// The login may have updated the organization context in the config
	err = harness.RefreshClient()
	if err != nil {
		return fmt.Errorf("failed to refresh client after login: %w", err)
	}

	return nil
}
