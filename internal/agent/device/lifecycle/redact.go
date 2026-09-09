package lifecycle

import (
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/hook"
)

const truncationMarker = "\n[truncated]"

// pemBlockRe matches PEM blocks (certificates, private keys, etc.)
var pemBlockRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)

// bearerRe matches Bearer token values.
var bearerRe = regexp.MustCompile(`(?i)(Bearer\s+)\S+`)

// envVarSecretRe matches lines where a secret env var name (exact or common suffix) has a value assigned.
var envVarSecretRe = regexp.MustCompile(`(?m)^(?:[A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|BEARER_TOKEN)|AWS_SECRET_ACCESS_KEY)=.+$`)

// redactSecrets replaces known secret patterns in hook output.
func redactSecrets(output string) string {
	// Redact PEM blocks
	result := pemBlockRe.ReplaceAllString(output, "[REDACTED PEM BLOCK]")

	// Redact Bearer tokens
	result = bearerRe.ReplaceAllString(result, "${1}[REDACTED]")

	// Redact known secret env var values
	result = envVarSecretRe.ReplaceAllStringFunc(result, func(line string) string {
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			return line[:idx+1] + "[REDACTED]"
		}
		return line
	})

	return result
}

// sanitizePreEnrollmentActions redacts secrets and enforces a shared output budget
// across all pre-enrollment action results (first actions retain priority).
func sanitizePreEnrollmentActions(actions []hook.EnrollmentActionResult, maxBytes int) []hook.EnrollmentActionResult {
	if len(actions) == 0 {
		return nil
	}
	sanitized := make([]hook.EnrollmentActionResult, len(actions))
	remaining := maxBytes
	for i, action := range actions {
		sanitized[i].Source = action.Source
		sanitized[i].ExitCode = action.ExitCode
		if action.Output == "" || remaining <= 0 {
			continue
		}
		out := redactSecrets(action.Output)
		if len(out) > remaining {
			out = truncateOutput(out, remaining)
			remaining = 0
		} else {
			remaining -= len(out)
		}
		sanitized[i].Output = out
	}
	return sanitized
}

// truncateOutput keeps the last maxBytes of output, appending a truncation
// marker at the end when the raw output exceeds the limit.
func truncateOutput(output string, maxBytes int) string {
	if len(output) <= maxBytes {
		return output
	}
	// Keep the last (maxBytes - marker length) bytes, then append marker
	keep := maxBytes - len(truncationMarker)
	if keep < 0 {
		keep = 0
	}
	return output[len(output)-keep:] + truncationMarker
}
