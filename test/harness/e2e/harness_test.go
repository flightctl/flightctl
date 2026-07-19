package e2e

import "testing"

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
