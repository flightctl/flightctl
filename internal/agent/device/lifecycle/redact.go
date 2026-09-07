package lifecycle

import (
	"regexp"
	"strings"
)

const truncationMarker = "\n[truncated]"

// pemBlockRe matches PEM blocks (certificates, private keys, etc.)
var pemBlockRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)

// bearerRe matches Bearer token values.
var bearerRe = regexp.MustCompile(`(?i)(Bearer\s+)\S+`)

// envVarSecretRe matches lines where a known secret env var name has a value assigned.
var envVarSecretRe = regexp.MustCompile(`(?m)^(TOKEN|SECRET|PASSWORD|API_KEY|AWS_SECRET_ACCESS_KEY|BEARER_TOKEN)=.+$`)

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
