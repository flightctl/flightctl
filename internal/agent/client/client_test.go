package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
)

func TestNewFromConfigWithRetry(t *testing.T) {
	require := require.New(t)
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, err := w.Write([]byte("Server error"))
			require.NoError(err)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"apiVersion":"v1beta1","kind":"Device","metadata":{"name":"test"}}`))
			require.NoError(err)
		}
	}))
	defer server.Close()

	config := &baseclient.Config{
		Service: baseclient.Service{
			Server: server.URL,
		},
	}

	retryConfig := poll.Config{
		BaseDelay: 10 * time.Millisecond,
		Factor:    2.0,
		MaxDelay:  100 * time.Millisecond,
		MaxSteps:  3,
	}

	log := log.NewPrefixLogger("test")
	client, err := NewFromConfig(config, log, WithHTTPRetry(retryConfig))
	require.NoError(err)
	require.NotNil(client)

	// trigger retries
	ctx := context.Background()
	resp, err := client.GetRenderedDeviceWithResponse(ctx, "test", nil)
	require.NoError(err)

	// should succeeded after 3 attempts
	require.Equal(3, attempts)
	require.Equal(http.StatusOK, resp.StatusCode())
}

func TestManagementClient_DeviceNotFoundHandling(t *testing.T) {
	require := require.New(t)

	t.Run("returns ErrDeviceNotFound for 404 with device not found content", func(t *testing.T) {
		// Create a test server that returns 404 with device not found content
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, err := w.Write([]byte(`Device of name "test-device" not found`))
			require.NoError(err)
		}))
		defer server.Close()

		// Create management client
		config := &baseclient.Config{
			Service: baseclient.Service{
				Server: server.URL,
			},
		}

		retryConfig := poll.Config{
			BaseDelay: 10 * time.Millisecond,
			Factor:    2.0,
			MaxDelay:  100 * time.Millisecond,
			MaxSteps:  3,
		}

		log := log.NewPrefixLogger("test")
		httpClient, err := NewFromConfig(config, log, WithHTTPRetry(retryConfig))
		require.NoError(err)
		require.NotNil(httpClient)

		// Create management client wrapper
		managementClient := NewManagement(httpClient, nil)

		// Call GetRenderedDevice
		ctx := context.Background()
		device, statusCode, err := managementClient.GetRenderedDevice(ctx, "test-device", nil)

		// Verify the response
		if err == nil {
			t.Logf("Expected error but got nil. StatusCode: %d, Device: %v", statusCode, device)
		}
		require.Error(err)
		require.Equal(ErrDeviceNotFound, err)
		require.Equal(http.StatusNotFound, statusCode)
		require.Nil(device)
	})

	t.Run("returns generic 404 for 404 without device not found content", func(t *testing.T) {
		// Create a test server that returns 404 without device not found content
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, err := w.Write([]byte(`Resource not found`))
			require.NoError(err)
		}))
		defer server.Close()

		// Create management client
		config := &baseclient.Config{
			Service: baseclient.Service{
				Server: server.URL,
			},
		}

		retryConfig := poll.Config{
			BaseDelay: 10 * time.Millisecond,
			Factor:    2.0,
			MaxDelay:  100 * time.Millisecond,
			MaxSteps:  3,
		}

		log := log.NewPrefixLogger("test")
		httpClient, err := NewFromConfig(config, log, WithHTTPRetry(retryConfig))
		require.NoError(err)
		require.NotNil(httpClient)

		// Create management client wrapper
		managementClient := NewManagement(httpClient, nil)

		// Call GetRenderedDevice
		ctx := context.Background()
		device, statusCode, err := managementClient.GetRenderedDevice(ctx, "test-device", nil)

		// Verify the response - should be generic 404, not ErrDeviceNotFound
		require.NoError(err)
		require.Equal(http.StatusNotFound, statusCode)
		require.Nil(device)
	})

	t.Run("returns generic 404 for 404 with empty body", func(t *testing.T) {
		// Create a test server that returns 404 with empty body
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			// No body written
		}))
		defer server.Close()

		// Create management client
		config := &baseclient.Config{
			Service: baseclient.Service{
				Server: server.URL,
			},
		}

		retryConfig := poll.Config{
			BaseDelay: 10 * time.Millisecond,
			Factor:    2.0,
			MaxDelay:  100 * time.Millisecond,
			MaxSteps:  3,
		}

		log := log.NewPrefixLogger("test")
		httpClient, err := NewFromConfig(config, log, WithHTTPRetry(retryConfig))
		require.NoError(err)
		require.NotNil(httpClient)

		// Create management client wrapper
		managementClient := NewManagement(httpClient, nil)

		// Call GetRenderedDevice
		ctx := context.Background()
		device, statusCode, err := managementClient.GetRenderedDevice(ctx, "test-device", nil)

		// Verify the response - should be generic 404, not ErrDeviceNotFound
		require.NoError(err)
		require.Equal(http.StatusNotFound, statusCode)
		require.Nil(device)
	})
}
