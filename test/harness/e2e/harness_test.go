package e2e

import (
	"slices"
	"strings"
	"testing"
)

const (
	redactionTokenValue     = "value-for-token-flag"
	redactionPasswordValue  = "value-for-p-flag"
	redactionClientKeyValue = "value-for-client-key-flag"
)

// TestIsCLIRateLimitOutputDetectsLoginRateLimit verifies auth rate-limit output is retried.
func TestIsCLIRateLimitOutputDetectsLoginRateLimit(t *testing.T) {
	if !isCLIRateLimitOutput(cliLoginRateLimitExceededOutput) {
		t.Fatalf("expected login rate-limit output to be classified as rate-limited")
	}
}

// TestIsCLIRateLimitOutputPreservesExisting429Checks verifies existing 429 retry detection.
func TestIsCLIRateLimitOutputPreservesExisting429Checks(t *testing.T) {
	cases := []string{
		cliRateLimitResponseStatus,
		cliRateLimitServerReturned,
	}

	for _, output := range cases {
		if !isCLIRateLimitOutput(output) {
			t.Fatalf("expected %q to be classified as rate-limited", output)
		}
	}
}

// TestRedactCommandArgsRemovesSensitiveValues verifies CLI logs do not expose credentials.
func TestRedactCommandArgsRemovesSensitiveValues(t *testing.T) {
	args := []string{
		"flightctl",
		"login",
		"--token",
		redactionTokenValue,
		"-p",
		redactionPasswordValue,
		"--client-key=" + redactionClientKeyValue,
		"get",
		"devices",
	}

	redacted := redactCommandArgs(args)
	redactedLog := strings.Join(redacted, " ")
	for _, value := range []string{redactionTokenValue, redactionPasswordValue, redactionClientKeyValue} {
		if strings.Contains(redactedLog, value) {
			t.Fatalf("expected %q to be redacted from %v", value, redacted)
		}
	}
	if !slices.Contains(redacted, "devices") {
		t.Fatalf("expected non-sensitive args to be preserved in %v", redacted)
	}
}
