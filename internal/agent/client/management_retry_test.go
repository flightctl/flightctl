package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

func TestDeviceNotFoundHTTPClient_5xxRetry(t *testing.T) {
	callCount := atomic.Int32{}

	// Mock transport that returns 500 twice, then 200
	mockTransport := &retryMockRoundTripper{
		responses: []*http.Response{
			{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewReader([]byte("Internal Server Error"))),
			},
			{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewReader([]byte("Internal Server Error"))),
			},
			{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte("OK"))),
			},
		},
		callCount: &callCount,
	}

	httpClient := &http.Client{
		Transport: mockTransport,
	}

	client := &DeviceNotFoundHTTPClient{
		Client:   httpClient,
		Callback: nil, // No callback needed for this test
		log:      log.NewPrefixLogger("test"),
	}

	// Use a context with timeout to allow retries to complete
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.com/api/v1/test", nil)
	resp, err := client.Do(req)

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	// Should have been called 3 times (2 failures + 1 success)
	if callCount.Load() != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount.Load())
	}
}

func TestDeviceNotFoundHTTPClient_Non5xxNoRetry(t *testing.T) {
	callCount := atomic.Int32{}

	// Mock transport that returns 404 (should not retry)
	mockTransport := &retryMockRoundTripper{
		responses: []*http.Response{
			{
				StatusCode: 404,
				Body:       io.NopCloser(bytes.NewReader([]byte("Not Found"))),
			},
		},
		callCount: &callCount,
	}

	httpClient := &http.Client{
		Transport: mockTransport,
	}

	client := &DeviceNotFoundHTTPClient{
		Client:   httpClient,
		Callback: nil, // No callback needed for this test
		log:      log.NewPrefixLogger("test"),
	}

	req, _ := http.NewRequest("GET", "http://example.com/api/v1/test", nil)
	resp, err := client.Do(req)

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Fatalf("Expected status 404, got %d", resp.StatusCode)
	}

	// Should have been called only once (no retry for 404)
	if callCount.Load() != 1 {
		t.Errorf("Expected 1 call, got %d", callCount.Load())
	}
}

// retryMockRoundTripper is a mock that returns different responses on each call
type retryMockRoundTripper struct {
	responses []*http.Response
	callCount *atomic.Int32
	delay     time.Duration
}

func (m *retryMockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	count := int(m.callCount.Add(1)) - 1
	if count >= len(m.responses) {
		// Return the last response if we've run out
		return m.responses[len(m.responses)-1], nil
	}
	return m.responses[count], nil
}
