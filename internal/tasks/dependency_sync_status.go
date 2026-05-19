package tasks

import (
	"regexp"
)

var credentialPattern = regexp.MustCompile(`(?i)(password|token|secret|bearer|authorization)[=:\s]+\S+`)

// sanitizeError strips credential-like patterns from error messages to
// prevent leaking secrets into events or status fields.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	return credentialPattern.ReplaceAllString(msg, "$1=[REDACTED]")
}
