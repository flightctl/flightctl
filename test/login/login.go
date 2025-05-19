package login

import (
	"encoding/base64"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/gomega"
)

func LoginToAPIWithToken(harness *e2e.Harness) {

	// Login Arguments and token
	var token string
	loginArgs := []string{"login", "${API_ENDPOINT}", "--insecure-skip-tls-verify"}
	if token != "" {
		loginArgs = append(loginArgs, "--token", token)
	}
	// login
	out, err := harness.CLI(loginArgs...)
	if strings.Contains(out, "You must provide one of the following options to log in") {
		token, err = harness.SH("oc", "whoami", "-t")
		token = strings.TrimSpace(token)
		// Validate Token Retrieval
		Expect(err).ToNot(HaveOccurred(), "Failed to retrieve token")
		Expect(token).ToNot(BeEmpty(), "Token from 'oc whoami' should not be empty")

		// Retry login with the retrieved token
		loginArgsOcp := append(loginArgs, "--token", token)
		out, err = harness.CLI(loginArgsOcp...)
	}
	// Case standalone
	if strings.Contains(out, "the token provided is invalid or expired") || strings.Contains(out, "invalid JWT") {
		password, e := harness.SH("oc", "get", "secret/keycloak-demouser-secret", "-n", "flightctl", "-o=jsonpath='{.data.password}'")
		Expect(e).ToNot(HaveOccurred(), "Failed to retrieve password")
		Expect(password).ToNot(BeEmpty(), "Password of demouser should not be empty")

		// Decode password from base64
		password = strings.ReplaceAll(password, "'", "")
		decodedBytes, e := base64.StdEncoding.DecodeString(password)
		Expect(e).ToNot(HaveOccurred(), "Failed to convert password")

		// Convert the decoded bytes to a string
		password = string(decodedBytes)

		// Retry login with the retrieved password
		loginArgs = append(loginArgs, "-k", "-u", "demouser", "-p", password)
		out, err = harness.CLI(loginArgs...)
	}
	// Validate Login
	Expect(err).ToNot(HaveOccurred())
	Expect(strings.TrimSpace(out)).To(BeElementOf("Auth is disabled", "Login successful", "Login successful."))
}
