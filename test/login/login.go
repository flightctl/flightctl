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
	return WithPassword(harness)
}

// WithPassword attempts to log in to the flightctl API with only the user/password flow.
// If auth is disabled then this is largely a noop. This method panics if login fails
func WithPassword(harness *e2e.Harness) AuthMethod {
	if !isAuthEnabled(harness) {
		return AuthDisabled
	}
	authMethod, err := loginWithPassword(harness)
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
	return slices.Contains([]string{"auth is disabled", "login successful", "login successful."},
		strings.ToLower(strings.TrimSpace(cmdOutput)))
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

func loginWithPassword(harness *e2e.Harness) (AuthMethod, error) {
	namespace, err := resolveFlightctlNamespace(harness)
	Expect(err).ToNot(HaveOccurred(), "error resolving flightctl namespace")
	Expect(namespace).NotTo(BeEmpty(), "Unable to determine the namespace associated with the demo user")

	secret, err := harness.Cluster.CoreV1().Secrets(namespace).Get(harness.Context, "keycloak-demouser-secret", metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred(), "error getting user password")
	password := secret.Data["password"]
	Expect(password).ToNot(BeEmpty(), "Password of demouser should not be empty")

	// Retry login with the retrieved password
	loginArgs := append(baseLoginArgs(), "-u", "demouser", "-p", string(password))
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		return AuthDisabled, fmt.Errorf("error executing login: %w", err)
	}
	if isLoginSuccessful(out) {
		return AuthUsernamePassword, nil
	}
	return AuthDisabled, errors.New("failed to sign in with user name and password")
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
