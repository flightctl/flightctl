package client

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/stretchr/testify/require"
)

type stubAuthProvider struct {
	renewCount atomic.Int64
}

func (s *stubAuthProvider) Auth() (login.AuthInfo, error) {
	return login.AuthInfo{}, nil
}

func (s *stubAuthProvider) Validate(login.ValidateArgs) error {
	return nil
}

func (s *stubAuthProvider) SetInsecureSkipVerify(bool) {}

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

func TestAccessTokenRefresherWhenRefreshSucceedsItShouldExposeTheNewAccessToken(t *testing.T) {
	require := require.New(t)
	r := newTestAccessTokenRefresher(t, "")

	require.Equal("access-0", r.GetAccessToken())
	require.NoError(r.refresh())
	require.Equal("access-1", r.GetAccessToken())
}

func TestAccessTokenRefresherWhenTokenToUseIsIdItShouldReturnTheIdToken(t *testing.T) {
	require := require.New(t)
	r := newTestAccessTokenRefresher(t, "")
	r.config.AuthInfo.TokenToUse = TokenToUseIdToken

	require.Equal("id-0", r.GetAccessToken())
	require.NoError(r.refresh())
	require.Equal("id-1", r.GetAccessToken())
}

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
