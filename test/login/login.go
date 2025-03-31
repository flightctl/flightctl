package login

import (
	"encoding/base64"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/gomega"
)

func LoginToAPIWithToken(harness *e2e.Harness) {
	var token string

	// Try retrieving an existing token
	token, err := harness.SH("oc", "whoami", "-t")
	if err == nil {
		token = strings.TrimSpace(token)
	}

	// Build initial login arguments
	loginArgs := []string{"login", "${API_ENDPOINT}", "--insecure-skip-tls-verify"}
	if token != "" {
		loginArgs = append(loginArgs, "--token", token)
	}

	// Attempt login
	out, err := harness.CLI(loginArgs...)
	if err != nil {
		Expect(err).ToNot(HaveOccurred(), "Initial login failed")
	}

	// If login fails due to an invalid or missing token, retry with a password
	if strings.Contains(out, "the token provided is invalid or expired") || strings.Contains(out, "invalid JWT") {
		password, err := harness.SH("oc", "get", "secret/keycloak-demouser-secret", "-n", "flightctl", "-o=jsonpath={.data.password}")
		Expect(err).ToNot(HaveOccurred(), "Failed to retrieve password")

		password = strings.Trim(password, "'") // Clean up single quotes
		decodedBytes, err := base64.StdEncoding.DecodeString(password)
		Expect(err).ToNot(HaveOccurred(), "Failed to decode password")

		// Retry login with username/password
		loginArgs = []string{"login", "${API_ENDPOINT}", "-k", "-u", "demouser", "-p", string(decodedBytes)}
		out, err = harness.CLI(loginArgs...)
		Expect(err).ToNot(HaveOccurred(), "Login with username/password failed") // <-- Added error check
	}

	// Validate final login result
	Expect(err).ToNot(HaveOccurred(), "Login process encountered an error")
	Expect(strings.TrimSpace(out)).To(BeElementOf("Auth is disabled", "Login successful", "Login successful."))

}
