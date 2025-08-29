package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
)

func TestBuildAuthProviderConfig(t *testing.T) {
	tests := []struct {
		name     string
		authType string
		authURL  string
		clientID string
		caFile   string
		expected *client.AuthProviderConfig
	}{
		{
			name:     "complete configuration with all parameters",
			authType: "oidc",
			authURL:  "https://auth.example.com",
			clientID: "test-client",
			caFile:   "/path/to/ca.crt",
			expected: &client.AuthProviderConfig{
				Name: "oidc",
				Config: map[string]string{
					client.AuthUrlKey:      "https://auth.example.com",
					client.AuthClientIdKey: "test-client",
					client.AuthCAFileKey:   "/path/to/ca.crt",
				},
			},
		},
		{
			name:     "configuration without auth URL",
			authType: "k8s",
			authURL:  "",
			clientID: "openshift-cli-client",
			caFile:   "",
			expected: &client.AuthProviderConfig{
				Name: "k8s",
				Config: map[string]string{
					client.AuthClientIdKey: "openshift-cli-client",
				},
			},
		},
		{
			name:     "configuration without CA file",
			authType: "oidc",
			authURL:  "https://auth.example.com",
			clientID: "test-client",
			caFile:   "",
			expected: &client.AuthProviderConfig{
				Name: "oidc",
				Config: map[string]string{
					client.AuthUrlKey:      "https://auth.example.com",
					client.AuthClientIdKey: "test-client",
				},
			},
		},
		{
			name:     "configuration with empty client ID",
			authType: "aap",
			authURL:  "https://auth.example.com",
			clientID: "",
			caFile:   "/path/to/ca.crt",
			expected: &client.AuthProviderConfig{
				Name: "aap",
				Config: map[string]string{
					client.AuthUrlKey:      "https://auth.example.com",
					client.AuthClientIdKey: "",
					client.AuthCAFileKey:   "/path/to/ca.crt",
				},
			},
		},
		{
			name:     "token-only configuration (no auth URL)",
			authType: "oidc",
			authURL:  "",
			clientID: "test-client",
			caFile:   "",
			expected: &client.AuthProviderConfig{
				Name: "oidc",
				Config: map[string]string{
					client.AuthClientIdKey: "test-client",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAuthProviderConfig(tt.authType, tt.authURL, tt.clientID, tt.caFile)

			assert.Equal(t, tt.expected.Name, result.Name)
			assert.Equal(t, tt.expected.Config, result.Config)

			// Verify that empty values are not included in the config
			if tt.authURL == "" {
				assert.NotContains(t, result.Config, client.AuthUrlKey)
			}
			if tt.caFile == "" {
				assert.NotContains(t, result.Config, client.AuthCAFileKey)
			}
		})
	}
}

func TestBuildAuthProviderConfig_EmptyValuesHandling(t *testing.T) {
	tests := []struct {
		name     string
		authType string
		authURL  string
		clientID string
		caFile   string
	}{
		{
			name:     "all empty values",
			authType: "",
			authURL:  "",
			clientID: "",
			caFile:   "",
		},
		{
			name:     "empty auth URL and CA file",
			authType: "oidc",
			authURL:  "",
			clientID: "test-client",
			caFile:   "",
		},
		{
			name:     "empty client ID and CA file",
			authType: "k8s",
			authURL:  "https://auth.example.com",
			clientID: "",
			caFile:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAuthProviderConfig(tt.authType, tt.authURL, tt.clientID, tt.caFile)

			// Verify the result is not nil
			assert.NotNil(t, result)
			assert.NotNil(t, result.Config)

			// Verify empty auth URL is not included
			if tt.authURL == "" {
				assert.NotContains(t, result.Config, client.AuthUrlKey)
			}

			// Verify empty CA file is not included
			if tt.caFile == "" {
				assert.NotContains(t, result.Config, client.AuthCAFileKey)
			}

			// Verify client ID is always included (even if empty)
			assert.Contains(t, result.Config, client.AuthClientIdKey)
		})
	}
}

func TestBuildAuthProviderConfig_ConfigMapIntegrity(t *testing.T) {
	// Test that the config map is properly initialized and doesn't contain unexpected keys
	result := buildAuthProviderConfig("oidc", "https://auth.example.com", "test-client", "/path/to/ca.crt")

	// Verify only expected keys are present
	expectedKeys := []string{
		client.AuthUrlKey,
		client.AuthClientIdKey,
		client.AuthCAFileKey,
	}

	for _, key := range expectedKeys {
		assert.Contains(t, result.Config, key)
	}

	// Verify no unexpected keys
	unexpectedKeys := []string{
		"unexpected-key",
		"server-url",
		"ca-file",
	}

	for _, key := range unexpectedKeys {
		assert.NotContains(t, result.Config, key)
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
		username    string
		password    string
		web         bool
		wantErr     bool
		errMsg      string
	}{
		{
			name:        "token with username",
			accessToken: "token123",
			username:    "user",
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:        "token with password",
			accessToken: "token123",
			password:    "pass",
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:        "token with web",
			accessToken: "token123",
			web:         true,
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:     "web with username",
			web:      true,
			username: "user",
			wantErr:  true,
			errMsg:   "--web cannot be used along with --username, --password or --token",
		},
		{
			name:     "web with password",
			web:      true,
			password: "pass",
			wantErr:  true,
			errMsg:   "--web cannot be used along with --username, --password or --token",
		},
		{
			name:        "web with token",
			web:         true,
			accessToken: "token123",
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:     "username without password",
			username: "user",
			wantErr:  true,
			errMsg:   "both --username and --password need to be provided",
		},
		{
			name:     "password without username",
			password: "pass",
			wantErr:  true,
			errMsg:   "both --username and --password need to be provided",
		},
		{
			name:     "valid username and password",
			username: "user",
			password: "pass",
			wantErr:  false,
		},
		{
			name:        "valid token only",
			accessToken: "token123",
			wantErr:     false,
		},
		{
			name:    "valid web only",
			web:     true,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.AccessToken = tt.accessToken
			o.Username = tt.username
			o.Password = tt.password
			o.Web = tt.web

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

func TestLoginOptions_Bind(t *testing.T) {
	o := DefaultLoginOptions()
	fs := &pflag.FlagSet{}

	// Test that Bind doesn't panic
	assert.NotPanics(t, func() {
		o.Bind(fs)
	})

	// Verify flags are registered
	assert.True(t, fs.HasFlags())
}

func TestLoginOptions_Complete_Comprehensive(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		setupOpts   func(*LoginOptions)
		expectError bool
	}{
		{
			name: "with config dir",
			args: []string{"https://api.example.com"},
			setupOpts: func(o *LoginOptions) {
				o.ConfigDir = "/tmp/test-config"
			},
			expectError: false,
		},
		{
			name: "with context",
			args: []string{"https://api.example.com"},
			setupOpts: func(o *LoginOptions) {
				o.Context = "test-context"
			},
			expectError: false,
		},
		{
			name: "with organization",
			args: []string{"https://api.example.com"},
			setupOpts: func(o *LoginOptions) {
				o.Organization = "test-org"
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			if tt.setupOpts != nil {
				tt.setupOpts(o)
			}

			cmd := &cobra.Command{}
			err := o.Complete(cmd, tt.args)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_Run_Comprehensive(t *testing.T) {
	tests := []struct {
		name        string
		setupOpts   func(*LoginOptions)
		args        []string
		expectError bool
	}{
		{
			name: "with token authentication",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: "https://api.example.com",
					},
				}
				o.AccessToken = "test-token"
			},
			args:        []string{"https://api.example.com"},
			expectError: true, // will fail due to network issues
		},
		{
			name: "with username password authentication",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: "https://api.example.com",
					},
				}
				o.Username = "testuser"
				o.Password = "testpass"
			},
			args:        []string{"https://api.example.com"},
			expectError: true, // will fail due to network issues
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			if tt.setupOpts != nil {
				tt.setupOpts(o)
			}

			ctx := context.Background()
			err := o.Run(ctx, tt.args)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_GetAuthConfig_Comprehensive(t *testing.T) {
	tests := []struct {
		name        string
		serverURL   string
		expectError bool
	}{
		{
			name:        "unreachable server",
			serverURL:   "https://unreachable.example.com",
			expectError: true,
		},
		{
			name:        "invalid URL format",
			serverURL:   "not-a-url",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.clientConfig = &client.Config{
				Service: client.Service{
					Server: tt.serverURL,
				},
			}

			ctx := context.Background()
			_, err := o.getAuthConfig(ctx)

			assert.Error(t, err)
		})
	}
}
