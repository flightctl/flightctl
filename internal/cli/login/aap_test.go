package login

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAAPTokenServer(t *testing.T, responseBody map[string]interface{}, statusCode int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(responseBody)
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestAAPOAuth(serverURL string) *AAPOAuth {
	return &AAPOAuth{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("test-aap-provider")},
		Spec: api.AapProviderSpec{
			ClientId:         "test-client-id",
			AuthorizationUrl: serverURL + "/o/authorize/",
			TokenUrl:         serverURL + "/o/token/",
			Scopes:           []string{"read"},
		},
		ApiServerURL:       serverURL,
		InsecureSkipVerify: true,
	}
}

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// exerciseAuth runs AAPOAuth.Auth() with NoBrowser and a synthetic callback.
//
// Completion model: Auth goroutine sends result on resultCh then closes
// doneCh. The caller consumes resultCh; cleanup waits on doneCh (close).
//
// Bounded abort: on any fixture error BEFORE a valid callback was
// delivered, cleanup sends a missing-state callback to the Auth listener.
// oauth2AuthCodeFlow's handler sends an error on its buffered(1) done
// channel for missing state, causing Auth to receive the error, shut down
// its server, and return. This unblocks the goroutine without production
// API changes or leaked goroutines.
//
// Pipe handling: pipeW close unblocks the scanner goroutine. The Auth
// goroutine is joined via doneCh before os.Stdout is restored, preventing
// data races.
func exerciseAuth(t *testing.T, aap *AAPOAuth) (AuthInfo, error) {
	t.Helper()

	origStdout := os.Stdout
	pipeR, pipeW, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = pipeW

	type authResult struct {
		info AuthInfo
		err  error
	}
	resultCh := make(chan authResult, 1)
	doneCh := make(chan struct{})

	// Scanner goroutine.
	urlCh := make(chan string, 1)
	var scanWg sync.WaitGroup
	scanWg.Add(1)
	go func() {
		defer scanWg.Done()
		scanner := bufio.NewScanner(pipeR)
		for scanner.Scan() {
			line := scanner.Text()
			if idx := strings.Index(line, "http"); idx >= 0 {
				urlCh <- strings.TrimSpace(line[idx:])
				return
			}
		}
		urlCh <- ""
	}()

	// Auth goroutine.
	go func() {
		defer close(doneCh)
		info, authErr := aap.Auth()
		resultCh <- authResult{info, authErr}
	}()

	// Track whether a valid callback was sent. If not, cleanup sends an
	// abort callback to unblock Auth.
	callbackSent := false
	port := aap.CallbackPort

	// Cleanup: always runs. Abort Auth if needed, join everything, restore stdout.
	t.Cleanup(func() {
		if !callbackSent {
			// Send a missing-state callback to trigger oauth2AuthCodeFlow's
			// error path (done <- error), which makes Auth shut down and return.
			abortURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
			abortClient := &http.Client{Timeout: 2 * time.Second}
			_, _ = abortClient.Get(abortURL) //nolint:gosec
		}
		pipeW.Close()
		pipeR.Close()
		scanWg.Wait()
		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			t.Error("Auth goroutine did not exit within 5s after abort — genuine leak")
		}
		os.Stdout = origStdout
	})

	// Wait for URL.
	var loginURL string
	select {
	case loginURL = <-urlCh:
	case <-time.After(5 * time.Second):
		return AuthInfo{}, fmt.Errorf("timeout waiting for Auth to print login URL")
	}
	if loginURL == "" {
		return AuthInfo{}, fmt.Errorf("Auth exited without printing a login URL")
	}

	parsed, err := url.Parse(loginURL)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to parse login URL: %w", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		return AuthInfo{}, fmt.Errorf("state not present in authorization URL")
	}

	// Wait for callback server.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		if ctx.Err() != nil {
			return AuthInfo{}, fmt.Errorf("callback server not ready within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Send synthetic callback.
	cbClient := &http.Client{Timeout: 5 * time.Second}
	cbURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=test-auth-code&state=%s",
		port, url.QueryEscape(state))
	resp, err := cbClient.Get(cbURL) //nolint:gosec
	if err != nil {
		return AuthInfo{}, fmt.Errorf("callback request failed: %w", err)
	}
	resp.Body.Close()
	callbackSent = true

	// Wait for Auth result.
	select {
	case r := <-resultCh:
		return r.info, r.err
	case <-time.After(10 * time.Second):
		return AuthInfo{}, fmt.Errorf("Auth did not complete within 10s after callback")
	}
}

// ===== Auth — seconds-valued lifetime preserved =====

func TestAAPAuth_WhenServerReturns31536000SecondsItShouldPreserveThem(t *testing.T) {
	const input = int64(31536000)
	server := setupAAPTokenServer(t, map[string]interface{}{
		"access_token": "auth-31536000", "refresh_token": "r", "token_type": "bearer",
		"expires_in": float64(input),
	}, http.StatusOK)
	aap := newTestAAPOAuth(server.URL)
	aap.CallbackPort = getFreePort(t)
	aap.Web = true
	aap.NoBrowser = true

	info, err := exerciseAuth(t, aap)
	require.NoError(t, err)
	require.NotNil(t, info.ExpiresIn)
	assert.Equal(t, input, *info.ExpiresIn)
	assert.Equal(t, TokenToUseAccessToken, info.TokenToUse)
}

func TestAAPAuth_WhenServerReturns600SecondsItShouldPreserveThem(t *testing.T) {
	const input = int64(600)
	server := setupAAPTokenServer(t, map[string]interface{}{
		"access_token": "auth-600", "refresh_token": "r", "token_type": "bearer",
		"expires_in": float64(input),
	}, http.StatusOK)
	aap := newTestAAPOAuth(server.URL)
	aap.CallbackPort = getFreePort(t)
	aap.Web = true
	aap.NoBrowser = true

	info, err := exerciseAuth(t, aap)
	require.NoError(t, err)
	require.NotNil(t, info.ExpiresIn)
	assert.Equal(t, input, *info.ExpiresIn)
	assert.Equal(t, TokenToUseAccessToken, info.TokenToUse)
}

// TestAAPAuth_WhenTokenServerReturnsErrorItShouldCompletePromptly verifies
// Auth completes without timeout when the token exchange fails (callback
// delivered but server returns error).
func TestAAPAuth_WhenTokenServerReturnsErrorItShouldCompletePromptly(t *testing.T) {
	server := setupAAPTokenServer(t, map[string]interface{}{
		"error": "server_error", "error_description": "test failure",
	}, http.StatusOK)
	aap := newTestAAPOAuth(server.URL)
	aap.CallbackPort = getFreePort(t)
	aap.Web = true
	aap.NoBrowser = true

	start := time.Now()
	_, err := exerciseAuth(t, aap)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, 5*time.Second,
		"exerciseAuth must complete promptly on Auth error")
}

// TestAAPAuth_WhenFixtureAbortsBeforeCallbackItShouldCleanUpPromptly
// verifies that when the helper aborts (e.g. fixture decides not to send a
// callback), cleanup sends a missing-state abort callback to Auth's
// listener, causing oauth2AuthCodeFlow to receive an error on its done
// channel, shut down its server, and return. The Auth goroutine must exit
// and the port must be released promptly — no 5s timeout, no leaked goroutine.
func TestAAPAuth_WhenFixtureAbortsBeforeCallbackItShouldCleanUpPromptly(t *testing.T) {
	server := setupAAPTokenServer(t, map[string]interface{}{
		"access_token": "should-not-reach", "refresh_token": "r", "token_type": "bearer",
		"expires_in": float64(3600),
	}, http.StatusOK)
	aap := newTestAAPOAuth(server.URL)
	port := getFreePort(t)
	aap.CallbackPort = port
	aap.Web = true
	aap.NoBrowser = true

	origStdout := os.Stdout
	pipeR, pipeW, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = pipeW

	doneCh := make(chan struct{})
	var authErr error

	var scanWg sync.WaitGroup
	scanWg.Add(1)
	go func() {
		defer scanWg.Done()
		scanner := bufio.NewScanner(pipeR)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "http") {
				break
			}
		}
	}()

	go func() {
		defer close(doneCh)
		_, authErr = aap.Auth()
	}()

	// Wait for Auth to start listening.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		require.NoError(t, ctx.Err(), "callback server did not start")
		time.Sleep(10 * time.Millisecond)
	}

	// Intentionally do NOT send a valid callback. Instead send a
	// missing-state abort to unblock Auth.
	abortStart := time.Now()
	abortClient := &http.Client{Timeout: 2 * time.Second}
	resp, abortErr := abortClient.Get(fmt.Sprintf("http://127.0.0.1:%d/callback", port)) //nolint:gosec
	if abortErr == nil {
		resp.Body.Close()
	}

	// Join Auth goroutine — must complete promptly.
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		pipeW.Close()
		pipeR.Close()
		scanWg.Wait()
		os.Stdout = origStdout
		t.Fatal("Auth goroutine did not exit within 5s after abort callback — leak")
		return
	}
	abortElapsed := time.Since(abortStart)

	pipeW.Close()
	pipeR.Close()
	scanWg.Wait()
	os.Stdout = origStdout

	assert.Less(t, abortElapsed, 3*time.Second,
		"Auth must exit promptly after abort callback, not wait for timeout")
	assert.Error(t, authErr, "Auth should return an error from the missing-state callback")
	assert.Contains(t, authErr.Error(), "state",
		"Error should indicate state parameter issue")

	// Verify port is released.
	l, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if listenErr == nil {
		l.Close()
	}
	assert.NoError(t, listenErr, "Callback port should be released after Auth exits")

	t.Logf("Abort cleanup completed in %v, auth error: %v", abortElapsed, authErr)
}

// ===== Renew =====

func TestAAPRenew_WhenServerReturns31536000SecondsItShouldPreserveThem(t *testing.T) {
	const input = int64(31536000)
	server := setupAAPTokenServer(t, map[string]interface{}{
		"access_token": "renew-31536000", "refresh_token": "r", "token_type": "bearer",
		"expires_in": float64(input),
	}, http.StatusOK)
	info, err := newTestAAPOAuth(server.URL).Renew("token")
	require.NoError(t, err)
	require.NotNil(t, info.ExpiresIn)
	assert.Equal(t, input, *info.ExpiresIn)
}

func TestAAPRenew_WhenServerReturns3600SecondsItShouldPreserveThem(t *testing.T) {
	info, err := newTestAAPOAuth(setupAAPTokenServer(t, map[string]interface{}{
		"access_token": "r", "refresh_token": "r", "token_type": "bearer",
		"expires_in": float64(3600),
	}, http.StatusOK).URL).Renew("t")
	require.NoError(t, err)
	assert.Equal(t, int64(3600), *info.ExpiresIn)
}

func TestAAPRenew_WhenServerReturns600SecondsItShouldPreserveThem(t *testing.T) {
	info, err := newTestAAPOAuth(setupAAPTokenServer(t, map[string]interface{}{
		"access_token": "r", "refresh_token": "r", "token_type": "bearer",
		"expires_in": float64(600),
	}, http.StatusOK).URL).Renew("t")
	require.NoError(t, err)
	assert.Equal(t, int64(600), *info.ExpiresIn)
}

func TestAAPRenew_WhenInvalidGrantHTTP200ItShouldContainInvalidGrant(t *testing.T) {
	_, err := newTestAAPOAuth(setupAAPTokenServer(t, map[string]interface{}{
		"error": "invalid_grant", "error_description": "expired",
	}, http.StatusOK).URL).Renew("expired")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
}

func TestAAPRenew_WhenInvalidGrantHTTP400ItShouldContainInvalidGrant(t *testing.T) {
	_, err := newTestAAPOAuth(setupAAPTokenServer(t, map[string]interface{}{
		"error": "invalid_grant", "error_description": "expired",
	}, http.StatusBadRequest).URL).Renew("expired")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
}

func TestAAPRenew_WhenServerErrorItShouldNotContainInvalidGrant(t *testing.T) {
	_, err := newTestAAPOAuth(setupAAPTokenServer(t, map[string]interface{}{
		"error": "server_error", "error_description": "internal",
	}, http.StatusOK).URL).Renew("t")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid_grant")
}

func TestAAPRenew_WhenSuccessItShouldPreserveAccessToken(t *testing.T) {
	info, err := newTestAAPOAuth(setupAAPTokenServer(t, map[string]interface{}{
		"access_token": "renewed-xyz", "refresh_token": "r", "token_type": "bearer",
		"expires_in": float64(3600),
	}, http.StatusOK).URL).Renew("t")
	require.NoError(t, err)
	assert.Equal(t, "renewed-xyz", info.AccessToken)
}
