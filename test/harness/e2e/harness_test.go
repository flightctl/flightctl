package e2e

import (
	"errors"
	"io"
	"net/url"
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

// TestIsRetryableAPIWriteErrorRequiresTransportError verifies API response errors are not
// retried just because their formatted status message contains transport-looking text.
func TestIsRetryableAPIWriteErrorRequiresTransportError(t *testing.T) {
	apiStatusErr := errors.New("unexpected status creating/updating fleet test: 400: EOF in validation message")
	if isRetryableAPIWriteError(apiStatusErr) {
		t.Fatalf("expected formatted API status error not to be classified as retryable")
	}
}

// TestIsRetryableAPIWriteErrorAllowsTransportErrors verifies marked and typed transport
// failures are still retried.
func TestIsRetryableAPIWriteErrorAllowsTransportErrors(t *testing.T) {
	cases := []error{
		apiWriteTransportError{operation: "replace fleet test", err: io.EOF},
		&url.Error{Op: "Put", URL: "https://flightctl.example/api/v1/fleets/test", Err: io.ErrUnexpectedEOF},
		apiWriteTransportError{operation: "replace fleet test", err: errors.New("server closed idle connection")},
	}

	for _, err := range cases {
		if !isRetryableAPIWriteError(err) {
			t.Fatalf("expected %q to be classified as retryable", err)
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
