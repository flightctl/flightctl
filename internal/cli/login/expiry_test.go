package login

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpiryFromLifetime_WhenNilItShouldReturnEmptyWithoutError(t *testing.T) {
	result, err := ExpiryFromLifetime(nil)
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestExpiryFromLifetimeAt_WhenZeroItShouldEqualBase(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 123456789, time.UTC)
	result, err := expiryFromLifetimeAt(0, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.True(t, parsed.Equal(base))
	assert.Equal(t, 123456789, parsed.Nanosecond())
}

func TestExpiryFromLifetimeAt_WhenOneHourItShouldAddExactly3600Seconds(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 500000000, time.UTC)
	result, err := expiryFromLifetimeAt(3600, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	expected := time.Date(2026, 10, 27, 13, 0, 0, 500000000, time.UTC)
	assert.True(t, parsed.Equal(expected))
}

func TestExpiryFromLifetimeAt_WhenOneYearItShouldAdd365Days(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	result, err := expiryFromLifetimeAt(31536000, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	expected := time.Date(2027, 10, 27, 12, 0, 0, 0, time.UTC)
	assert.True(t, parsed.Equal(expected))
}

func TestExpiryFromLifetimeAt_WhenLegacy1000YearsItShouldPreserveExactDeadline(t *testing.T) {
	const legacy1000y int64 = 31536000000
	base := time.Date(2026, 1, 1, 0, 0, 0, 999999999, time.UTC)
	result, err := expiryFromLifetimeAt(legacy1000y, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	// Exact epoch arithmetic — no time.Duration.
	expectedUnix := base.Unix() + legacy1000y
	assert.Equal(t, expectedUnix, parsed.Unix(), "Unix seconds must match exactly")
	assert.Equal(t, 999999999, parsed.Nanosecond(), "nanoseconds preserved")
	expectedTime := time.Unix(expectedUnix, int64(base.Nanosecond())).UTC()
	assert.True(t, parsed.Equal(expectedTime), "got %v, want %v", parsed, expectedTime)
}

func TestExpiryFromLifetimeAt_WhenNegativeItShouldProducePastExpiry(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	result, err := expiryFromLifetimeAt(-60, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	expected := time.Date(2026, 10, 27, 11, 59, 0, 0, time.UTC)
	assert.True(t, parsed.Equal(expected))
}

func TestExpiryFromLifetimeAt_WhenSubSecondPrecisionItShouldRoundTrip(t *testing.T) {
	base := time.Date(2026, 6, 15, 8, 30, 45, 123456789, time.UTC)
	result, err := expiryFromLifetimeAt(7200, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	expected := time.Date(2026, 6, 15, 10, 30, 45, 123456789, time.UTC)
	assert.True(t, parsed.Equal(expected))
	assert.Equal(t, 123456789, parsed.Nanosecond())
}

func TestExpiryFromLifetimeAt_WhenNonUTCBaseItShouldProduceUTCOutput(t *testing.T) {
	loc := time.FixedZone("Test+5", 5*3600)
	base := time.Date(2026, 10, 27, 17, 0, 0, 123456789, loc)
	result, err := expiryFromLifetimeAt(3600, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	expected := time.Date(2026, 10, 27, 13, 0, 0, 123456789, time.UTC)
	assert.True(t, parsed.Equal(expected))
	assert.Contains(t, result, "Z")
}

func TestExpiryFromLifetimeAt_When292YearsMaxDurationSafeItShouldSucceed(t *testing.T) {
	const maxDurSec int64 = 9223372036
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := expiryFromLifetimeAt(maxDurSec, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.Equal(t, base.Unix()+maxDurSec, parsed.Unix())
}

// ===== Signed-overflow / underflow =====

func TestExpiryFromLifetimeAt_WhenPositiveOverflowItShouldReturnError(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	_, err := expiryFromLifetimeAt(math.MaxInt64, base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflows", "positive overflow must be detected")
}

func TestExpiryFromLifetimeAt_WhenNegativeOverflowItShouldReturnError(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	_, err := expiryFromLifetimeAt(math.MinInt64, base)
	require.Error(t, err, "MinInt64 lifetime must be rejected")
}

// True signed UNDERFLOW: negative base + MinInt64 lifetime causes the int64
// addition to wrap positive. The overflow check must catch this.
func TestExpiryFromLifetimeAt_WhenNegativeBaseAndMinInt64ItShouldDetectUnderflow(t *testing.T) {
	base := time.Unix(-1000, 0) // negative Unix timestamp
	_, err := expiryFromLifetimeAt(math.MinInt64, base)
	require.Error(t, err, "negative base + MinInt64 must be detected as underflow")
	assert.Contains(t, err.Error(), "overflows",
		"underflow should be caught by the signed-overflow check")
}

// True signed OVERFLOW: large positive base + MaxInt64 lifetime wraps negative.
func TestExpiryFromLifetimeAt_WhenLargeBaseAndMaxInt64ItShouldDetectOverflow(t *testing.T) {
	base := time.Unix(1000, 0) // small positive Unix timestamp
	_, err := expiryFromLifetimeAt(math.MaxInt64, base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflows")
}

// ===== RFC3339Nano representability boundaries =====
// Determined empirically: Go time.Parse(time.RFC3339Nano, ...) round-trips
// for years 0–9999. Year 10000 fails parse (5+ digit year). Year -1 fails.

// True lower accepted instant: year 0, first moment, with zero nanoseconds.
func TestExpiryFromLifetimeAt_WhenResultIsYear0FirstInstantZeroNanosItShouldSucceed(t *testing.T) {
	target := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	targetUnix := target.Unix()
	base := time.Unix(0, 0)
	lifetime := targetUnix - base.Unix()
	result, err := expiryFromLifetimeAt(lifetime, base)
	require.NoError(t, err, "year 0 first instant (zero ns) must be representable")
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.True(t, parsed.Equal(target))
	assert.Equal(t, 0, parsed.Year())
	assert.Equal(t, 0, parsed.Nanosecond())
}

// Year 0 first instant with nonzero nanoseconds.
func TestExpiryFromLifetimeAt_WhenResultIsYear0FirstInstantWithNanosItShouldSucceed(t *testing.T) {
	target := time.Date(0, 1, 1, 0, 0, 0, 123456789, time.UTC)
	targetUnix := target.Unix()
	base := time.Unix(0, 123456789)
	lifetime := targetUnix - base.Unix()
	result, err := expiryFromLifetimeAt(lifetime, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.True(t, parsed.Equal(target))
	assert.Equal(t, 0, parsed.Year())
	assert.Equal(t, 123456789, parsed.Nanosecond())
}

// Just before year 0: one second before 0000-01-01T00:00:00 must be rejected
// (negative year not representable in RFC3339).
func TestExpiryFromLifetimeAt_WhenResultIsJustBeforeYear0ItShouldFail(t *testing.T) {
	justBefore := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).Unix() - 1
	base := time.Unix(0, 0)
	lifetime := justBefore - base.Unix()
	_, err := expiryFromLifetimeAt(lifetime, base)
	require.Error(t, err, "one second before year 0 must be rejected")
	t.Logf("just-before-year-0 rejection: %s", err)
}

// Last second of year 0 (previously mislabeled as lower boundary).
func TestExpiryFromLifetimeAt_WhenResultIsYear0LastSecondItShouldSucceed(t *testing.T) {
	const targetUnix int64 = -62135596801 // 0000-12-31T23:59:59
	base := time.Unix(0, 999999999)
	lifetime := targetUnix - base.Unix()
	result, err := expiryFromLifetimeAt(lifetime, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.Equal(t, targetUnix, parsed.Unix())
	assert.Equal(t, 999999999, parsed.Nanosecond())
}

// Upper accepted: year 9999, last second with full nanoseconds.
func TestExpiryFromLifetimeAt_WhenResultIsYear9999LastSecondItShouldSucceed(t *testing.T) {
	const targetUnix int64 = 253402300799 // 9999-12-31T23:59:59
	base := time.Unix(0, 987654321)
	lifetime := targetUnix - base.Unix()
	result, err := expiryFromLifetimeAt(lifetime, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.Equal(t, targetUnix, parsed.Unix())
	assert.Equal(t, 987654321, parsed.Nanosecond())
	assert.Equal(t, 9999, parsed.Year())
}

// Just-past upper: year 10000 must be rejected.
func TestExpiryFromLifetimeAt_WhenResultIsYear10000ItShouldFail(t *testing.T) {
	const targetUnix int64 = 253402300800 // 10000-01-01T00:00:00
	base := time.Unix(0, 0)
	lifetime := targetUnix - base.Unix()
	_, err := expiryFromLifetimeAt(lifetime, base)
	require.Error(t, err, "year 10000 must be rejected")
	assert.Contains(t, err.Error(), "not representable")
}

// Non-UTC base near upper bound: same instant, different zone.
func TestExpiryFromLifetimeAt_WhenNonUTCBaseNearUpperBoundItShouldSucceed(t *testing.T) {
	const targetUnix int64 = 253402300799
	loc := time.FixedZone("IST", 5*3600+30*60)
	base := time.Date(1970, 1, 1, 5, 30, 0, 555000000, loc) // = epoch UTC
	lifetime := targetUnix - base.Unix()
	result, err := expiryFromLifetimeAt(lifetime, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.Equal(t, targetUnix, parsed.Unix())
	assert.Equal(t, 555000000, parsed.Nanosecond())
	assert.Contains(t, result, "Z", "output must be UTC")
}

func TestExpiryFromLifetimeAt_WhenResultIsYear1FirstSecondItShouldSucceed(t *testing.T) {
	const targetUnix int64 = -62135596800 // 0001-01-01T00:00:00
	base := time.Unix(0, 0)
	lifetime := targetUnix - base.Unix()
	result, err := expiryFromLifetimeAt(lifetime, base)
	require.NoError(t, err)
	parsed, err := time.Parse(time.RFC3339Nano, result)
	require.NoError(t, err)
	assert.Equal(t, 1, parsed.Year())
}
