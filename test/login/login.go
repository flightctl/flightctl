package login

import (
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
	if strings.Contains(out, "You must obtain an API token by visiting") {
		token, err = harness.SH("oc", "whoami", "-t")
		token = strings.TrimSpace(token)
		// Validate Token Retrieval
		Expect(err).ToNot(HaveOccurred(), "Failed to retrieve token")
		Expect(token).ToNot(BeEmpty(), "Token from 'oc whoami' should not be empty")

		// Retry login with the retrieved token
		loginArgs = append(loginArgs, "--token", token)
		out, err = harness.CLI(loginArgs...)
	}
	// Validate Login
	Expect(err).ToNot(HaveOccurred())
	Expect(strings.TrimSpace(out)).To(BeElementOf("Auth is disabled", "Login successful"))
}
