package client

import (
	"context"
	"fmt"
	"os"
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

// TestAccessTokenRefresherWhenPersistFailsItShouldWrapTheError
// verifies that a config persist failure is returned with operation context.
func TestAccessTokenRefresherWhenPersistFailsItShouldWrapTheError(t *testing.T) {
	require := require.New(t)
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(os.WriteFile(parent, []byte("x"), 0o600))
	r := newTestAccessTokenRefresher(t, filepath.Join(parent, "client.yaml"))

	err := r.refresh()
	require.Error(err)
	require.ErrorContains(err, "failed to persist config")
	require.ErrorContains(err, "writing config")
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
	// Expiry is already due so Start would refresh immediately unless Stop is honored first.
	r.config.AuthInfo.AccessTokenExpiry = time.Now().Add(-time.Second).Format(time.RFC3339Nano)

	r.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		r.Start(ctx)
		close(started)
	}()
	waitClosed(t, started, time.Second)

	waitForLoopActionOrDeadline(t, provider.loopAction, 2*time.Second)

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
	waitForLoopActionOrDeadline(t, provider.loopAction, 2*time.Second)

	require.Equal(afterStop, provider.renewCount.Load())
}

// stubAuthProvider is a login.AuthProvider that returns incrementing tokens on Renew.
type stubAuthProvider struct {
	renewCount atomic.Int64
	loopAction chan struct{}
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
	s.notifyLoopAction()
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

// notifyLoopAction signals that the refresh loop took an action. The send is
// non-blocking so Renew during a Start/Stop race is dropped unless a test is
// already waiting.
func (s *stubAuthProvider) notifyLoopAction() {
	if s.loopAction == nil {
		return
	}
	select {
	case s.loopAction <- struct{}{}:
	default:
	}
}

// waitForLoopActionOrDeadline waits until the stub signals a refresh-loop
// action or the deadline elapses. Returning on the signal lets callers assert
// immediately instead of sleeping, while still observing a delayed faulty refresh.
func waitForLoopActionOrDeadline(t *testing.T, action <-chan struct{}, deadline time.Duration) {
	t.Helper()
	select {
	case <-action:
	case <-time.After(deadline):
	}
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
	r.provider = &stubAuthProvider{loopAction: make(chan struct{})}
	return r
}
