package login

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AuthMethod int

const (
	// AuthDisabled indicates that authentication is not enabled for the deployment
	AuthDisabled AuthMethod = iota

	// AuthToken indicates authentication using an OpenShift token.
	AuthToken

	// AuthUsernamePassword indicates authentication using a username and password.
	AuthUsernamePassword
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
	authMethod, err := loginWithK8Token(harness)
	Expect(err).ToNot(HaveOccurred(), "Authentication was unsuccessful")
	return authMethod
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

func getActiveNamespaces(harness *e2e.Harness) []string {
	res, err := harness.Cluster.CoreV1().Namespaces().List(harness.Context, metav1.ListOptions{FieldSelector: "status.phase=Active"})
	Expect(err).ToNot(HaveOccurred(), "error listing namespaces")
	namespaces := make([]string, 0, len(res.Items))
	for _, item := range res.Items {
		namespaces = append(namespaces, strings.ToLower(strings.TrimSpace(item.Name)))
	}
	return namespaces
}

func resolveFlightctlNamespace(harness *e2e.Harness) (string, error) {
	namespaces := getActiveNamespaces(harness)
	flightCtlNs := os.Getenv("FLIGHTCTL_NS")
	// if the NS env variable is set we only check that one
	if flightCtlNs != "" {
		const fmtString = "unable to resolve flightctl namespace. %s is defined as the namespace but does not exist in the collection %v"
		if !slices.Contains(namespaces, flightCtlNs) {
			return "", fmt.Errorf(fmtString, flightCtlNs, namespaces)
		}
		return flightCtlNs, nil
	}
	wellKnownNs := []string{"flightctl", "flightctl-external"}
	for _, ns := range wellKnownNs {
		if slices.Contains(namespaces, ns) {
			return ns, nil
		}
	}
	return "", fmt.Errorf("unable to resolve flightctl namespace using well known namespaces %v", wellKnownNs)
}

func loginWithK8Token(harness *e2e.Harness) (AuthMethod, error) {
	namespace, err := resolveFlightctlNamespace(harness)
	Expect(err).ToNot(HaveOccurred(), "error resolving flightctl namespace")
	Expect(namespace).NotTo(BeEmpty(), "Unable to determine the namespace associated with the demo user")

	// Get Kubernetes service account token
	token, err := harness.SH("kubectl", "-n", namespace, "create", "token", "flightctl-admin", "--context", "kind-kind")
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
		return AuthToken, nil
	}
	return AuthDisabled, errors.New("failed to sign in with token")
}

func loginWithOpenshiftToken(harness *e2e.Harness) error {
	token, err := harness.SH(openshift, "whoami", "-t")
	// If whoami fails just try logging in with the user
	if err != nil {
		return fmt.Errorf("calling oc whoami: %w", err)
	}
	// otherwise try logging in with the openshift token but still fallback
	// to regular username/password if that fails
	token = strings.TrimSpace(token)
	Expect(token).ToNot(BeEmpty(), "Token from 'oc whoami' should not be empty")
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
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("user is empty")
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("password is empty")
	}
	if strings.TrimSpace(k8sApiEndpoint) == "" {
		return fmt.Errorf("k8s api endpoint is empty")
	}

	if strings.TrimSpace(k8sContext) != "" {
		if _, err := harness.ChangeK8sContext(harness.Context, k8sContext); err != nil {
			return fmt.Errorf("failed to change k8s context to %q: %w", k8sContext, err)
		}
	}

	if _, err := harness.SH("oc", "login", "-u", user, "-p", password, k8sApiEndpoint); err != nil {
		return fmt.Errorf("failed to login to Kubernetes cluster as non-admin: %w", err)
	}

	token, err := harness.GetOpenShiftToken()
	if err != nil {
		return fmt.Errorf("failed to get openshift token for non-admin user %q: %w", user, err)
	}
	loginArgs := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		return fmt.Errorf("failed to login to flightctl API as non-admin user %q: %w, output: %s", user, err, strings.TrimSpace(out))
	}
	if !isLoginSuccessful(out) {
		return errors.New("failed to sign in with non-admin openshift token")
	}

	// Refresh the harness client to pick up the updated organization from the config file
	// The login may have updated the organization context in the config
	if err := harness.RefreshClient(); err != nil {
		return fmt.Errorf("failed to refresh client after login: %w", err)
	}

	return nil
}
