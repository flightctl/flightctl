package client

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
)

func TestRetryTransport_RetryOn429(t *testing.T) {
	require := require.New(t)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, err := w.Write([]byte("Rate limited"))
			require.NoError(err)
		} else {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte("Success"))
			require.NoError(err)
		}
	}))
	defer server.Close()

	testPollCfg := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}
	transport := NewRetryTransport(http.DefaultTransport, log.NewPrefixLogger("test"), testPollCfg)

	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusOK, resp.StatusCode)
	require.Equal(3, attempts)

	body, err := io.ReadAll(resp.Body)
	require.NoError(err)
	require.Equal("Success", string(body))
}

func TestRetryTransport_RetryOn500(t *testing.T) {
	require := require.New(t)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, err := w.Write([]byte("Server error"))
			require.NoError(err)
		} else {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte("Success"))
			require.NoError(err)
		}
	}))
	defer server.Close()

	testPollCfg := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}
	transport := NewRetryTransport(http.DefaultTransport, log.NewPrefixLogger("test"), testPollCfg)

	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusOK, resp.StatusCode)
	require.Equal(2, attempts)
}

func TestRetryTransport_NoRetryOnSuccess(t *testing.T) {
	require := require.New(t)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("Success"))
		require.NoError(err)
	}))
	defer server.Close()

	testPollCfg := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}
	transport := NewRetryTransport(http.DefaultTransport, log.NewPrefixLogger("test"), testPollCfg)

	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusOK, resp.StatusCode)
	require.Equal(1, attempts)
}

func TestRetryTransport_NoRetryOnOtherErrors(t *testing.T) {
	require := require.New(t)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, err := w.Write([]byte("Bad request"))
		require.NoError(err)
	}))
	defer server.Close()

	testPollCfg := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}
	transport := NewRetryTransport(http.DefaultTransport, log.NewPrefixLogger("test"), testPollCfg)

	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusBadRequest, resp.StatusCode)
	require.Equal(1, attempts)
}

func TestRetryTransport_MaxRetriesExceeded(t *testing.T) {
	require := require.New(t)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		_, err := w.Write([]byte("Rate limited"))
		require.NoError(err)
	}))
	defer server.Close()

	testPollCfg := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}
	transport := NewRetryTransport(http.DefaultTransport, log.NewPrefixLogger("test"), testPollCfg)

	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(4, attempts) // Initial + 3 retries (MaxSteps=3)
}

func TestRetryTransport_RequestBodyReuse(t *testing.T) {
	require := require.New(t)

	attempts := 0
	expectedBody := "test request body"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++

		body, err := io.ReadAll(r.Body)
		require.NoError(err)
		require.Equal(expectedBody, string(body))

		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte("Success"))
			require.NoError(err)
		}
	}))
	defer server.Close()

	testPollCfg := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}
	transport := NewRetryTransport(http.DefaultTransport, log.NewPrefixLogger("test"), testPollCfg)

	client := &http.Client{Transport: transport}

	req, err := http.NewRequest("POST", server.URL, bytes.NewBufferString(expectedBody))
	require.NoError(err)

	resp, err := client.Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusOK, resp.StatusCode)
	require.Equal(2, attempts)
}

func TestRetryTransport_RetryAfterHeader(t *testing.T) {
	require := require.New(t)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, err := w.Write([]byte("Rate limited"))
			require.NoError(err)
		} else {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte("Success"))
			require.NoError(err)
		}
	}))
	defer server.Close()

	log := log.NewPrefixLogger("test")
	testPollCfg := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}
	transport := NewRetryTransport(http.DefaultTransport, log, testPollCfg)

	client := &http.Client{Transport: transport}

	start := time.Now()
	resp, err := client.Get(server.URL)
	elapsed := time.Since(start)

	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusOK, resp.StatusCode)
	require.Equal(2, attempts)
	// wait ~100ms (capped by MaxDelay) instead of 1 second
	require.Greater(elapsed, 90*time.Millisecond)
	require.Less(elapsed, 200*time.Millisecond)
}
