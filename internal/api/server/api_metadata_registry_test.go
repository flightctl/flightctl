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
			path:           "/deviceactions/resume",
			method:         "POST",
			expectedFound:  true,
			expectedOpID:   "resumeDevices",
			expectedRes:    "devices/resume",
			expectedAction: "update",
		},
		{
			name:           "Device endpoint with path parameter",
			path:           "/devices/{name}",
			method:         "GET",
			expectedFound:  true,
			expectedOpID:   "getDevice",
			expectedRes:    "devices",
			expectedAction: "get",
		},
		{
			name:           "List devices endpoint",
			path:           "/devices",
			method:         "GET",
			expectedFound:  true,
			expectedOpID:   "listDevices",
			expectedRes:    "devices",
			expectedAction: "list",
		},
		{
			name:           "Fleet status endpoint",
			path:           "/fleets/{name}/status",
			method:         "GET",
			expectedFound:  true,
			expectedOpID:   "getFleetStatus",
			expectedRes:    "fleets/status",
			expectedAction: "get",
		},
		{
			name:          "Non-existent endpoint",
			path:          "/nonexistent",
			method:        "GET",
			expectedFound: false,
		},
		{
			name:          "Wrong method",
			path:          "/deviceactions/resume",
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
	metadata, found := APIMetadataMap["POST:/deviceactions/resume"]

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

	// Verify version information
	if len(metadata.Versions) == 0 {
		t.Error("ResumeDevices should have at least one version")
		return
	}
	if metadata.Versions[0].Version != "v1beta1" {
		t.Errorf("ResumeDevices version = %v, want v1beta1", metadata.Versions[0].Version)
	}
	if metadata.Versions[0].DeprecatedAt != nil {
		t.Errorf("ResumeDevices should not be deprecated, got %v", metadata.Versions[0].DeprecatedAt)
	}
}

func TestAPIMetadataVersionOrdering(t *testing.T) {
	// Test that versions are ordered correctly (stable > beta > alpha)
	// For now we only have v1beta1, but this test ensures the structure is correct
	metadata, found := APIMetadataMap["GET:/devices"]

	if !found {
		t.Error("listDevices endpoint not found in APIMetadataMap")
		return
	}

	if len(metadata.Versions) == 0 {
		t.Error("listDevices should have at least one version")
		return
	}

	// All endpoints should have v1beta1 version
	hasV1beta1 := false
	for _, v := range metadata.Versions {
		if v.Version == "v1beta1" {
			hasV1beta1 = true
			break
		}
	}
	if !hasV1beta1 {
		t.Error("listDevices should have v1beta1 version")
	}
}

func TestEndpointResourceInference(t *testing.T) {
	// Test that resources are correctly extracted from x-resource
	tests := []struct {
		key              string
		expectedResource string
		expectedAction   string
	}{
		{"GET:/devices", "devices", "list"},
		{"GET:/devices/{name}", "devices", "get"},
		{"POST:/devices", "devices", "create"},
		{"PUT:/devices/{name}", "devices", "update"},
		{"DELETE:/devices/{name}", "devices", "delete"},
		{"GET:/fleets", "fleets", "list"},
		{"GET:/fleets/{name}/status", "fleets/status", "get"},
		{"GET:/repositories", "repositories", "list"},
		{"GET:/resourcesyncs", "resourcesyncs", "list"},
		{"GET:/enrollmentrequests", "enrollmentrequests", "list"},
		{"GET:/certificatesigningrequests", "certificatesigningrequests", "list"},
		{"GET:/authproviders", "authproviders", "list"},
		{"GET:/events", "events", "list"},
		{"GET:/organizations", "organizations", "list"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			metadata, found := APIMetadataMap[tt.key]
			if !found {
				t.Errorf("Endpoint %s not found in APIMetadataMap", tt.key)
				return
			}
			if metadata.Resource != tt.expectedResource {
				t.Errorf("Endpoint %s resource = %v, want %v", tt.key, metadata.Resource, tt.expectedResource)
			}
			if metadata.Action != tt.expectedAction {
				t.Errorf("Endpoint %s action = %v, want %v", tt.key, metadata.Action, tt.expectedAction)
			}
		})
	}
}
