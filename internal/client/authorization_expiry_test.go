package client

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubExpiryProvider struct {
	renewCount atomic.Int64
	expiresIn  *int64
	renewErr   error
}

func (s *stubExpiryProvider) Auth() (login.AuthInfo, error)     { return login.AuthInfo{}, nil }
func (s *stubExpiryProvider) Validate(login.ValidateArgs) error { return nil }
func (s *stubExpiryProvider) SetInsecureSkipVerify(bool)        {}

func (s *stubExpiryProvider) Renew(string) (login.AuthInfo, error) {
	n := s.renewCount.Add(1)
	if s.renewErr != nil {
		return login.AuthInfo{}, s.renewErr
	}
	info := login.AuthInfo{
		AccessToken:  fmt.Sprintf("access-%d", n),
		RefreshToken: fmt.Sprintf("refresh-%d", n),
		IdToken:      fmt.Sprintf("id-%d", n),
	}
	if s.expiresIn != nil {
		v := *s.expiresIn
		info.ExpiresIn = &v
	}
	return info, nil
}

func newExpiryRefresher(expiresIn *int64, renewErr error) *AccessTokenRefresher {
	cfg := &Config{
		AuthInfo: AuthInfo{
			AccessToken:       "access-0",
			RefreshToken:      "refresh-0",
			IdToken:           "id-0",
			AccessTokenExpiry: time.Now().Add(-time.Second).Format(time.RFC3339Nano),
		},
	}
	r := NewAccessTokenRefresher(cfg, "", 0)
	r.provider = &stubExpiryProvider{expiresIn: expiresIn, renewErr: renewErr}
	return r
}

func int64Ptr(v int64) *int64 { return &v }

// snapshotAuthInfo captures all credential fields for comparison.
func snapshotAuthInfo(r *AccessTokenRefresher) AuthInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.AuthInfo
}

// ===== Correct expiry behavior =====

func TestRefreshExpiry_WhenExpiresIn3600ItShouldSetFutureExpiry(t *testing.T) {
	r := newExpiryRefresher(int64Ptr(3600), nil)
	require.NoError(t, r.refresh())
	expiryStr := r.accessTokenExpiry()
	require.NotEmpty(t, expiryStr)
	expiry, err := time.Parse(time.RFC3339Nano, expiryStr)
	require.NoError(t, err)
	assert.True(t, expiry.After(time.Now().Add(3500*time.Second)))
	assert.False(t, r.shouldRefresh(expiry))
}

func TestRefreshExpiry_WhenExpiresIn31536000ItShouldSetOneYearExpiry(t *testing.T) {
	r := newExpiryRefresher(int64Ptr(31536000), nil)
	require.NoError(t, r.refresh())
	expiryStr := r.accessTokenExpiry()
	expiry, err := time.Parse(time.RFC3339Nano, expiryStr)
	require.NoError(t, err)
	assert.True(t, expiry.After(time.Now().Add(364*24*time.Hour)))
	assert.False(t, r.shouldRefresh(expiry))
}

func TestRefreshExpiry_WhenLegacy1000YearsItShouldPreserveExactDeadline(t *testing.T) {
	const legacy1000y int64 = 31536000000
	r := newExpiryRefresher(int64Ptr(legacy1000y), nil)
	before := time.Now()
	require.NoError(t, r.refresh())
	expiryStr := r.accessTokenExpiry()
	require.NotEmpty(t, expiryStr)
	expiry, err := time.Parse(time.RFC3339Nano, expiryStr)
	require.NoError(t, err)
	assert.True(t, expiry.Unix() >= before.Unix()+legacy1000y)
	assert.True(t, expiry.Year() > 3000)
	assert.False(t, r.shouldRefresh(expiry))
}

func TestRefreshExpiry_WhenExpiresInZeroItShouldSetExpiryToNow(t *testing.T) {
	r := newExpiryRefresher(int64Ptr(0), nil)
	before := time.Now()
	require.NoError(t, r.refresh())
	after := time.Now()
	expiryStr := r.accessTokenExpiry()
	expiry, err := time.Parse(time.RFC3339Nano, expiryStr)
	require.NoError(t, err)
	assert.True(t, !expiry.Before(before.Add(-time.Second)))
	assert.True(t, !expiry.After(after.Add(time.Second)))
	assert.True(t, r.shouldRefresh(expiry))
}

func TestRefreshExpiry_WhenExpiresInNilItShouldNotUpdateExpiry(t *testing.T) {
	r := newExpiryRefresher(nil, nil)
	originalExpiry := r.accessTokenExpiry()
	require.NoError(t, r.refresh())
	assert.Equal(t, originalExpiry, r.accessTokenExpiry())
}

func TestRefreshExpiry_WhenNegativeItShouldSetPastExpiry(t *testing.T) {
	r := newExpiryRefresher(int64Ptr(-60), nil)
	require.NoError(t, r.refresh())
	expiryStr := r.accessTokenExpiry()
	expiry, err := time.Parse(time.RFC3339Nano, expiryStr)
	require.NoError(t, err)
	assert.True(t, expiry.Before(time.Now()))
	assert.True(t, r.shouldRefresh(expiry))
}

func TestRefreshExpiry_When292YearsMaxSafeItShouldSucceed(t *testing.T) {
	const maxSafe int64 = 9223372036
	r := newExpiryRefresher(int64Ptr(maxSafe), nil)
	require.NoError(t, r.refresh())
	expiryStr := r.accessTokenExpiry()
	expiry, err := time.Parse(time.RFC3339Nano, expiryStr)
	require.NoError(t, err)
	assert.True(t, expiry.Year() > 2300)
	assert.False(t, r.shouldRefresh(expiry))
}

// ===== Error paths =====

func TestRefreshExpiry_WhenInvalidGrantItShouldReturnErrSessionExpired(t *testing.T) {
	r := newExpiryRefresher(int64Ptr(0),
		fmt.Errorf("OAuth2 error: invalid_grant - expired"))
	err := r.Start(context.Background())
	require.ErrorIs(t, err, ErrSessionExpired)
}

func TestRefreshExpiry_WhenServerErrorItShouldNotReturnErrSessionExpired(t *testing.T) {
	r := newExpiryRefresher(int64Ptr(0),
		fmt.Errorf("OAuth2 error: server_error - internal"))
	err := r.Start(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrSessionExpired)
	assert.Contains(t, err.Error(), "server_error")
}

// ===== Overflow: ALL credential fields unchanged, persistence not written =====

func TestRefreshExpiry_WhenOverflowItShouldNotUpdateAnyAuthState(t *testing.T) {
	r := newExpiryRefresher(int64Ptr(math.MaxInt64), nil)

	before := snapshotAuthInfo(r)

	err := r.refresh()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid_grant",
		"Expiry overflow must not be misclassified as session expiry")

	after := snapshotAuthInfo(r)
	assert.Equal(t, before.AccessToken, after.AccessToken)
	assert.Equal(t, before.RefreshToken, after.RefreshToken)
	assert.Equal(t, before.IdToken, after.IdToken)
	assert.Equal(t, before.AccessTokenExpiry, after.AccessTokenExpiry)
	assert.Equal(t, before.TokenToUse, after.TokenToUse)
}

func TestRefreshExpiry_WhenOverflowWithPersistenceItShouldNotWriteFile(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "client.yaml")

	cfg := &Config{
		AuthInfo: AuthInfo{
			AccessToken:       "orig-access",
			RefreshToken:      "orig-refresh",
			IdToken:           "orig-id",
			AccessTokenExpiry: time.Now().Add(-time.Second).Format(time.RFC3339Nano),
		},
	}
	require.NoError(t, cfg.Persist(configPath))
	origBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)

	r := NewAccessTokenRefresher(cfg, configPath, 0)
	r.provider = &stubExpiryProvider{expiresIn: int64Ptr(math.MaxInt64)}

	beforeSnap := snapshotAuthInfo(r)
	err = r.refresh()
	require.Error(t, err)

	// File contents must be byte-identical.
	afterBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, origBytes, afterBytes,
		"Config file contents must not change when expiry computation fails")

	// Entire in-memory AuthInfo unchanged (all fields).
	afterSnap := snapshotAuthInfo(r)
	assert.Equal(t, beforeSnap, afterSnap,
		"Complete AuthInfo must be unchanged after expiry error")
}

// ===== Valid refresh with persistence: control proving the Persist path works =====

func TestRefreshExpiry_WhenValidRefreshWithPersistenceItShouldUpdateFile(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "client.yaml")

	origExpiry := time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	cfg := &Config{
		AuthInfo: AuthInfo{
			AccessToken:       "orig-access",
			RefreshToken:      "orig-refresh",
			IdToken:           "orig-id",
			AccessTokenExpiry: origExpiry,
		},
	}
	require.NoError(t, cfg.Persist(configPath))
	origBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)

	r := NewAccessTokenRefresher(cfg, configPath, 0)
	r.provider = &stubExpiryProvider{expiresIn: int64Ptr(3600)}

	require.NoError(t, r.refresh())

	// File must have changed (new tokens + expiry written).
	afterBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotEqual(t, origBytes, afterBytes,
		"Config file must be updated after a successful refresh")

	// In-memory credentials updated.
	snap := snapshotAuthInfo(r)
	assert.Equal(t, "access-1", snap.AccessToken)
	assert.Equal(t, "refresh-1", snap.RefreshToken)
	assert.Equal(t, "id-1", snap.IdToken)
	assert.NotEqual(t, origExpiry, snap.AccessTokenExpiry,
		"Expiry must be updated to a future value")

	// Verify new expiry is in the future.
	newExpiry, err := time.Parse(time.RFC3339Nano, snap.AccessTokenExpiry)
	require.NoError(t, err)
	assert.True(t, newExpiry.After(time.Now().Add(3500*time.Second)),
		"New expiry should be ~1h in the future")
}
