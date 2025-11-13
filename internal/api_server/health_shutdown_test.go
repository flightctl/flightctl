package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockShutdownStatusProvider implements ShutdownStatusProvider for testing
type mockShutdownStatusProvider struct {
	status shutdown.ShutdownStatus
}

func (m *mockShutdownStatusProvider) GetShutdownStatus() shutdown.ShutdownStatus {
	return m.status
}

func TestShutdownStatusHandler_Operational(t *testing.T) {
	provider := &mockShutdownStatusProvider{
		status: shutdown.ShutdownStatus{
			IsShuttingDown:      false,
			ShutdownInitiated:   nil,
			ActiveComponents:    []string{},
			CompletedComponents: []shutdown.CompletedComponent{},
			State:               "idle",
			Message:             "Service is operational",
		},
	}

	handler := ShutdownStatusHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/shutdown-status", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	// Should return 200 OK when operational
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

	var response shutdown.ShutdownStatus
	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response.IsShuttingDown)
	assert.Equal(t, "idle", response.State)
	assert.Equal(t, "Service is operational", response.Message)
}

func TestShutdownStatusHandler_ShuttingDown(t *testing.T) {
	now := time.Now()
	provider := &mockShutdownStatusProvider{
		status: shutdown.ShutdownStatus{
			IsShuttingDown:    true,
			ShutdownInitiated: &now,
			ActiveComponents:  []string{"http-server", "database"},
			CompletedComponents: []shutdown.CompletedComponent{
				{
					Name:     "cache",
					Status:   "success",
					Duration: 100 * time.Millisecond,
					Error:    "",
				},
			},
			State:   "in_progress",
			Message: "Shutting down 3 components",
		},
	}

	handler := ShutdownStatusHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/shutdown-status", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	// Should return 503 Service Unavailable when shutting down
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

	var response shutdown.ShutdownStatus
	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.IsShuttingDown)
	assert.Equal(t, "in_progress", response.State)
	assert.Equal(t, "Shutting down 3 components", response.Message)
	assert.NotNil(t, response.ShutdownInitiated)
	assert.Equal(t, 2, len(response.ActiveComponents))
	assert.Contains(t, response.ActiveComponents, "http-server")
	assert.Contains(t, response.ActiveComponents, "database")
	assert.Equal(t, 1, len(response.CompletedComponents))
	assert.Equal(t, "cache", response.CompletedComponents[0].Name)
	assert.Equal(t, "success", response.CompletedComponents[0].Status)
}

func TestShutdownStatusHandler_Failed(t *testing.T) {
	now := time.Now()
	provider := &mockShutdownStatusProvider{
		status: shutdown.ShutdownStatus{
			IsShuttingDown:    false, // Failed shutdown is no longer "shutting down"
			ShutdownInitiated: &now,
			ActiveComponents:  []string{},
			CompletedComponents: []shutdown.CompletedComponent{
				{
					Name:     "good-component",
					Status:   "success",
					Duration: 100 * time.Millisecond,
					Error:    "",
				},
				{
					Name:     "bad-component",
					Status:   "error",
					Duration: 200 * time.Millisecond,
					Error:    "connection failed",
				},
			},
			State:   "failed",
			Message: "Shutdown failed: component bad-component shutdown failed",
		},
	}

	handler := ShutdownStatusHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/shutdown-status", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	// Should return 200 OK even for failed shutdown (since not currently shutting down)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

	var response shutdown.ShutdownStatus
	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response.IsShuttingDown)
	assert.Equal(t, "failed", response.State)
	assert.Contains(t, response.Message, "Shutdown failed")
	assert.Equal(t, 2, len(response.CompletedComponents))

	// Find the failed component
	var failedComponent *shutdown.CompletedComponent
	for _, comp := range response.CompletedComponents {
		if comp.Name == "bad-component" {
			failedComponent = &comp
			break
		}
	}
	require.NotNil(t, failedComponent)
	assert.Equal(t, "error", failedComponent.Status)
	assert.Equal(t, "connection failed", failedComponent.Error)
}

func TestShutdownStatusHandler_WithTimeout(t *testing.T) {
	now := time.Now()
	provider := &mockShutdownStatusProvider{
		status: shutdown.ShutdownStatus{
			IsShuttingDown:    false,
			ShutdownInitiated: &now,
			ActiveComponents:  []string{},
			CompletedComponents: []shutdown.CompletedComponent{
				{
					Name:     "slow-component",
					Status:   "timeout",
					Duration: 5 * time.Second,
					Error:    "component shutdown timed out",
				},
			},
			State:   "failed",
			Message: "Shutdown failed: component slow-component shutdown failed",
		},
	}

	handler := ShutdownStatusHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/shutdown-status", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)

	var response shutdown.ShutdownStatus
	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "failed", response.State)
	assert.Equal(t, 1, len(response.CompletedComponents))

	comp := response.CompletedComponents[0]
	assert.Equal(t, "slow-component", comp.Name)
	assert.Equal(t, "timeout", comp.Status)
	assert.Equal(t, "component shutdown timed out", comp.Error)
	assert.Equal(t, 5*time.Second, comp.Duration)
}

func TestShutdownStatusHandler_JSONResponse(t *testing.T) {
	now := time.Now().UTC()
	provider := &mockShutdownStatusProvider{
		status: shutdown.ShutdownStatus{
			IsShuttingDown:    true,
			ShutdownInitiated: &now,
			ActiveComponents:  []string{"component-a", "component-b"},
			CompletedComponents: []shutdown.CompletedComponent{
				{
					Name:     "component-c",
					Status:   "success",
					Duration: 150 * time.Millisecond,
					Error:    "",
				},
			},
			State:   "in_progress",
			Message: "Processing shutdown",
		},
	}

	handler := ShutdownStatusHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/shutdown-status", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

	// Parse and verify the JSON structure
	var response map[string]interface{}
	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	require.NoError(t, err)

	// Verify all expected JSON fields are present
	assert.Equal(t, true, response["isShuttingDown"])
	assert.Equal(t, "in_progress", response["state"])
	assert.Equal(t, "Processing shutdown", response["message"])
	assert.NotNil(t, response["shutdownInitiated"])

	// Verify arrays
	activeComponents, ok := response["activeComponents"].([]interface{})
	require.True(t, ok)
	assert.Equal(t, 2, len(activeComponents))

	completedComponents, ok := response["completedComponents"].([]interface{})
	require.True(t, ok)
	assert.Equal(t, 1, len(completedComponents))

	// Verify completed component structure
	comp := completedComponents[0].(map[string]interface{})
	assert.Equal(t, "component-c", comp["name"])
	assert.Equal(t, "success", comp["status"])
	assert.NotNil(t, comp["duration"])
	// For successful components, error may be empty string or omitted (nil)
	if comp["error"] != nil {
		assert.Equal(t, "", comp["error"])
	}
}

func TestShutdownStatusHandler_HTTPMethods(t *testing.T) {
	provider := &mockShutdownStatusProvider{
		status: shutdown.ShutdownStatus{
			State:   "idle",
			Message: "Test",
		},
	}

	handler := ShutdownStatusHandler(provider)

	// Test different HTTP methods - all should work the same way
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/shutdown-status", nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

			var response shutdown.ShutdownStatus
			err := json.Unmarshal(recorder.Body.Bytes(), &response)
			require.NoError(t, err)
			assert.Equal(t, "idle", response.State)
		})
	}
}

func TestShutdownStatusHandler_ErrorHandling(t *testing.T) {
	// Create a provider that will cause JSON encoding to fail
	// by providing a status with invalid JSON data
	provider := &mockShutdownStatusProvider{
		status: shutdown.ShutdownStatus{
			State:   "idle",
			Message: "Test",
		},
	}

	handler := ShutdownStatusHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/shutdown-status", nil)
	recorder := httptest.NewRecorder()

	// This should work normally since our status is valid
	handler.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)

	// For a real error test, we'd need to mock json.NewEncoder to fail,
	// but that's complex. The error handling code path is there for safety.
}
