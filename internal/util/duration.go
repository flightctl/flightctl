package util

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Common duration constants for convenience
const (
	Day  = 24 * time.Hour
	Week = 7 * Day
)

// ExtendedParseDuration parses a duration string that supports additional units:
// "d" for days, "w" for weeks
func ExtendedParseDuration(s string) (time.Duration, error) {
	// First try standard Go parsing
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	return parseExtendedDuration(s)
}

// parseExtendedDuration parses a duration string with extended units
func parseExtendedDuration(s string) (time.Duration, error) {
	var total time.Duration
	remaining := s

	// Parse extended units first (days and weeks)
	for _, unit := range []struct {
		suffix     string
		multiplier time.Duration
	}{
		{"w", Week},
		{"d", Day},
	} {
		if duration, newRemaining, err := extractUnit(remaining, unit.suffix, unit.multiplier); err != nil {
			return 0, err
		} else {
			total += duration
			remaining = newRemaining
		}
	}

	// Parse any remaining standard Go units
	if remaining != "" {
		if standardDuration, err := time.ParseDuration(remaining); err != nil {
			return 0, err
		} else {
			total += standardDuration
		}
	}

	return total, nil
}

// extractUnit extracts and converts a specific unit from the duration string
func extractUnit(s, unit string, multiplier time.Duration) (time.Duration, string, error) {
	var total time.Duration
	remaining := s

	// Find all occurrences of the unit
	for {
		idx := strings.Index(remaining, unit)
		if idx == -1 {
			break
		}

		// Extract the number before the unit
		numStart := idx
		for numStart > 0 && isDigit(remaining[numStart-1]) {
			numStart--
		}

		if numStart == idx {
			return 0, "", fmt.Errorf("invalid duration format: missing number before '%s'", unit)
		}

		numStr := remaining[numStart:idx]
		num, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("invalid number '%s' before unit '%s': %w", numStr, unit, err)
		}

		// Check for duration overflow
		if num > int64(math.MaxInt64)/int64(multiplier) {
			return 0, "", fmt.Errorf("duration overflow: %d%s is too large", num, unit)
		}

		total += time.Duration(num) * multiplier

		// Remove this occurrence from the string
		remaining = remaining[:numStart] + remaining[idx+len(unit):]
	}

	return total, remaining, nil
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
