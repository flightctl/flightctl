package webhookdelivery

import (
	"math"
	"time"
)

// ComputeBackoff calculates exponential backoff with a cap.
func ComputeBackoff(attempt int, baseDelay, maxBackoff time.Duration) time.Duration {
	backoff := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt-1)))
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}
