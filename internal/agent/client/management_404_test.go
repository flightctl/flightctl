package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

func TestIsDeviceNotFoundError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   bool
	}{
		{
			name:       "device not found with JSON status",
			statusCode: 404,
			body: func() string {
				status := v1alpha1.Status{
					Code:    404,
					Message: "Device of name \"test-device\" not found",
				}
				b, _ := json.Marshal(status)
				return string(b)
			}(),
			expected: true,
		},
		{
			name:       "device not found with plain text",
			statusCode: 404,
			body:       "Device of name \"test-device\" not found",
			expected:   true,
		},
		{
			name:       "other 404 error",
			statusCode: 404,
			body:       "Fleet not found",
			expected:   false,
		},
		{
			name:       "non-404 error",
			statusCode: 500,
			body:       "Device of name \"test-device\" not found",
			expected:   false,
		},
		{
			name:       "success response",
			statusCode: 200,
			body:       "OK",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}

			// Create a temporary client to test the method
			client := &DeviceNotFoundHTTPClient{}
			result := client.isDeviceNotFoundResponse(resp)
			if result != tt.expected {
				t.Errorf("isDeviceNotFoundError() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDeviceNotFoundHTTPClient(t *testing.T) {
	callbackCalled := atomic.Bool{}
	callback := func(ctx context.Context) error {
		callbackCalled.Store(true)
		return nil
	}

	// Create a mock HTTP client that returns a device 404
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: 404,
			Body: io.NopCloser(bytes.NewReader([]byte(`{
				"code": 404,
				"message": "Device of name \"test-device\" not found"
			}`))),
		},
	}

	httpClient := &http.Client{
		Transport: mockTransport,
	}

	client := &DeviceNotFoundHTTPClient{
		Client:   httpClient,
		Callback: callback,
	}

	req, _ := http.NewRequest("GET", "http://example.com/api/v1/devices/test-device", nil)
	_, err := client.Do(req)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// Give the goroutine time to execute
	time.Sleep(100 * time.Millisecond)

	if !callbackCalled.Load() {
		t.Error("Expected callback to be called for device 404")
	}
}

// mockRoundTripper is a simple mock for testing
type mockRoundTripper struct {
	response *http.Response
	err      error
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.response, m.err
}
