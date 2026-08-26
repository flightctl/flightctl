package client

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/stretchr/testify/require"
)

// TestAccessTokenRefresherWhenRefreshSucceedsItShouldExposeTheNewAccessToken
// verifies that a successful refresh updates the token returned by GetAccessToken.
func TestAccessTokenRefresherWhenRefreshSucceedsItShouldExposeTheNewAccessToken(t *testing.T) {
	require := require.New(t)
	r := newTestAccessTokenRefresher(t, "")

	require.Equal("access-0", r.GetAccessToken())
	require.NoError(r.refresh())
	require.Equal("access-1", r.GetAccessToken())
}

// TestAccessTokenRefresherWhenTokenToUseIsIdItShouldReturnTheIdToken
// verifies that GetAccessToken returns the ID token when TokenToUse is set to id.
func TestAccessTokenRefresherWhenTokenToUseIsIdItShouldReturnTheIdToken(t *testing.T) {
	require := require.New(t)
	r := newTestAccessTokenRefresher(t, "")
	r.config.AuthInfo.TokenToUse = TokenToUseIdToken

	require.Equal("id-0", r.GetAccessToken())
	require.NoError(r.refresh())
	require.Equal("id-1", r.GetAccessToken())
}

// TestAccessTokenRefresherWhenRefreshAndGetAccessTokenRunConcurrentlyItShouldNotRace
// verifies that concurrent refresh and GetAccessToken calls do not race on shared config.
func TestAccessTokenRefresherWhenRefreshAndGetAccessTokenRunConcurrentlyItShouldNotRace(t *testing.T) {
	require := require.New(t)
	r := newTestAccessTokenRefresher(t, filepath.Join(t.TempDir(), "client.yaml"))

	const readers = 8
	const writers = 4
	const iterations = 50

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				require.NotEmpty(r.GetAccessToken())
			}
		}()
	}
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				require.NoError(r.refresh())
			}
		}()
	}

	close(start)
	wg.Wait()
	require.NotEmpty(r.GetAccessToken())
}

// TestAccessTokenRefresherWhenStopRunsBeforeStartItShouldNotLaunchRefreshLoop
// verifies that Stop before Start prevents the background refresh loop from running.
func TestAccessTokenRefresherWhenStopRunsBeforeStartItShouldNotLaunchRefreshLoop(t *testing.T) {
	require := require.New(t)
	r := newTestAccessTokenRefresher(t, "")
	provider := r.provider.(*stubAuthProvider)
	setSoonExpiry(r)

	r.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		r.Start(ctx)
		close(started)
	}()
	waitClosed(t, started, time.Second)

	idle := make(chan struct{})
	go func() {
		time.Sleep(2 * time.Second)
		close(idle)
	}()
	waitClosed(t, idle, 3*time.Second)

	require.Equal(int64(0), provider.renewCount.Load())
}

// TestAccessTokenRefresherWhenStartAndStopRunConcurrentlyItShouldNotLeaveAnActiveRefreshLoop
// verifies that concurrent Start and Stop leave no running refresh loop.
func TestAccessTokenRefresherWhenStartAndStopRunConcurrentlyItShouldNotLeaveAnActiveRefreshLoop(t *testing.T) {
	require := require.New(t)
	r := newTestAccessTokenRefresher(t, "")
	provider := r.provider.(*stubAuthProvider)
	setSoonExpiry(r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.Start(ctx)
	}()
	go func() {
		defer wg.Done()
		r.Stop()
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	waitClosed(t, done, time.Second)

	afterStop := provider.renewCount.Load()
	idle := make(chan struct{})
	go func() {
		time.Sleep(2 * time.Second)
		close(idle)
	}()
	waitClosed(t, idle, 3*time.Second)

	require.Equal(afterStop, provider.renewCount.Load())
}

// stubAuthProvider is a login.AuthProvider that returns incrementing tokens on Renew.
type stubAuthProvider struct {
	renewCount atomic.Int64
}

// Auth implements login.AuthProvider and is unused in these tests.
func (s *stubAuthProvider) Auth() (login.AuthInfo, error) {
	return login.AuthInfo{}, nil
}

// Validate implements login.AuthProvider and is unused in these tests.
func (s *stubAuthProvider) Validate(login.ValidateArgs) error {
	return nil
}

// SetInsecureSkipVerify implements login.AuthProvider and is unused in these tests.
func (s *stubAuthProvider) SetInsecureSkipVerify(bool) {}

// Renew returns a new access, refresh, and ID token on each call.
func (s *stubAuthProvider) Renew(string) (login.AuthInfo, error) {
	n := s.renewCount.Add(1)
	expiresIn := int64(3600)
	return login.AuthInfo{
		AccessToken:  fmt.Sprintf("access-%d", n),
		RefreshToken: fmt.Sprintf("refresh-%d", n),
		IdToken:      fmt.Sprintf("id-%d", n),
		ExpiresIn:    &expiresIn,
	}, nil
}

// setSoonExpiry sets access-token expiry so Start skips an immediate refresh
// but a running refresh loop would tick after about one second.
func setSoonExpiry(r *AccessTokenRefresher) {
	r.config.AuthInfo.AccessTokenExpiry = time.Now().Add(6 * time.Second).Format(time.RFC3339Nano)
}

// waitClosed waits until ch is closed or fails the test at timeout.
func waitClosed(t *testing.T, ch <-chan struct{}, timeout time.Duration) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for test condition")
	}
}

// newTestAccessTokenRefresher returns a refresher with a stub auth provider
// and pre-populated tokens for unit tests.
func newTestAccessTokenRefresher(t *testing.T, configFilePath string) *AccessTokenRefresher {
	t.Helper()
	cfg := &Config{
		AuthInfo: AuthInfo{
			AccessToken:       "access-0",
			RefreshToken:      "refresh-0",
			IdToken:           "id-0",
			AccessTokenExpiry: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
		},
	}
	r := NewAccessTokenRefresher(cfg, configFilePath, 0)
	r.provider = &stubAuthProvider{}
	return r
}
