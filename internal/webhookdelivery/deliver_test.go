package webhookdelivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		wantRetry   bool
		wantMessage string
	}{
		{name: "When 200 OK it should be success", statusCode: 200, wantRetry: false, wantMessage: ""},
		{name: "When 204 No Content it should be success", statusCode: 204, wantRetry: false, wantMessage: ""},
		{name: "When 400 Bad Request it should be non-retryable", statusCode: 400, wantRetry: false, wantMessage: "non-retryable HTTP 400"},
		{name: "When 401 Unauthorized it should be non-retryable", statusCode: 401, wantRetry: false, wantMessage: "non-retryable HTTP 401"},
		{name: "When 403 Forbidden it should be non-retryable", statusCode: 403, wantRetry: false, wantMessage: "non-retryable HTTP 403"},
		{name: "When 404 Not Found it should be non-retryable", statusCode: 404, wantRetry: false, wantMessage: "non-retryable HTTP 404"},
		{name: "When 429 Too Many Requests it should be retryable", statusCode: 429, wantRetry: true, wantMessage: "retryable HTTP 429"},
		{name: "When 500 Internal Server Error it should be retryable", statusCode: 500, wantRetry: true, wantMessage: "retryable HTTP 500"},
		{name: "When 502 Bad Gateway it should be retryable", statusCode: 502, wantRetry: true, wantMessage: "retryable HTTP 502"},
		{name: "When 503 Service Unavailable it should be retryable", statusCode: 503, wantRetry: true, wantMessage: "retryable HTTP 503"},
		{name: "When 408 Request Timeout it should be non-retryable", statusCode: 408, wantRetry: false, wantMessage: "non-retryable HTTP 408"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retryable, message := ClassifyHTTPStatus(tt.statusCode)
			assert.Equal(t, tt.wantRetry, retryable)
			assert.Equal(t, tt.wantMessage, message)
		})
	}
}

func TestConfigWithDefaults(t *testing.T) {
	t.Run("When all zero it should apply defaults", func(t *testing.T) {
		cfg := Config{URL: "https://example.com"}.WithDefaults()
		assert.Equal(t, DefaultMaxAttempts, cfg.MaxAttempts)
		assert.Equal(t, DefaultDeadline, cfg.Deadline)
		assert.Equal(t, DefaultMaxBackoff, cfg.MaxBackoff)
		assert.Equal(t, DefaultBackoffDelay, cfg.BackoffDelay)
		assert.Equal(t, DefaultTimeout, cfg.Timeout)
	})

	t.Run("When values are set it should keep them", func(t *testing.T) {
		cfg := Config{
			URL:          "https://example.com",
			MaxAttempts:  3,
			Deadline:     5 * time.Minute,
			MaxBackoff:   1 * time.Minute,
			BackoffDelay: 1 * time.Second,
			Timeout:      45 * time.Second,
		}.WithDefaults()
		assert.Equal(t, 3, cfg.MaxAttempts)
		assert.Equal(t, 5*time.Minute, cfg.Deadline)
		assert.Equal(t, 1*time.Minute, cfg.MaxBackoff)
		assert.Equal(t, 1*time.Second, cfg.BackoffDelay)
		assert.Equal(t, 45*time.Second, cfg.Timeout)
	})

	t.Run("When durations are non-positive it should keep defaults", func(t *testing.T) {
		cfg := Config{
			URL:          "https://example.com",
			Timeout:      0,
			Deadline:     -1 * time.Second,
			MaxBackoff:   0,
			BackoffDelay: -5 * time.Millisecond,
		}.WithDefaults()
		assert.Equal(t, DefaultDeadline, cfg.Deadline)
		assert.Equal(t, DefaultMaxBackoff, cfg.MaxBackoff)
		assert.Equal(t, DefaultBackoffDelay, cfg.BackoffDelay)
		assert.Equal(t, DefaultTimeout, cfg.Timeout)
	})
}

func TestDeliver_HTTPSOnly(t *testing.T) {
	log := logrus.NewEntry(logrus.New())

	t.Run("When URL is HTTP it should reject as non-retryable", func(t *testing.T) {
		err := Deliver(context.Background(), NewClient(), Delivery{
			Config:     Config{URL: "http://example.com/hook"},
			DeliveryID: "test/0",
		}, log)
		require.Error(t, err)
		assert.True(t, IsNonRetryable(err))
		assert.Contains(t, err.Error(), "insecure or invalid URL rejected")
	})

	t.Run("When URL has no scheme it should reject as non-retryable", func(t *testing.T) {
		err := Deliver(context.Background(), NewClient(), Delivery{
			Config:     Config{URL: "example.com/hook"},
			DeliveryID: "test/0",
		}, log)
		require.Error(t, err)
		assert.True(t, IsNonRetryable(err))
		assert.Contains(t, err.Error(), "insecure or invalid URL rejected")
	})

	t.Run("When URL has https scheme but no host it should reject as non-retryable", func(t *testing.T) {
		err := Deliver(context.Background(), NewClient(), Delivery{
			Config:     Config{URL: "https:hook"},
			DeliveryID: "test/0",
		}, log)
		require.Error(t, err)
		assert.True(t, IsNonRetryable(err))
		assert.Contains(t, err.Error(), "insecure or invalid URL rejected")
	})
}

func TestDeliver_WithTLSServer(t *testing.T) {
	log := logrus.NewEntry(logrus.New())

	t.Run("When server returns 200 it should succeed", func(t *testing.T) {
		var receivedHeaders http.Header
		var receivedBody string
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHeaders = r.Header
			body := make([]byte, 1024)
			n, _ := r.Body.Read(body)
			receivedBody = string(body[:n])
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		err := Deliver(context.Background(), server.Client(), Delivery{
			Config: Config{
				URL:         server.URL,
				Timeout:     5 * time.Second,
				MaxAttempts: 1,
			},
			BearerToken: "test-token",
			Payload:     []byte(`{"test":true}`),
			DeliveryID:  "dev1/0",
		}, log)
		require.NoError(t, err)

		assert.Equal(t, "dev1/0", receivedHeaders.Get("X-Flightctl-Delivery-Id"))
		assert.Equal(t, "1", receivedHeaders.Get("X-Flightctl-Delivery-Attempt"))
		assert.Equal(t, "Bearer test-token", receivedHeaders.Get("Authorization"))
		assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
		assert.Equal(t, `{"test":true}`, receivedBody)
	})

	t.Run("When server returns 400 it should fail without retry", func(t *testing.T) {
		var callCount atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		err := Deliver(context.Background(), server.Client(), Delivery{
			Config: Config{
				URL:         server.URL,
				Timeout:     5 * time.Second,
				MaxAttempts: 3,
			},
			DeliveryID: "dev1/0",
		}, log)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-retryable HTTP 400")
		assert.Equal(t, int32(1), callCount.Load(), "should not retry on 400")
	})

	t.Run("When server returns 500 then 200 it should retry and succeed", func(t *testing.T) {
		var callCount atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := callCount.Add(1)
			if count == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		err := Deliver(context.Background(), server.Client(), Delivery{
			Config: Config{
				URL:          server.URL,
				Timeout:      5 * time.Second,
				MaxAttempts:  3,
				BackoffDelay: 10 * time.Millisecond,
				Deadline:     30 * time.Second,
			},
			DeliveryID: "dev1/0",
		}, log)
		require.NoError(t, err)
		assert.Equal(t, int32(2), callCount.Load(), "should retry once after 500")
	})

	t.Run("When server returns 500 for all attempts it should exhaust retries", func(t *testing.T) {
		var callCount atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		err := Deliver(context.Background(), server.Client(), Delivery{
			Config: Config{
				URL:          server.URL,
				Timeout:      5 * time.Second,
				MaxAttempts:  2,
				BackoffDelay: 10 * time.Millisecond,
				Deadline:     30 * time.Second,
			},
			DeliveryID: "dev1/0",
		}, log)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attempts exhausted")
		assert.Equal(t, int32(2), callCount.Load())
	})

	t.Run("When no Authorization header it should omit it", func(t *testing.T) {
		var authHeader string
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		err := Deliver(context.Background(), server.Client(), Delivery{
			Config: Config{
				URL:         server.URL,
				Timeout:     5 * time.Second,
				MaxAttempts: 1,
			},
			DeliveryID: "dev1/0",
		}, log)
		require.NoError(t, err)
		assert.Empty(t, authHeader)
	})
}

func TestComputeBackoff(t *testing.T) {
	t.Run("When first attempt it should return base delay", func(t *testing.T) {
		assert.Equal(t, 2*time.Second, ComputeBackoff(1, 2*time.Second, 2*time.Minute))
	})

	t.Run("When second attempt it should double", func(t *testing.T) {
		assert.Equal(t, 4*time.Second, ComputeBackoff(2, 2*time.Second, 2*time.Minute))
	})

	t.Run("When backoff exceeds max it should cap", func(t *testing.T) {
		assert.Equal(t, 30*time.Second, ComputeBackoff(10, 2*time.Second, 30*time.Second))
	})
}
