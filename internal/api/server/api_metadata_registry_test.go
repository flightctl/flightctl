package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestGetEndpointMetadata(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		method         string
		expectedFound  bool
		expectedOpID   string
		expectedRes    string
		expectedAction string
	}{
		{
			name:           "ResumeDevices endpoint with x-rbac",
			path:           "/api/v1/deviceactions/resume",
			method:         "POST",
			expectedFound:  true,
			expectedOpID:   "resumeDevices",
			expectedRes:    "devices/resume",
			expectedAction: "update",
		},
		{
			name:           "Device endpoint with path parameter",
			path:           "/api/v1/devices/{name}",
			method:         "GET",
			expectedFound:  true,
			expectedOpID:   "getDevice",
			expectedRes:    "",
			expectedAction: "",
		},
		{
			name:          "Non-existent endpoint",
			path:          "/api/v1/nonexistent",
			method:        "GET",
			expectedFound: false,
		},
		{
			name:          "Wrong method",
			path:          "/api/v1/deviceactions/resume",
			method:        "GET",
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a Chi router and set up a real route to get proper route context
			r := chi.NewRouter()

			// Add a route that matches our test pattern
			r.MethodFunc(tt.method, tt.path, func(w http.ResponseWriter, r *http.Request) {
				// This handler will be called during the test
				// The route context will be properly set by Chi
				metadata, found := GetEndpointMetadata(r)

				// Store results in response headers for verification
				if found {
					w.Header().Set("X-Found", "true")
					w.Header().Set("X-OperationID", metadata.OperationID)
					w.Header().Set("X-Resource", metadata.Resource)
					w.Header().Set("X-Action", metadata.Action)
				} else {
					w.Header().Set("X-Found", "false")
				}
				w.WriteHeader(http.StatusOK)
			})

			// Create a request and response recorder
			req, err := http.NewRequest(tt.method, tt.path, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()

			// Let Chi handle the request (this will set up the route context properly)
			r.ServeHTTP(rr, req)

			// Extract results from response headers
			foundHeader := rr.Header().Get("X-Found")
			found := foundHeader == "true"

			if found != tt.expectedFound {
				t.Errorf("GetEndpointMetadata() found = %v, want %v", found, tt.expectedFound)
				return
			}

			if !found {
				return // No need to check metadata if not found
			}

			operationID := rr.Header().Get("X-OperationID")
			resource := rr.Header().Get("X-Resource")
			action := rr.Header().Get("X-Action")

			if operationID != tt.expectedOpID {
				t.Errorf("GetEndpointMetadata() OperationID = %v, want %v", operationID, tt.expectedOpID)
			}

			if resource != tt.expectedRes {
				t.Errorf("GetEndpointMetadata() Resource = %v, want %v", resource, tt.expectedRes)
			}

			if action != tt.expectedAction {
				t.Errorf("GetEndpointMetadata() Action = %v, want %v", action, tt.expectedAction)
			}
		})
	}
}

func TestAPIMetadataRegistryContainsResumeDevices(t *testing.T) {
	// Verify that our registry contains the ResumeDevices endpoint
	// Test direct map lookup for the resumeDevices endpoint
	metadata, found := APIMetadataMap["POST:/api/v1/deviceactions/resume"]

	if !found {
		t.Error("ResumeDevices endpoint not found in APIMetadataMap")
		return
	}

	if metadata.OperationID != "resumeDevices" {
		t.Errorf("ResumeDevices OperationID = %v, want resumeDevices", metadata.OperationID)
	}
	if metadata.Resource != "devices/resume" {
		t.Errorf("ResumeDevices resource = %v, want devices/resume", metadata.Resource)
	}
	if metadata.Action != "update" {
		t.Errorf("ResumeDevices action = %v, want update", metadata.Action)
	}
}
