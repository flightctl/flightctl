package login

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
)

type AuthMethod int

const (
	// AuthToken indicates authentication using an OpenShift or K8s token.
	AuthToken AuthMethod = iota
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

func isLoginSuccessful(cmdOutput string) bool {
	return strings.Contains(strings.ToLower(cmdOutput), "login successful")
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

// LoginToEnv logs in to the environment (Quadlet / OpenShift / K8) with the given user and returns a token and auth method.
// serverURL is used only for OpenShift (oc login ... serverURL); empty for Quadlet/K8.
func LoginToEnv(harness *e2e.Harness, username, password, serverURL string) (string, AuthMethod, error) {
	if infra.IsQuadletEnvironment() {
		token, err := loginToEnvQuadlet(harness, username, password)
		if err != nil {
			return "", 0, err
		}
		return token, AuthPAM, nil
	}
	if util.BinaryExistsOnPath(openshift) {
		token, err := loginToEnvOpenShift(harness, username, password, serverURL)
		if err != nil {
			return "", 0, err
		}
		return token, AuthToken, nil
	}
	token, err := loginToEnvK8(harness, username, password)
	if err != nil {
		return "", 0, err
	}
	return token, AuthToken, nil
}

func loginToEnvQuadlet(harness *e2e.Harness, username, password string) (string, error) {
	if username == "" {
		username = os.Getenv("E2E_PAM_USER")
	}
	if username == "" {
		username = "admin"
	}
	if password == "" {
		password = os.Getenv("E2E_PAM_PASSWORD")
	}
	if password == "" {
		password = os.Getenv("E2E_DEFAULT_PAM_PASSWORD")
	}
	if password == "" {
		password = "flightctl-e2e" //nolint:gosec // G101: Test-only default password
	}
	loginArgs := append(baseLoginArgs(), "-u", username, "-p", password)
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		return "", fmt.Errorf("flightctl login -u -p: %w", err)
	}
	if !isLoginSuccessful(out) {
		return "", fmt.Errorf("flightctl login did not succeed: %s", out)
	}
	configPath, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return "", fmt.Errorf("getting config path after PAM login: %w", err)
	}
	cfg, err := harness.ReadClientConfig(configPath)
	if err != nil {
		return "", fmt.Errorf("reading config after PAM login: %w", err)
	}
	token := cfg.AuthInfo.AccessToken
	if cfg.AuthInfo.TokenToUse == client.TokenToUseIdToken && cfg.AuthInfo.IdToken != "" {
		token = cfg.AuthInfo.IdToken
	}
	if token == "" {
		return "", errors.New("no token in config after PAM login")
	}
	return token, nil
}

func loginToEnvOpenShift(harness *e2e.Harness, username, password, serverURL string) (string, error) {
	if serverURL == "" {
		var err error
		serverURL, err = getOpenShiftServer(harness)
		if err != nil {
			return "", fmt.Errorf("getting OpenShift server: %w", err)
		}
	}
	_, err := harness.SH(openshift, "login", "-u", username, "-p", password, serverURL, "--insecure-skip-tls-verify")
	if err != nil {
		return "", fmt.Errorf("oc login: %w", err)
	}
	token, err := harness.SH(openshift, "whoami", "-t")
	if err != nil {
		return "", fmt.Errorf("oc whoami -t: %w", err)
	}
	return strings.TrimSpace(token), nil
}

func loginToEnvK8(harness *e2e.Harness, username, password string) (string, error) {
	if username != "" || password != "" {
		return "", errors.New("K8/KIND does not support non-admin user/password login")
	}
	namespace, err := resolveFlightctlNamespace(harness)
	if err != nil {
		return "", err
	}
	token, err := harness.SH("kubectl", "-n", namespace, "create", "token", "flightctl-admin", "--duration=8h", "--context", "kind-kind")
	if err != nil {
		return "", fmt.Errorf("kubectl create token: %w", err)
	}
	return strings.TrimSpace(token), nil
}

// LoginToEnvAsAdmin returns an admin token and auth method for the current environment.
func LoginToEnvAsAdmin(harness *e2e.Harness) (string, AuthMethod, error) {
	if infra.IsQuadletEnvironment() {
		user := os.Getenv("E2E_PAM_USER")
		if user == "" {
			user = "admin"
		}
		pass := os.Getenv("E2E_PAM_PASSWORD")
		if pass == "" {
			pass = os.Getenv("E2E_DEFAULT_PAM_PASSWORD")
		}
		if pass == "" {
			pass = "flightctl-e2e" //nolint:gosec // G101: Test-only default password
		}
		return LoginToEnv(harness, user, pass, "")
	}
	if util.BinaryExistsOnPath(openshift) {
		server, err := getOpenShiftServer(harness)
		if err != nil {
			return "", 0, err
		}
		kubeadminPass := os.Getenv("KUBEADMIN_PASS")
		if kubeadminPass == "" {
			return "", 0, errors.New("KUBEADMIN_PASS not set for OpenShift admin login")
		}
		return LoginToEnv(harness, "kubeadmin", kubeadminPass, server)
	}
	token, err := loginToEnvK8(harness, "", "")
	if err != nil {
		return "", 0, err
	}
	return token, AuthToken, nil
}

// LoginToFlightctl runs flightctl login --token and refreshes the harness client.
func LoginToFlightctl(harness *e2e.Harness, token string) error {
	if token == "" {
		return errors.New("token is empty")
	}
	loginArgs := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		return fmt.Errorf("flightctl login: %w", err)
	}
	if !isLoginSuccessful(out) {
		return fmt.Errorf("flightctl login did not succeed: %s", out)
	}
	if err := harness.RefreshClient(); err != nil {
		return fmt.Errorf("refresh client after login: %w", err)
	}
	logrus.Info("Logged in to flightctl API with token")
	return nil
}

// LoginToAPIWithToken logs in as admin and persists the token.
func LoginToAPIWithToken(harness *e2e.Harness) (AuthMethod, error) {
	token, method, err := LoginToEnvAsAdmin(harness)
	if err != nil {
		return 0, fmt.Errorf("get admin token: %w", err)
	}
	if err := LoginToFlightctl(harness, token); err != nil {
		return 0, fmt.Errorf("login to flightctl: %w", err)
	}
	return method, nil
}

// Login logs in to the cluster and flightctl API as the given user.
func Login(harness *e2e.Harness, user, password string) error {
	token, _, err := LoginToEnv(harness, user, password, "")
	if err != nil {
		return fmt.Errorf("login to env: %w", err)
	}
	if err := LoginToFlightctl(harness, token); err != nil {
		return fmt.Errorf("login to flightctl: %w", err)
	}
	logrus.Infof("Logged in as %s", user)
	return nil
}
