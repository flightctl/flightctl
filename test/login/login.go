package login

import (
	"errors"
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
		authMethod, err := loginWithOpenshift(harness)
		// try password login if openshift login fails
		if err != nil {
			return WithPassword(harness)
		} else {
			return authMethod
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

func loginWithPassword(harness *e2e.Harness) (AuthMethod, error) {
	namespaces := getActiveNamespaces(harness)
	namespace := ""
	// is this a legacy thing?
	if slices.Contains(namespaces, "flightctl") {
		namespace = "flightctl"
	} else if slices.Contains(namespaces, "flightctl-external") {
		namespace = "flightctl-external"
	}
	Expect(namespace).NotTo(BeEmpty(), "Unable to determine the namespace associated with the demo user")

	secret, err := harness.Cluster.CoreV1().Secrets(namespace).Get(harness.Context, "keycloak-demouser-secret", metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred(), "error getting user password")
	password := secret.Data["password"]
	Expect(password).ToNot(BeEmpty(), "Password of demouser should not be empty")

	// Retry login with the retrieved password
	loginArgs := append(baseLoginArgs(), "-u", "demouser", "-p", string(password))
	out, err := harness.CLI(loginArgs...)
	Expect(err).ToNot(HaveOccurred(), "Failed to login with password")
	if isLoginSuccessful(out) {
		return AuthUsernamePassword, nil
	}
	return AuthDisabled, errors.New("failed to sign in with user name and password")
}

func loginWithOpenshift(harness *e2e.Harness) (AuthMethod, error) {
	token, err := harness.SH(openshift, "whoami", "-t")
	// If whoami fails just try logging in with the user
	if err != nil {
		return AuthDisabled, err
	}
	// otherwise try logging in with the openshift token but still fallback
	// to regular username/password if that fails
	token = strings.TrimSpace(token)
	Expect(token).ToNot(BeEmpty(), "Token from 'oc whoami' should not be empty")
	loginArgsOcp := append(baseLoginArgs(), "--token", token)
	out, err := harness.CLI(loginArgsOcp...)
	Expect(err).ToNot(HaveOccurred())
	if isLoginSuccessful(out) {
		return AuthToken, nil
	}
	return AuthDisabled, errors.New("failed to sign in with OpenShift token")
}
