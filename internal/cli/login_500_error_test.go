package cli

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoginOptions_TokenValidation_500Error(t *testing.T) {
	// This test reproduces the scenario from EDM-2831:
	// When token validation returns 500 Internal Server Error, the login should fail
	// and not report "Login successful"
	tests := []struct {
		name             string
		statusCode       int
		responseBody     string
		expectLoginError bool
		expectedErrMsg   string
	}{
		{
			name:             "500 Internal Server Error should fail login",
			statusCode:       500,
			responseBody:     `{"message": "Internal Server Error", "code": 500}`,
			expectLoginError: true,
			expectedErrMsg:   "server returned 500: Internal Server Error",
		},
		{
			name:             "401 Unauthorized should fail login",
			statusCode:       401,
			responseBody:     `{"message": "Unauthorized", "code": 401}`,
			expectLoginError: true,
			expectedErrMsg:   "server returned 401: Unauthorized",
		},
		{
			name:             "403 Forbidden should fail login",
			statusCode:       403,
			responseBody:     `{"message": "Forbidden", "code": 403}`,
			expectLoginError: true,
			expectedErrMsg:   "server returned 403: Forbidden",
		},
		{
			name:             "200 OK should succeed",
			statusCode:       200,
			responseBody:     `{"message": "OK", "code": 200}`,
			expectLoginError: false,
			expectedErrMsg:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the validateHttpResponse function directly
			// This is the core function that should catch HTTP errors
			err := validateHttpResponse([]byte(tt.responseBody), tt.statusCode, http.StatusOK)

			if tt.expectLoginError {
				assert.Error(t, err, "validateHttpResponse should return an error for status %d", tt.statusCode)
				assert.Contains(t, err.Error(), fmt.Sprintf("server returned %d", tt.statusCode))
			} else {
				assert.NoError(t, err, "validateHttpResponse should not return an error for status %d", tt.statusCode)
			}
		})
	}
}
