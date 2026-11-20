package login

import (
	"fmt"
	"time"
)

// ExpiryFromLifetime computes an RFC3339Nano expiry string from a token
// lifetime in seconds, measured from the current time. It preserves
// sub-second precision and uses checked arithmetic to avoid overflow.
//
// Returns ("", nil) when expiresIn is nil — the caller should preserve
// the existing stored expiry unchanged.
//
// Returns the formatted deadline for all representable lifetimes,
// including zero, negative, and very large positive values such as the
// documented 1000-year legacy token default (31536000000 seconds).
//
// Returns ("", error) when the deadline overflows int64 Unix seconds or
// does not round-trip through RFC3339Nano formatting.
func ExpiryFromLifetime(expiresIn *int64) (string, error) {
	if expiresIn == nil {
		return "", nil
	}
	return expiryFromLifetimeAt(*expiresIn, time.Now())
}

// expiryFromLifetimeAt computes an absolute RFC3339Nano expiry from a
// lifetime in seconds and a base timestamp. It is unexported; tests in
// this package call it directly for deterministic base-time control.
//
// The arithmetic adds the lifetime to the base's Unix seconds directly,
// avoiding time.Duration multiplication that overflows for lifetimes
// exceeding approximately 292 years (math.MaxInt64 nanoseconds).
// Sub-second precision is carried from the base timestamp.
func expiryFromLifetimeAt(expiresInSeconds int64, base time.Time) (string, error) {
	baseUnix := base.Unix()
	deadlineUnix := baseUnix + expiresInSeconds

	// Detect signed int64 addition overflow: a positive lifetime must not
	// decrease the timestamp, and a negative lifetime must not increase it.
	if (expiresInSeconds > 0 && deadlineUnix < baseUnix) ||
		(expiresInSeconds < 0 && deadlineUnix > baseUnix) {
		return "", fmt.Errorf(
			"token lifetime %d seconds overflows int64 Unix seconds (base %d)",
			expiresInSeconds, baseUnix)
	}

	// Construct the deadline preserving sub-second precision from the base.
	// Representability is verified by the round-trip check below rather than
	// by a hard-coded seconds boundary.
	deadline := time.Unix(deadlineUnix, int64(base.Nanosecond()))

	// Format as RFC3339Nano and verify that parsing recovers the same instant.
	// This rejects deadlines whose year exceeds the format's representable
	// range or that lose precision through formatting, without hard-coding
	// an arbitrary year/seconds boundary.
	formatted := deadline.UTC().Format(time.RFC3339Nano)
	roundTrip, err := time.Parse(time.RFC3339Nano, formatted)
	if err != nil {
		return "", fmt.Errorf(
			"token expiry is not representable as RFC3339Nano: %w", err)
	}
	if !roundTrip.Equal(deadline) {
		return "", fmt.Errorf(
			"token expiry does not round-trip through RFC3339Nano (got %v, want %v)",
			roundTrip, deadline)
	}

	return formatted, nil
}
