package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// APIError is a structured error returned by harness methods when the API responds
// with a non-success HTTP status code. It preserves the status code and the parsed
// v1beta1.Status body from the API response, enabling callers to use errors.As for
// precise error inspection instead of substring matching.
type APIError struct {
	// StatusCode is the HTTP status code returned by the API.
	StatusCode int
	// Status is the parsed API status body (nil if the body could not be parsed).
	Status *v1beta1.Status
	// Resource describes the resource type being operated on (e.g., "ImageBuild").
	Resource string
	// ResourceName is the name of the resource being operated on.
	ResourceName string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Status != nil {
		return fmt.Sprintf("%s %q: %d %s", e.Resource, e.ResourceName, e.StatusCode, e.Status.Message)
	}
	return fmt.Sprintf("%s %q: %d %s", e.Resource, e.ResourceName, e.StatusCode, http.StatusText(e.StatusCode))
}

// IsStatusCode returns true if the error has the given HTTP status code.
func (e *APIError) IsStatusCode(code int) bool {
	return e.StatusCode == code
}

// newAPIError creates an APIError from a response status code and raw body bytes.
// It attempts to parse the body as a v1beta1.Status JSON object.
func newAPIError(statusCode int, body []byte, resource, resourceName string) *APIError {
	apiErr := &APIError{
		StatusCode:   statusCode,
		Resource:     resource,
		ResourceName: resourceName,
	}

	var status v1beta1.Status
	if err := json.Unmarshal(body, &status); err == nil && status.Message != "" {
		apiErr.Status = &status
	}

	return apiErr
}
