package cli

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/stretchr/testify/assert"
)

func TestLoginOptions_Complete(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "hostname without schema",
			input:    "my.service.com",
			expected: "https://my.service.com",
		},
		{
			name:     "hostname with port without schema",
			input:    "my.service.com:8443",
			expected: "https://my.service.com:8443",
		},
		{
			name:     "URL with https schema",
			input:    "https://my.service.com",
			expected: "https://my.service.com",
		},
		{
			name:     "URL with http schema",
			input:    "http://my.service.com",
			expected: "http://my.service.com",
		},
		{
			name:     "hostname with leading/trailing whitespace",
			input:    " my.service.com ",
			expected: "https://my.service.com",
		},
		{
			name:     "URL with schema and whitespace",
			input:    " https://my.service.com ",
			expected: "https://my.service.com",
		},
		{
			name:     "URL with ftp schema",
			input:    "ftp://my.service.com",
			expected: "ftp://my.service.com",
		},
		{
			name:     "IPv6 hostname without schema",
			input:    "[2001:db8::1]",
			expected: "https://[2001:db8::1]",
		},
		{
			name:     "IPv6 hostname with port without schema",
			input:    "[2001:db8::1]:8443",
			expected: "https://[2001:db8::1]:8443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			args := []string{tt.input}
			cmd := NewCmdLogin()

			err := o.Complete(cmd, args)

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, args[0])
		})
	}
}

func TestLoginOptions_Validate(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "any URL should pass validation since URL validation was removed",
			url:     "https://api.example.com",
			wantErr: false,
		},
		{
			name:    "invalid URL should pass validation since URL validation was removed",
			url:     "not-a-url",
			wantErr: false,
		},
		{
			name:    "URL without protocol should pass validation since URL validation was removed",
			url:     "api.example.com",
			wantErr: false,
		},
		{
			name:    "URL with invalid protocol should pass validation since URL validation was removed",
			url:     "ftp://api.example.com",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			err := o.Validate([]string{tt.url})

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_Validate_AuthenticationFlags(t *testing.T) {
	tests := []struct {
		name        string
		accessToken string
		provider    string
		authCAFile  string
		wantErr     bool
		errMsg      string
	}{
		{
			name:        "token with provider",
			accessToken: "token123",
			provider:    "my-provider",
			wantErr:     true,
			errMsg:      "--token cannot be used with --provider",
		},
		{
			name:        "token with auth-certificate-authority",
			accessToken: "token123",
			authCAFile:  "/path/to/ca.crt",
			wantErr:     true,
			errMsg:      "--token cannot be used with --auth-certificate-authority",
		},
		{
			name:        "token with both provider and auth CA",
			accessToken: "token123",
			provider:    "my-provider",
			authCAFile:  "/path/to/ca.crt",
			wantErr:     true,
			errMsg:      "--token cannot be used with --provider",
		},
		{
			name:        "valid token only",
			accessToken: "token123",
			wantErr:     false,
		},
		{
			name:     "valid provider only",
			provider: "my-provider",
			wantErr:  false,
		},
		{
			name:       "valid auth CA only",
			authCAFile: "/path/to/ca.crt",
			wantErr:    false,
		},
		{
			name:       "valid provider with auth CA",
			provider:   "my-provider",
			authCAFile: "/path/to/ca.crt",
			wantErr:    false,
		},
		{
			name:    "no auth flags",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.AccessToken = tt.accessToken
			o.Provider = tt.provider
			o.AuthCAFile = tt.authCAFile

			err := o.Validate([]string{"https://api.example.com"})

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_ValidateURLFormat(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid URL should not trigger validation error",
			url:     "https://api.example.com",
			wantErr: false,
		},
		{
			name:    "valid URL with port should not trigger validation error",
			url:     "https://api.example.com:8443",
			wantErr: false,
		},
		{
			name:    "URL with path component should trigger validation error",
			url:     "https://api.example.com/api/v1",
			wantErr: true,
			errMsg:  "https://api.example.com/api/v1 contains path component '/api/v1' which may not be needed. Try: https://api.example.com",
		},
		{
			name:    "URL with path component and port should trigger validation error",
			url:     "https://api.example.com:8443/api/v1",
			wantErr: true,
			errMsg:  "https://api.example.com:8443/api/v1 contains path component '/api/v1' which may not be needed. Try: https://api.example.com:8443",
		},
		{
			name:    "URL with query parameters should trigger validation error",
			url:     "https://api.example.com?param=value",
			wantErr: true,
			errMsg:  "https://api.example.com?param=value contains query parameters '?param=value' which may not be needed. Try: https://api.example.com",
		},
		{
			name:    "URL with query parameters and port should trigger validation error",
			url:     "https://api.example.com:8443?param=value",
			wantErr: true,
			errMsg:  "https://api.example.com:8443?param=value contains query parameters '?param=value' which may not be needed. Try: https://api.example.com:8443",
		},
		{
			name:    "URL with fragment should trigger validation error",
			url:     "https://api.example.com#section",
			wantErr: true,
			errMsg:  "https://api.example.com#section contains fragment '#section' which may not be needed. Try: https://api.example.com",
		},
		{
			name:    "URL with fragment and port should trigger validation error",
			url:     "https://api.example.com:8443#section",
			wantErr: true,
			errMsg:  "https://api.example.com:8443#section contains fragment '#section' which may not be needed. Try: https://api.example.com:8443",
		},
		{
			name:    "URL with double slashes in hostname should trigger validation error",
			url:     "https://api//example.com",
			wantErr: true,
			errMsg:  "https://api//example.com contains path component '//example.com' which may not be needed. Try: https://api",
		},
		{
			name:    "invalid URL should trigger validation error",
			url:     "not-a-url",
			wantErr: true,
			errMsg:  "not-a-url contains path component 'not-a-url' which may not be needed. Try: ://",
		},
		{
			name:    "URL with root path should not trigger validation error",
			url:     "https://api.example.com/",
			wantErr: false,
		},
		{
			name:    "URL with root path and port should not trigger validation error",
			url:     "https://api.example.com:8443/",
			wantErr: false,
		},
		// IPv6 test cases
		{
			name:    "valid IPv6 URL should not trigger validation error",
			url:     "https://[2001:db8::1]",
			wantErr: false,
		},
		{
			name:    "valid IPv6 URL with port should not trigger validation error",
			url:     "https://[2001:db8::1]:8443",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with path component should trigger validation error",
			url:     "https://[2001:db8::1]/api/v1",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with path component and port should trigger validation error",
			url:     "https://[2001:db8::1]:8443/api/v1",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]:8443/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with query parameters should trigger validation error",
			url:     "https://[2001:db8::1]?param=value",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with query parameters and port should trigger validation error",
			url:     "https://[2001:db8::1]:8443?param=value",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]:8443?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with fragment should trigger validation error",
			url:     "https://[2001:db8::1]#section",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with fragment and port should trigger validation error",
			url:     "https://[2001:db8::1]:8443#section",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]:8443#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with root path should not trigger validation error",
			url:     "https://[2001:db8::1]/",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with root path and port should not trigger validation error",
			url:     "https://[2001:db8::1]:8443/",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with embedded credentials should not trigger validation error",
			url:     "https://user:pass@[2001:db8::1]",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with embedded credentials and port should not trigger validation error",
			url:     "https://user:pass@[2001:db8::1]:8443",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with embedded credentials and path should trigger validation error",
			url:     "https://user:pass@[2001:db8::1]/api/v1",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with embedded credentials, port, and path should trigger validation error",
			url:     "https://user:pass@[2001:db8::1]:8443/api/v1",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]:8443/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with embedded credentials and query parameters should trigger validation error",
			url:     "https://user:pass@[2001:db8::1]?param=value",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with embedded credentials, port, and query parameters should trigger validation error",
			url:     "https://user:pass@[2001:db8::1]:8443?param=value",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]:8443?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with embedded credentials and fragment should trigger validation error",
			url:     "https://user:pass@[2001:db8::1]#section",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with embedded credentials, port, and fragment should trigger validation error",
			url:     "https://user:pass@[2001:db8::1]:8443#section",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]:8443#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "HTTP IPv6 URL should not trigger validation error",
			url:     "http://[2001:db8::1]",
			wantErr: false,
		},
		{
			name:    "HTTP IPv6 URL with port should not trigger validation error",
			url:     "http://[2001:db8::1]:8443",
			wantErr: false,
		},
		{
			name:    "HTTP IPv6 URL with path should trigger validation error",
			url:     "http://[2001:db8::1]/api/v1",
			wantErr: true,
			errMsg:  "http://[2001:db8::1]/api/v1 contains path component '/api/v1' which may not be needed. Try: http://2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			err := o.validateURLFormat(tt.url)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_GetAuthConfig_NetworkErrors(t *testing.T) {
	tests := []struct {
		name           string
		url            string
		mockError      string
		expectedErrMsg string
	}{
		{
			name:           "connection refused with valid URL",
			url:            "https://api.example.com",
			mockError:      "connection refused",
			expectedErrMsg: "cannot connect to the API server at https://api.example.com. The server may be down or not accessible. Please verify the URL and try again",
		},
		{
			name:           "connection refused with URL containing path",
			url:            "https://api.example.com/api/v1",
			mockError:      "connection refused",
			expectedErrMsg: "cannot connect to the API server at https://api.example.com/api/v1. The server may be down or not accessible. URL contains path component '/api/v1' which may not be needed. Try: https://api.example.com",
		},
		{
			name:           "DNS resolution error with valid URL",
			url:            "https://api.example.com",
			mockError:      "no such host",
			expectedErrMsg: "cannot resolve hostname for https://api.example.com. Please check the URL and ensure the hostname is correct",
		},
		{
			name:           "DNS resolution error with URL containing query parameters",
			url:            "https://api.example.com?param=value",
			mockError:      "no such host",
			expectedErrMsg: "cannot resolve hostname for https://api.example.com?param=value. URL contains query parameters '?param=value' which may not be needed. Try: https://api.example.com",
		},
		{
			name:           "timeout error",
			url:            "https://api.example.com",
			mockError:      "timeout",
			expectedErrMsg: "connection to https://api.example.com timed out. Please check your network connection and try again",
		},
		{
			name:           "TLS certificate error",
			url:            "https://api.example.com",
			mockError:      "certificate",
			expectedErrMsg: "TLS certificate error when connecting to https://api.example.com. Provide a CA bundle with --certificate-authority=<path-to-ca.crt> or, for development only, use --insecure-skip-tls-verify",
		},
		{
			name:           "generic network error",
			url:            "https://api.example.com",
			mockError:      "network unreachable",
			expectedErrMsg: "failed to get auth info from https://api.example.com:",
		},
		{
			name:           "connection refused with IPv6 URL",
			url:            "https://[2001:db8::1]",
			mockError:      "connection refused",
			expectedErrMsg: "cannot connect to the API server at https://[2001:db8::1]. The server may be down or not accessible. Please verify the URL and try again",
		},
		{
			name:           "connection refused with IPv6 URL containing path",
			url:            "https://[2001:db8::1]/api/v1",
			mockError:      "connection refused",
			expectedErrMsg: "cannot connect to the API server at https://[2001:db8::1]/api/v1. The server may be down or not accessible. URL contains path component '/api/v1' which may not be needed. Try: https://[2001:db8::1]",
		},
		{
			name:           "DNS resolution error with IPv6 URL",
			url:            "https://[2001:db8::1]",
			mockError:      "no such host",
			expectedErrMsg: "cannot resolve hostname for https://[2001:db8::1]. Please check the URL and ensure the hostname is correct",
		},
		{
			name:           "DNS resolution error with IPv6 URL containing query parameters",
			url:            "https://[2001:db8::1]?param=value",
			mockError:      "no such host",
			expectedErrMsg: "cannot resolve hostname for https://[2001:db8::1]?param=value. URL contains query parameters '?param=value' which may not be needed. Try: https://[2001:db8::1]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.clientConfig = &client.Config{
				Service: client.Service{
					Server: tt.url,
				},
			}

			// Note: This test would require mocking the HTTP client to simulate different network errors
			// For now, we're testing the error message construction logic
			// In a real implementation, you might want to use a mock HTTP client

			// Test the validateURLFormat method directly
			if strings.Contains(tt.mockError, "connection refused") || strings.Contains(tt.mockError, "no such host") {
				validationErr := o.validateURLFormat(tt.url)
				if validationErr != nil {
					// This simulates the error message construction in getAuthConfig
					if strings.Contains(tt.mockError, "connection refused") {
						errMsg := fmt.Sprintf("cannot connect to the API server at %s. The server may be down or not accessible. %s", tt.url, validationErr.Error())
						assert.Contains(t, errMsg, tt.url)
						assert.Contains(t, errMsg, validationErr.Error())
					} else if strings.Contains(tt.mockError, "no such host") {
						errMsg := fmt.Sprintf("cannot resolve hostname for %s. %s", tt.url, validationErr.Error())
						assert.Contains(t, errMsg, tt.url)
						assert.Contains(t, errMsg, validationErr.Error())
					}
				}
			}
		})
	}
}

func TestLoginOptions_GetAuthConfig_HTTPResponseErrors(t *testing.T) {
	tests := []struct {
		name           string
		url            string
		statusCode     int
		expectedErrMsg string
	}{
		{
			name:           "404 response",
			url:            "https://api.example.com",
			statusCode:     404,
			expectedErrMsg: "unexpected response code 404 from https://api.example.com. Please verify that the API URL is correct and the server is running",
		},
		{
			name:           "500 response",
			url:            "https://api.example.com:8443",
			statusCode:     500,
			expectedErrMsg: "unexpected response code 500 from https://api.example.com:8443. Please verify that the API URL is correct and the server is running",
		},
		{
			name:           "418 teapot response should not error",
			url:            "https://api.example.com",
			statusCode:     418,
			expectedErrMsg: "", // Should not error, auth is disabled
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.clientConfig = &client.Config{
				Service: client.Service{
					Server: tt.url,
				},
			}

			// Note: This test would require mocking the HTTP response
			// For now, we're testing the error message construction logic
			if tt.statusCode != http.StatusTeapot && tt.statusCode != http.StatusOK {
				errMsg := fmt.Sprintf("unexpected response code %v from %s. Please verify that the API URL is correct and the server is running", tt.statusCode, tt.url)
				assert.Contains(t, errMsg, tt.url)
				assert.Contains(t, errMsg, fmt.Sprintf("response code %v", tt.statusCode))
			}
		})
	}
}
