package webhookdelivery

import (
	"fmt"
	"net/http"
)

// ClassifyHTTPStatus determines whether an HTTP response status code is
// retryable and returns a human-readable description.
func ClassifyHTTPStatus(statusCode int) (retryable bool, message string) {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return false, ""
	case statusCode == http.StatusBadRequest,
		statusCode == http.StatusUnauthorized,
		statusCode == http.StatusForbidden,
		statusCode == http.StatusNotFound:
		return false, fmt.Sprintf("non-retryable HTTP %d", statusCode)
	case statusCode == http.StatusTooManyRequests:
		return true, fmt.Sprintf("retryable HTTP %d", statusCode)
	case statusCode >= 500:
		return true, fmt.Sprintf("retryable HTTP %d", statusCode)
	default:
		return false, fmt.Sprintf("non-retryable HTTP %d", statusCode)
	}
}
