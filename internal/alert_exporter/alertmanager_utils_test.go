package alert_exporter

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "network timeout error",
			err:      &net.OpError{Op: "dial", Err: &timeoutError{}},
			expected: true,
		},
		{
			name:     "DNS temporary error",
			err:      &net.DNSError{Err: "temporary", IsTemporary: true},
			expected: true,
		},
		{
			name:     "HTTP 500 error",
			err:      errors.New("alertmanager returned status 500 Internal Server Error"),
			expected: true,
		},
		{
			name:     "HTTP 502 error",
			err:      errors.New("alertmanager returned status 502 Bad Gateway"),
			expected: true,
		},
		{
			name:     "HTTP 503 error",
			err:      errors.New("alertmanager returned status 503 Service Unavailable"),
			expected: true,
		},
		{
			name:     "HTTP 504 error",
			err:      errors.New("alertmanager returned status 504 Gateway Timeout"),
			expected: true,
		},
		{
			name:     "HTTP 429 error",
			err:      errors.New("alertmanager returned status 429 Too Many Requests"),
			expected: true,
		},
		{
			name:     "connection refused error",
			err:      errors.New("dial tcp: connection refused"),
			expected: true,
		},
		{
			name:     "no such host error",
			err:      errors.New("dial tcp: no such host"),
			expected: true,
		},
		{
			name:     "HTTP 400 error (not retryable)",
			err:      errors.New("alertmanager returned status 400 Bad Request"),
			expected: false,
		},
		{
			name:     "HTTP 401 error (not retryable)",
			err:      errors.New("alertmanager returned status 401 Unauthorized"),
			expected: false,
		},
		{
			name:     "generic error (not retryable)",
			err:      errors.New("some other error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("isRetryableError(%v) = %v, expected %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestCalculateBackoff(t *testing.T) {
	// Create a client for testing with default settings
	logger, _ := test.NewNullLogger()
	client := NewAlertmanagerClient("localhost", 9093, logger, nil)

	tests := []struct {
		name     string
		attempt  int
		expected time.Duration
	}{
		{
			name:     "first attempt",
			attempt:  0,
			expected: 500 * time.Millisecond,
		},
		{
			name:     "second attempt",
			attempt:  1,
			expected: 1 * time.Second,
		},
		{
			name:     "third attempt",
			attempt:  2,
			expected: 2 * time.Second,
		},
		{
			name:     "fourth attempt",
			attempt:  3,
			expected: 4 * time.Second,
		},
		{
			name:     "max delay reached",
			attempt:  10,
			expected: 10 * time.Second, // maxDelay
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.calculateBackoff(tt.attempt)
			if result != tt.expected {
				t.Errorf("calculateBackoff(%d) = %v, expected %v", tt.attempt, result, tt.expected)
			}
		})
	}
}

func TestPostBatchWithRetry_Success(t *testing.T) {
	// Create a test server that succeeds on first try
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create logger for testing
	logger, hook := test.NewNullLogger()

	// Parse server URL to get hostname and port
	hostname := "localhost"
	port := uint(server.Listener.Addr().(*net.TCPAddr).Port)

	client := NewAlertmanagerClient(hostname, port, logger, nil)

	// Test data
	batch := []AlertmanagerAlert{
		{
			Labels:   map[string]string{"alertname": "test"},
			StartsAt: time.Now(),
		},
	}

	err := client.postBatchWithRetry(batch)
	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	// Should not have any retry messages
	for _, entry := range hook.Entries {
		if entry.Level == logrus.WarnLevel && entry.Message != "" {
			t.Errorf("Unexpected warning log: %s", entry.Message)
		}
	}
}

func TestPostBatchWithRetry_EventualSuccess(t *testing.T) {
	attempts := 0
	// Create a test server that fails twice then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	// Create logger for testing
	logger, hook := test.NewNullLogger()

	// Parse server URL to get hostname and port
	hostname := "localhost"
	port := uint(server.Listener.Addr().(*net.TCPAddr).Port)

	client := NewAlertmanagerClient(hostname, port, logger, nil)

	// Test data
	batch := []AlertmanagerAlert{
		{
			Labels:   map[string]string{"alertname": "test"},
			StartsAt: time.Now(),
		},
	}

	err := client.postBatchWithRetry(batch)
	if err != nil {
		t.Errorf("Expected eventual success, got error: %v", err)
	}

	// Should have retry warning messages
	warnCount := 0
	infoCount := 0
	for _, entry := range hook.Entries {
		if entry.Level == logrus.WarnLevel {
			warnCount++
		}
		if entry.Level == logrus.InfoLevel && entry.Message == "Successfully sent alert batch after 2 retries" {
			infoCount++
		}
	}

	if warnCount != 2 {
		t.Errorf("Expected 2 warning messages, got %d", warnCount)
	}
	if infoCount != 1 {
		t.Errorf("Expected 1 success message, got %d", infoCount)
	}
}

func TestPostBatchWithRetry_NonRetryableError(t *testing.T) {
	// Create a test server that returns 400 Bad Request (non-retryable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	// Create logger for testing
	logger, hook := test.NewNullLogger()

	// Parse server URL to get hostname and port
	hostname := "localhost"
	port := uint(server.Listener.Addr().(*net.TCPAddr).Port)

	client := NewAlertmanagerClient(hostname, port, logger, nil)

	// Test data
	batch := []AlertmanagerAlert{
		{
			Labels:   map[string]string{"alertname": "test"},
			StartsAt: time.Now(),
		},
	}

	err := client.postBatchWithRetry(batch)
	if err == nil {
		t.Error("Expected error for non-retryable status, got success")
	}

	// Should have one error message about non-retryable error
	errorCount := 0
	for _, entry := range hook.Entries {
		if entry.Level == logrus.ErrorLevel && strings.Contains(entry.Message, "Non-retryable error sending alerts") {
			errorCount++
		}
	}

	if errorCount != 1 {
		t.Errorf("Expected 1 non-retryable error message, got %d", errorCount)
	}
}

func TestNewAlertmanagerClient_Configuration(t *testing.T) {
	logger, _ := test.NewNullLogger()

	// Test with nil configuration (should use defaults)
	client := NewAlertmanagerClient("localhost", 9093, logger, nil)

	// Test that defaults are applied
	if client.maxRetries != 3 {
		t.Errorf("Expected maxRetries=3 (default), got %d", client.maxRetries)
	}
	if client.baseDelay != 500*time.Millisecond {
		t.Errorf("Expected baseDelay=500ms (default), got %v", client.baseDelay)
	}
	if client.maxDelay != 10*time.Second {
		t.Errorf("Expected maxDelay=10s (default), got %v", client.maxDelay)
	}
	if client.requestTimeout != 10*time.Second {
		t.Errorf("Expected requestTimeout=10s (default), got %v", client.requestTimeout)
	}
}

func TestNewAlertmanagerClient_NilConfig(t *testing.T) {
	logger, _ := test.NewNullLogger()

	// Test with nil configuration
	client := NewAlertmanagerClient("test-host", 8080, logger, nil)

	// Should use defaults and provided hostname/port
	if client.hostname != "test-host" {
		t.Errorf("Expected hostname=test-host, got %s", client.hostname)
	}
	if client.port != 8080 {
		t.Errorf("Expected port=8080, got %d", client.port)
	}
	if client.maxRetries != 3 {
		t.Errorf("Expected maxRetries=3 (default), got %d", client.maxRetries)
	}
}

// Mock types for testing
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }
