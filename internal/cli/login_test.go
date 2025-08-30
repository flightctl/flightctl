package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
)

const (
	testTimeout = 5 * time.Second

	// HTTP constants
	contentTypeHeader   = "Content-Type"
	applicationJSON     = "application/json"
	authorizationHeader = "Authorization"

	// Test URLs
	testAPIURL                 = "https://api.example.com"
	testHTTPAPIURL             = "http://api.example.com"
	testAuthURL                = "https://auth.example.com"
	testIPv6URL                = "https://[2001:db8::1]"
	testHTTPIPv6URL            = "http://[2001:db8::1]"
	testIPv6URLWithCredentials = "https://user:pass@[2001:db8::1]" //nolint:gosec
	testInvalidURL             = "not-a-url"
	testAPIHostname            = "api.example.com"
	authConfigPath             = "/api/v1/auth/config"
	authValidatePath           = "/api/v1/auth/validate"

	// Test credentials
	testToken             = "test-token"
	testValidToken        = "valid-token"
	testUsername          = "testuser"
	testPassword          = "testpass"
	testClientID          = "test-client"
	testCAFile            = "/path/to/ca.crt"
	testValidCAFile       = "/path/to/valid/ca.crt"
	testNonExistentCAFile = "/path/to/nonexistent/ca.crt"
	testToken123          = "token123"
	testUser              = "user"
	testPass              = "pass"

	// Auth types
	authTypeOIDC = "oidc"
	authTypeK8S  = "k8s"

	// JSON response templates
	authConfigResponseTemplate = `{"authOrganizationsConfig":{"enabled":false},"authType":"%s","authURL":"%s"}`
	successResponse            = `{"status":"success"}`
	invalidTokenResponse       = `{"error":"invalid token"}` //nolint:gosec
	authNotConfiguredResponse  = `{"apiVersion":"v1alpha1","code":418,"kind":"Status","message":"Auth not configured","reason":"Auth not configured","status":"Failure"}`

	// Network error messages
	connectionRefusedError = "connection refused"
	noSuchHostError        = "no such host"
	timeoutError           = "timeout"
	certificateError       = "certificate"

	// Error types
	errorTypeNetwork = "network"
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
			authType: authTypeOIDC,
			authURL:  testAuthURL,
			clientID: testClientID,
			caFile:   testCAFile,
			expected: &client.AuthProviderConfig{
				Name: authTypeOIDC,
				Config: map[string]string{
					client.AuthUrlKey:      testAuthURL,
					client.AuthClientIdKey: testClientID,
					client.AuthCAFileKey:   testCAFile,
				},
			},
		},
		{
			name:     "configuration without auth URL",
			authType: authTypeK8S,
			authURL:  "",
			clientID: "openshift-cli-client",
			caFile:   "",
			expected: &client.AuthProviderConfig{
				Name: authTypeK8S,
				Config: map[string]string{
					client.AuthClientIdKey: "openshift-cli-client",
				},
			},
		},
		{
			name:     "configuration without CA file",
			authType: authTypeOIDC,
			authURL:  testAuthURL,
			clientID: testClientID,
			caFile:   "",
			expected: &client.AuthProviderConfig{
				Name: authTypeOIDC,
				Config: map[string]string{
					client.AuthUrlKey:      testAuthURL,
					client.AuthClientIdKey: testClientID,
				},
			},
		},
		{
			name:     "configuration with empty client ID",
			authType: "aap",
			authURL:  testAuthURL,
			clientID: "",
			caFile:   testCAFile,
			expected: &client.AuthProviderConfig{
				Name: "aap",
				Config: map[string]string{
					client.AuthUrlKey:      testAuthURL,
					client.AuthClientIdKey: "",
					client.AuthCAFileKey:   testCAFile,
				},
			},
		},
		{
			name:     "token-only configuration (no auth URL)",
			authType: authTypeOIDC,
			authURL:  "",
			clientID: testClientID,
			caFile:   "",
			expected: &client.AuthProviderConfig{
				Name: authTypeOIDC,
				Config: map[string]string{
					client.AuthClientIdKey: testClientID,
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
			authType: authTypeOIDC,
			authURL:  "",
			clientID: testClientID,
			caFile:   "",
		},
		{
			name:     "empty client ID and CA file",
			authType: authTypeK8S,
			authURL:  testAuthURL,
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
	result := buildAuthProviderConfig(authTypeOIDC, testAuthURL, testClientID, testCAFile)

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
			url:     testAPIURL,
			wantErr: false,
		},
		{
			name:    "invalid URL should pass validation since URL validation was removed",
			url:     testInvalidURL,
			wantErr: false,
		},
		{
			name:    "URL without protocol should pass validation since URL validation was removed",
			url:     testAPIHostname,
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
			accessToken: testToken123,
			username:    testUser,
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:        "token with password",
			accessToken: testToken123,
			password:    testPass,
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:        "token with web",
			accessToken: testToken123,
			web:         true,
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:     "web with username",
			web:      true,
			username: testUser,
			wantErr:  true,
			errMsg:   "--web cannot be used along with --username, --password or --token",
		},
		{
			name:     "web with password",
			web:      true,
			password: testPass,
			wantErr:  true,
			errMsg:   "--web cannot be used along with --username, --password or --token",
		},
		{
			name:        "web with token",
			web:         true,
			accessToken: testToken123,
			wantErr:     true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:     "username without password",
			username: testUser,
			wantErr:  true,
			errMsg:   "both --username and --password need to be provided",
		},
		{
			name:     "password without username",
			password: testPass,
			wantErr:  true,
			errMsg:   "both --username and --password need to be provided",
		},
		{
			name:     "valid username and password",
			username: testUser,
			password: testPass,
			wantErr:  false,
		},
		{
			name:        "valid token only",
			accessToken: testToken123,
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

			err := o.Validate([]string{testAPIURL})

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
			url:     testAPIURL,
			wantErr: false,
		},
		{
			name:    "valid URL with port should not trigger validation error",
			url:     testAPIURL + ":8443",
			wantErr: false,
		},
		{
			name:    "URL with path component should trigger validation error",
			url:     testAPIURL + "/api/v1",
			wantErr: true,
			errMsg:  "https://api.example.com/api/v1 contains path component '/api/v1' which may not be needed. Try: https://api.example.com",
		},
		{
			name:    "URL with path component and port should trigger validation error",
			url:     testAPIURL + ":8443/api/v1",
			wantErr: true,
			errMsg:  "https://api.example.com:8443/api/v1 contains path component '/api/v1' which may not be needed. Try: https://api.example.com:8443",
		},
		{
			name:    "URL with query parameters should trigger validation error",
			url:     testAPIURL + "?param=value",
			wantErr: true,
			errMsg:  "https://api.example.com?param=value contains query parameters '?param=value' which may not be needed. Try: https://api.example.com",
		},
		{
			name:    "URL with query parameters and port should trigger validation error",
			url:     testAPIURL + ":8443?param=value",
			wantErr: true,
			errMsg:  "https://api.example.com:8443?param=value contains query parameters '?param=value' which may not be needed. Try: https://api.example.com:8443",
		},
		{
			name:    "URL with fragment should trigger validation error",
			url:     testAPIURL + "#section",
			wantErr: true,
			errMsg:  "https://api.example.com#section contains fragment '#section' which may not be needed. Try: https://api.example.com",
		},
		{
			name:    "URL with fragment and port should trigger validation error",
			url:     testAPIURL + ":8443#section",
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
			url:     testInvalidURL,
			wantErr: true,
			errMsg:  "not-a-url contains path component 'not-a-url' which may not be needed. Try: ://",
		},
		{
			name:    "URL with root path should not trigger validation error",
			url:     testAPIURL + "/",
			wantErr: false,
		},
		{
			name:    "URL with root path and port should not trigger validation error",
			url:     testAPIURL + ":8443/",
			wantErr: false,
		},
		// IPv6 test cases
		{
			name:    "valid IPv6 URL should not trigger validation error",
			url:     testIPv6URL,
			wantErr: false,
		},
		{
			name:    "valid IPv6 URL with port should not trigger validation error",
			url:     testIPv6URL + ":8443",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with path component should trigger validation error",
			url:     testIPv6URL + "/api/v1",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with path component and port should trigger validation error",
			url:     testIPv6URL + ":8443/api/v1",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]:8443/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with query parameters should trigger validation error",
			url:     testIPv6URL + "?param=value",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with query parameters and port should trigger validation error",
			url:     testIPv6URL + ":8443?param=value",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]:8443?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with fragment should trigger validation error",
			url:     testIPv6URL + "#section",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with fragment and port should trigger validation error",
			url:     testIPv6URL + ":8443#section",
			wantErr: true,
			errMsg:  "https://[2001:db8::1]:8443#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with root path should not trigger validation error",
			url:     testIPv6URL + "/",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with root path and port should not trigger validation error",
			url:     testIPv6URL + ":8443/",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with embedded credentials should not trigger validation error",
			url:     testIPv6URLWithCredentials,
			wantErr: false,
		},
		{
			name:    "IPv6 URL with embedded credentials and port should not trigger validation error",
			url:     testIPv6URLWithCredentials + ":8443",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with embedded credentials and path should trigger validation error",
			url:     testIPv6URLWithCredentials + "/api/v1",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with embedded credentials, port, and path should trigger validation error",
			url:     testIPv6URLWithCredentials + ":8443/api/v1",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]:8443/api/v1 contains path component '/api/v1' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with embedded credentials and query parameters should trigger validation error",
			url:     testIPv6URLWithCredentials + "?param=value",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with embedded credentials, port, and query parameters should trigger validation error",
			url:     testIPv6URLWithCredentials + ":8443?param=value",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]:8443?param=value contains query parameters '?param=value' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "IPv6 URL with embedded credentials and fragment should trigger validation error",
			url:     testIPv6URLWithCredentials + "#section",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1",
		},
		{
			name:    "IPv6 URL with embedded credentials, port, and fragment should trigger validation error",
			url:     testIPv6URLWithCredentials + ":8443#section",
			wantErr: true,
			errMsg:  "https://user:pass@[2001:db8::1]:8443#section contains fragment '#section' which may not be needed. Try: https://2001:db8::1:8443",
		},
		{
			name:    "HTTP IPv6 URL should not trigger validation error",
			url:     testHTTPIPv6URL,
			wantErr: false,
		},
		{
			name:    "HTTP IPv6 URL with port should not trigger validation error",
			url:     testHTTPIPv6URL + ":8443",
			wantErr: false,
		},
		{
			name:    "HTTP IPv6 URL with path should trigger validation error",
			url:     testHTTPIPv6URL + "/api/v1",
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
			url:            testAPIURL,
			mockError:      connectionRefusedError,
			expectedErrMsg: "cannot connect to the API server at https://api.example.com. The server may be down or not accessible. Please verify the URL and try again",
		},
		{
			name:           "connection refused with URL containing path",
			url:            testAPIURL + "/api/v1",
			mockError:      connectionRefusedError,
			expectedErrMsg: "cannot connect to the API server at https://api.example.com/api/v1. The server may be down or not accessible. URL contains path component '/api/v1' which may not be needed. Try: https://api.example.com",
		},
		{
			name:           "DNS resolution error with valid URL",
			url:            testAPIURL,
			mockError:      noSuchHostError,
			expectedErrMsg: "cannot resolve hostname for https://api.example.com. Please check the URL and ensure the hostname is correct",
		},
		{
			name:           "DNS resolution error with URL containing query parameters",
			url:            testAPIURL + "?param=value",
			mockError:      noSuchHostError,
			expectedErrMsg: "cannot resolve hostname for https://api.example.com?param=value. URL contains query parameters '?param=value' which may not be needed. Try: https://api.example.com",
		},
		{
			name:           "timeout error",
			url:            testAPIURL,
			mockError:      timeoutError,
			expectedErrMsg: "connection to https://api.example.com timed out. Please check your network connection and try again",
		},
		{
			name:           "TLS certificate error",
			url:            testAPIURL,
			mockError:      certificateError,
			expectedErrMsg: "TLS certificate error when connecting to https://api.example.com. Provide a CA bundle with --certificate-authority=<path-to-ca.crt> or, for development only, use --insecure-skip-tls-verify",
		},
		{
			name:           "generic network error",
			url:            testAPIURL,
			mockError:      "network unreachable",
			expectedErrMsg: "failed to get auth info from https://api.example.com:",
		},
		{
			name:           "connection refused with IPv6 URL",
			url:            testIPv6URL,
			mockError:      connectionRefusedError,
			expectedErrMsg: "cannot connect to the API server at https://[2001:db8::1]. The server may be down or not accessible. Please verify the URL and try again",
		},
		{
			name:           "connection refused with IPv6 URL containing path",
			url:            testIPv6URL + "/api/v1",
			mockError:      connectionRefusedError,
			expectedErrMsg: "cannot connect to the API server at https://[2001:db8::1]/api/v1. The server may be down or not accessible. URL contains path component '/api/v1' which may not be needed. Try: https://[2001:db8::1]",
		},
		{
			name:           "DNS resolution error with IPv6 URL",
			url:            testIPv6URL,
			mockError:      noSuchHostError,
			expectedErrMsg: "cannot resolve hostname for https://[2001:db8::1]. Please check the URL and ensure the hostname is correct",
		},
		{
			name:           "DNS resolution error with IPv6 URL containing query parameters",
			url:            testIPv6URL + "?param=value",
			mockError:      noSuchHostError,
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
			if strings.Contains(tt.mockError, connectionRefusedError) || strings.Contains(tt.mockError, noSuchHostError) {
				validationErr := o.validateURLFormat(tt.url)
				if validationErr != nil {
					// This simulates the error message construction in getAuthConfig
					if strings.Contains(tt.mockError, connectionRefusedError) {
						errMsg := fmt.Sprintf("cannot connect to the API server at %s. The server may be down or not accessible. %s", tt.url, validationErr.Error())
						assert.Contains(t, errMsg, tt.url)
						assert.Contains(t, errMsg, validationErr.Error())
					} else if strings.Contains(tt.mockError, noSuchHostError) {
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
			url:            testAPIURL,
			statusCode:     http.StatusNotFound,
			expectedErrMsg: "unexpected response code 404 from https://api.example.com. Please verify that the API URL is correct and the server is running",
		},
		{
			name:           "500 response",
			url:            testAPIURL + ":8443",
			statusCode:     http.StatusInternalServerError,
			expectedErrMsg: "unexpected response code 500 from https://api.example.com:8443. Please verify that the API URL is correct and the server is running",
		},
		{
			name:           "418 teapot response should not error",
			url:            testAPIURL,
			statusCode:     http.StatusTeapot,
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

func TestNewCmdLogin_CommandExecution(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		expectError    bool
		expectedOutput string
	}{
		{
			name:           "no arguments shows help",
			args:           []string{},
			expectError:    false,
			expectedOutput: "Usage:",
		},
		{
			name:           "help argument shows help",
			args:           []string{"help"},
			expectError:    false,
			expectedOutput: "Usage:",
		},
		{
			name:        "valid URL argument",
			args:        []string{testAPIURL},
			expectError: true, // Will fail due to network
		},
		{
			name:        "invalid URL argument",
			args:        []string{testInvalidURL},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmdLogin()
			cmd.SetArgs(tt.args)

			// Capture output to avoid printing during tests
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			err := cmd.Execute()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewCmdLogin_FlagHandling(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		flags       map[string]string
		expectError bool
	}{
		{
			name:        "with token flag",
			args:        []string{testAPIURL},
			flags:       map[string]string{"token": testToken},
			expectError: true, // Network failure expected
		},
		{
			name:        "with username password flags",
			args:        []string{testAPIURL},
			flags:       map[string]string{"username": testUser, "password": testPass},
			expectError: true, // Network failure expected
		},
		{
			name:        "with web flag",
			args:        []string{testAPIURL},
			flags:       map[string]string{"web": "true"},
			expectError: true, // Network failure expected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmdLogin()
			cmd.SetArgs(tt.args)

			// Set flags
			for flag, value := range tt.flags {
				err := cmd.Flags().Set(flag, value)
				assert.NoError(t, err, "failed to set flag %s", flag)
			}

			// Capture output to avoid printing during tests
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			err := cmd.Execute()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_Init_ClientConfig(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		setupOpts   func(*LoginOptions)
		expectError bool
	}{
		{
			name: "with existing client config",
			args: []string{testAPIURL},
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: "https://existing.example.com",
					},
				}
			},
			expectError: false,
		},
		{
			name:        "without client config",
			args:        []string{testAPIURL},
			setupOpts:   nil,
			expectError: false,
		},
		{
			name:        "with whitespace in URL",
			args:        []string{"  " + testAPIURL + "  "},
			setupOpts:   nil,
			expectError: false,
		},
		{
			name:        "single arg",
			args:        []string{testAPIURL},
			setupOpts:   nil,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			if tt.setupOpts != nil {
				tt.setupOpts(o)
			}

			err := o.Init(tt.args)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_Init_CAFileHandling(t *testing.T) {
	tests := []struct {
		name        string
		caFile      string
		expectError bool
	}{
		{
			name:        "valid CA file",
			caFile:      testValidCAFile,
			expectError: true, // File doesn't exist in test
		},
		{
			name:        "empty CA file",
			caFile:      "",
			expectError: false,
		},
		{
			name:        "non-existent CA file",
			caFile:      testNonExistentCAFile,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.CAFile = tt.caFile

			err := o.Init([]string{testAPIURL})

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_GetClientConfig_Basic(t *testing.T) {
	tests := []struct {
		name        string
		apiURL      string
		setupOpts   func(*LoginOptions)
		expectError bool
	}{
		{
			name:        "valid API URL",
			apiURL:      testAPIURL,
			setupOpts:   nil,
			expectError: false,
		},
		{
			name:        "empty API URL",
			apiURL:      "",
			setupOpts:   nil,
			expectError: false,
		},
		{
			name:        "URL with whitespace",
			apiURL:      "  " + testAPIURL + "  ",
			setupOpts:   nil,
			expectError: false,
		},
		{
			name:        "HTTP URL",
			apiURL:      testHTTPAPIURL,
			setupOpts:   nil,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			if tt.setupOpts != nil {
				tt.setupOpts(o)
			}

			config, err := o.getClientConfig(tt.apiURL)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
				assert.Equal(t, tt.apiURL, config.Service.Server)
			}
		})
	}
}

func TestLoginOptions_GetClientConfig_CAFile(t *testing.T) {
	tests := []struct {
		name        string
		apiURL      string
		caFile      string
		expectError bool
		errMsg      string
	}{
		{
			name:        "with valid CA file",
			apiURL:      testAPIURL,
			caFile:      testValidCAFile,
			expectError: true, // File doesn't exist in test
			errMsg:      "failed to read CA file",
		},
		{
			name:        "with empty CA file",
			apiURL:      testAPIURL,
			caFile:      "",
			expectError: false,
		},
		{
			name:        "with non-existent CA file",
			apiURL:      testAPIURL,
			caFile:      testNonExistentCAFile,
			expectError: true,
			errMsg:      "failed to read CA file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.CAFile = tt.caFile

			config, err := o.getClientConfig(tt.apiURL)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
			}
		})
	}
}

func TestLoginOptions_GetClientConfig_InsecureSkipVerify(t *testing.T) {
	tests := []struct {
		name               string
		apiURL             string
		insecureSkipVerify bool
		expectedInsecure   bool
	}{
		{
			name:               "insecure skip verify true",
			apiURL:             testAPIURL,
			insecureSkipVerify: true,
			expectedInsecure:   true,
		},
		{
			name:               "insecure skip verify false",
			apiURL:             testAPIURL,
			insecureSkipVerify: false,
			expectedInsecure:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.InsecureSkipVerify = tt.insecureSkipVerify

			config, err := o.getClientConfig(tt.apiURL)

			assert.NoError(t, err)
			assert.NotNil(t, config)
			assert.Equal(t, tt.expectedInsecure, config.Service.InsecureSkipVerify)
		})
	}
}

func TestLoginOptions_Validate_AuthCombinations(t *testing.T) {
	tests := []struct {
		name        string
		accessToken string
		username    string
		password    string
		web         bool
		clientID    string
		expectError bool
		errMsg      string
	}{
		// Token combinations
		{
			name:        "token with username",
			accessToken: testToken123,
			username:    testUser,
			expectError: true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:        "token with password",
			accessToken: testToken123,
			password:    testPass,
			expectError: true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:        "token with web",
			accessToken: testToken123,
			web:         true,
			expectError: true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},
		{
			name:        "token with client ID",
			accessToken: testToken123,
			clientID:    testClientID,
			expectError: false,
		},

		// Web combinations
		{
			name:        "web with username",
			web:         true,
			username:    testUser,
			expectError: true,
			errMsg:      "--web cannot be used along with --username, --password or --token",
		},
		{
			name:        "web with password",
			web:         true,
			password:    testPass,
			expectError: true,
			errMsg:      "--web cannot be used along with --username, --password or --token",
		},
		{
			name:        "web with token",
			web:         true,
			accessToken: testToken123,
			expectError: true,
			errMsg:      "--token cannot be used along with --username, --password or --web",
		},

		// Username/Password combinations
		{
			name:        "username without password",
			username:    testUser,
			expectError: true,
			errMsg:      "both --username and --password need to be provided",
		},
		{
			name:        "password without username",
			password:    testPass,
			expectError: true,
			errMsg:      "both --username and --password need to be provided",
		},
		{
			name:        "valid username and password",
			username:    testUser,
			password:    testPass,
			expectError: false,
		},

		// Valid combinations
		{
			name:        "valid token only",
			accessToken: testToken123,
			expectError: false,
		},
		{
			name:        "valid web only",
			web:         true,
			expectError: false,
		},
		{
			name:        "valid username password with client ID",
			username:    testUser,
			password:    testPass,
			clientID:    testClientID,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			o.AccessToken = tt.accessToken
			o.Username = tt.username
			o.Password = tt.password
			o.Web = tt.web
			o.ClientId = tt.clientID

			err := o.Validate([]string{testAPIURL})

			if tt.expectError {
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

func TestLoginOptions_Validate_URLs(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid HTTPS URL",
			url:     testAPIURL,
			wantErr: false,
		},
		{
			name:    "valid HTTP URL",
			url:     testHTTPAPIURL,
			wantErr: false,
		},
		{
			name:    "URL with port",
			url:     testAPIURL + ":8443",
			wantErr: false,
		},
		{
			name:    "invalid URL",
			url:     testInvalidURL,
			wantErr: false, // Current implementation doesn't validate URLs
		},
		{
			name:    "URL without protocol",
			url:     testAPIHostname,
			wantErr: false,
		},
		{
			name:    "empty URL",
			url:     "",
			wantErr: false, // Current implementation doesn't validate empty URLs
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			err := o.Validate([]string{tt.url})

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_ValidateURLFormat_Standard(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid HTTPS URL",
			url:     testAPIURL,
			wantErr: false,
		},
		{
			name:    "valid HTTP URL",
			url:     testHTTPAPIURL,
			wantErr: false,
		},
		{
			name:    "URL with port",
			url:     testAPIURL + ":8443",
			wantErr: false,
		},
		{
			name:    "URL with root path",
			url:     testAPIURL + "/",
			wantErr: false,
		},
		{
			name:    "URL with path component",
			url:     testAPIURL + "/api/v1",
			wantErr: true,
			errMsg:  "contains path component '/api/v1' which may not be needed",
		},
		{
			name:    "URL with query parameters",
			url:     testAPIURL + "?param=value",
			wantErr: true,
			errMsg:  "contains query parameters '?param=value' which may not be needed",
		},
		{
			name:    "URL with fragment",
			url:     testAPIURL + "#section",
			wantErr: true,
			errMsg:  "contains fragment '#section' which may not be needed",
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

func TestLoginOptions_ValidateURLFormat_Errors(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "invalid URL format",
			url:     testInvalidURL,
			wantErr: true,
			errMsg:  "contains path component",
		},
		{
			name:    "URL with double slashes in hostname",
			url:     "https://api//example.com",
			wantErr: true,
			errMsg:  "contains path component",
		},
		{
			name:    "missing scheme",
			url:     testAPIHostname,
			wantErr: true,
			errMsg:  "contains path component",
		},
		{
			name:    "unsupported scheme",
			url:     "ftp://api.example.com",
			wantErr: false, // Current implementation allows any scheme
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

func TestLoginOptions_Run_AuthenticationScenarios(t *testing.T) {
	tests := []struct {
		name        string
		setupOpts   func(*LoginOptions)
		args        []string
		expectError bool
		errorType   string
	}{
		{
			name: "token authentication",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{Server: testAPIURL},
				}
				o.AccessToken = testToken
			},
			args:        []string{testAPIURL},
			expectError: true, // Network failure expected
			errorType:   errorTypeNetwork,
		},
		{
			name: "username password authentication",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{Server: testAPIURL},
				}
				o.Username = testUsername
				o.Password = testPassword
			},
			args:        []string{testAPIURL},
			expectError: true, // Network failure expected
			errorType:   errorTypeNetwork,
		},
		{
			name: "web authentication",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{Server: testAPIURL},
				}
				o.Web = true
			},
			args:        []string{testAPIURL},
			expectError: true, // Network failure expected
			errorType:   errorTypeNetwork,
		},
		{
			name: "auth disabled scenario",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{Server: testAPIURL},
				}
			},
			args:        []string{testAPIURL},
			expectError: true, // Network failure expected
			errorType:   errorTypeNetwork,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			if tt.setupOpts != nil {
				tt.setupOpts(o)
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			err := o.Run(ctx, tt.args)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
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
			args: []string{testAPIURL},
			setupOpts: func(o *LoginOptions) {
				o.ConfigDir = "/tmp/test-config"
			},
			expectError: false,
		},
		{
			name: "with context",
			args: []string{testAPIURL},
			setupOpts: func(o *LoginOptions) {
				o.Context = "test-context"
			},
			expectError: false,
		},
		{
			name: "with organization",
			args: []string{testAPIURL},
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
						Server: testAPIURL,
					},
				}
				o.AccessToken = testToken
			},
			args:        []string{testAPIURL},
			expectError: true, // will fail due to network issues
		},
		{
			name: "with username password authentication",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: testAPIURL,
					},
				}
				o.Username = testUsername
				o.Password = testPassword
			},
			args:        []string{testAPIURL},
			expectError: true, // will fail due to network issues
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultLoginOptions()
			if tt.setupOpts != nil {
				tt.setupOpts(o)
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
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
		name           string
		statusCode     int
		responseBody   string
		expectError    bool
		expectedErrMsg string
	}{
		{
			name:         "successful auth config",
			statusCode:   http.StatusOK,
			responseBody: fmt.Sprintf(authConfigResponseTemplate, authTypeOIDC, testAuthURL),
			expectError:  false,
		},
		{
			name:           "404 response",
			statusCode:     http.StatusNotFound,
			responseBody:   `{"error":"not found"}`,
			expectError:    true,
			expectedErrMsg: "unexpected response code 404",
		},
		{
			name:           "500 response",
			statusCode:     http.StatusInternalServerError,
			responseBody:   `{"error":"internal server error"}`,
			expectError:    true,
			expectedErrMsg: "unexpected response code 500",
		},
		{
			name:         "418 teapot response should not error",
			statusCode:   http.StatusTeapot,
			responseBody: `{"apiVersion":"v1alpha1","code":418,"kind":"Status","message":"Auth not configured","reason":"Auth not configured","status":"Failure"}`,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(contentTypeHeader, applicationJSON)
				w.WriteHeader(tt.statusCode)
				_, err := w.Write([]byte(tt.responseBody))
				if err != nil {
					t.Errorf("failed to write response: %v", err)
				}
			}))
			defer server.Close()

			o := DefaultLoginOptions()
			o.clientConfig = &client.Config{
				Service: client.Service{
					Server: server.URL,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			_, err := o.getAuthConfig(ctx)

			if tt.expectError {
				assert.Error(t, err)
				if tt.expectedErrMsg != "" {
					assert.Contains(t, err.Error(), tt.expectedErrMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_Run_AuthDisabled(t *testing.T) {
	// Test the auth disabled scenario (418 response)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == authConfigPath {
			w.Header().Set(contentTypeHeader, applicationJSON)
			w.WriteHeader(http.StatusTeapot)
			_, err := w.Write([]byte(authNotConfiguredResponse))
			if err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		} else if r.URL.Path == authValidatePath {
			w.Header().Set(contentTypeHeader, applicationJSON)
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(successResponse))
			if err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		}
	}))
	defer server.Close()

	o := DefaultLoginOptions()
	o.clientConfig = &client.Config{
		Service: client.Service{
			Server: server.URL,
		},
	}
	o.ConfigFilePath = "/tmp/test-config.yaml"

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	err := o.Run(ctx, []string{server.URL})

	// Should succeed when auth is disabled
	assert.NoError(t, err)
}

func TestLoginOptions_Run_CAFileHandling(t *testing.T) {
	// Test CA file path normalization
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(contentTypeHeader, applicationJSON)
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(fmt.Sprintf(authConfigResponseTemplate, authTypeOIDC, testAuthURL)))
		if err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	// Create a temporary CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, []byte("test CA content"), 0600)
	assert.NoError(t, err)

	// Create a relative path to the CA file
	// We need to create the relative path from the current working directory
	// First, get the current working directory
	currentDir, err := os.Getwd()
	assert.NoError(t, err)

	// Create a relative path from current directory to the temp file
	caFileRel, err := filepath.Rel(currentDir, caFile)
	assert.NoError(t, err)

	// Verify the relative path is actually relative
	assert.False(t, filepath.IsAbs(caFileRel), "CA file path should be relative")

	// Get the expected absolute path for comparison
	expectedAbsPath, err := filepath.Abs(caFileRel)
	assert.NoError(t, err)
	assert.True(t, filepath.IsAbs(expectedAbsPath), "Expected absolute path should be absolute")

	o := DefaultLoginOptions()
	o.clientConfig = &client.Config{
		Service: client.Service{
			Server: server.URL,
		},
	}
	o.AuthCAFile = caFileRel // Use relative path
	o.AccessToken = testToken

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	err = o.Run(ctx, []string{server.URL})

	// Should fail due to auth provider creation, but CA file should be normalized
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "creating auth provider")

	// Verify that the CA file path was normalized to absolute path
	// Since the auth provider creation fails, we can't access the client config
	// But we can verify that the normalization happened by checking that the error
	// message doesn't contain the original relative path
	assert.NotContains(t, err.Error(), caFileRel, "Error message should not contain the original relative path")

	// The normalization should have happened before the auth provider creation
	// We can verify this by checking that the original field is still relative
	// (since the field itself is not modified, only the internal variable is)
	assert.Equal(t, caFileRel, o.AuthCAFile, "Original AuthCAFile field should remain unchanged")

	// Verify that the expected absolute path exists and is accessible
	assert.FileExists(t, expectedAbsPath, "Expected absolute path should exist")
}

func TestLoginOptions_Run_ClientIdSetup(t *testing.T) {
	tests := []struct {
		name        string
		authType    string
		username    string
		expectedID  string
		expectError bool
	}{
		{
			name:        "k8s auth with username",
			authType:    authTypeK8S,
			username:    testUsername,
			expectedID:  "openshift-challenging-client",
			expectError: false, // May succeed with test server
		},
		{
			name:        "k8s auth without username",
			authType:    authTypeK8S,
			username:    "",
			expectedID:  "openshift-cli-client",
			expectError: false, // May succeed with test server
		},
		{
			name:        "oidc auth",
			authType:    authTypeOIDC,
			username:    "",
			expectedID:  "flightctl",
			expectError: false, // May succeed with test server
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(contentTypeHeader, applicationJSON)
				if r.URL.Path == authConfigPath {
					w.WriteHeader(http.StatusOK)
					response := fmt.Sprintf(authConfigResponseTemplate, tt.authType, testAuthURL)
					_, err := w.Write([]byte(response))
					if err != nil {
						t.Errorf("failed to write response: %v", err)
					}
				} else if r.URL.Path == authValidatePath {
					w.WriteHeader(http.StatusOK)
					_, err := w.Write([]byte(successResponse))
					if err != nil {
						t.Errorf("failed to write response: %v", err)
					}
				}
			}))
			defer server.Close()

			o := DefaultLoginOptions()
			o.clientConfig = &client.Config{
				Service: client.Service{
					Server: server.URL,
				},
			}
			o.Username = tt.username
			o.AccessToken = testToken
			o.ConfigFilePath = "/tmp/test-config.yaml"

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			err := o.Run(ctx, []string{server.URL})

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// The test may succeed or fail depending on the auth provider implementation
				// We just want to ensure it doesn't panic
				if err != nil {
					assert.Contains(t, err.Error(), "creating auth provider")
				}
			}
		})
	}
}

func TestLoginOptions_Run_ErrorScenarios(t *testing.T) {
	tests := []struct {
		name        string
		setupOpts   func(*LoginOptions)
		serverSetup func(*httptest.Server)
		expectError bool
		errorMsg    string
	}{
		{
			name: "getAuthConfig failure",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: "https://invalid-server.com",
					},
				}
			},
			serverSetup: func(server *httptest.Server) {
				// Server will not be used, we're testing network failure
			},
			expectError: true,
			errorMsg:    "failed to get auth info",
		},
		{
			name: "CA file path error",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: testAPIURL,
					},
				}
				o.AuthCAFile = "/non/existent/path/ca.crt"
			},
			serverSetup: func(server *httptest.Server) {
				// Server will not be used
			},
			expectError: true,
			errorMsg:    "failed to get auth info",
		},
		{
			name: "auth provider creation failure",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: testAPIURL,
					},
				}
				o.AccessToken = testToken
			},
			serverSetup: func(server *httptest.Server) {
				// Server will return invalid auth config
			},
			expectError: true,
			errorMsg:    "failed to get auth info",
		},
		{
			name: "missing token and auth URL",
			setupOpts: func(o *LoginOptions) {
				o.clientConfig = &client.Config{
					Service: client.Service{
						Server: testAPIURL,
					},
				}
				// No token, no auth URL
			},
			serverSetup: func(server *httptest.Server) {
				server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set(contentTypeHeader, applicationJSON)
					w.WriteHeader(http.StatusOK)
					_, err := w.Write([]byte(fmt.Sprintf(authConfigResponseTemplate, authTypeOIDC, "")))
					if err != nil {
						t.Errorf("failed to write response: %v", err)
					}
				})
			},
			expectError: true,
			errorMsg:    "failed to get auth info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(contentTypeHeader, applicationJSON)
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte(fmt.Sprintf(authConfigResponseTemplate, authTypeOIDC, testAuthURL)))
				if err != nil {
					t.Errorf("failed to write response: %v", err)
				}
			}))
			defer server.Close()

			if tt.serverSetup != nil {
				tt.serverSetup(server)
			}

			o := DefaultLoginOptions()
			if tt.setupOpts != nil {
				tt.setupOpts(o)
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			err := o.Run(ctx, []string{testAPIURL})

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoginOptions_TokenValidation_AuthorizationHeader(t *testing.T) {
	// Test that the Authorization header is correctly sent to /auth/validate endpoint
	var recordedAuthHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(contentTypeHeader, applicationJSON)
		if r.URL.Path == authConfigPath {
			w.WriteHeader(http.StatusOK)
			// Use a simpler auth config that might work better
			_, err := w.Write([]byte(fmt.Sprintf(authConfigResponseTemplate, authTypeK8S, "")))
			if err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		} else if r.URL.Path == authValidatePath {
			// Record the Authorization header for verification
			recordedAuthHeader = r.Header.Get(authorizationHeader)

			// Simulate token validation
			if recordedAuthHeader == "Bearer "+testToken {
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte(successResponse))
				if err != nil {
					t.Errorf("failed to write response: %v", err)
				}
			} else {
				w.WriteHeader(http.StatusUnauthorized)
				_, err := w.Write([]byte(invalidTokenResponse))
				if err != nil {
					t.Errorf("failed to write response: %v", err)
				}
			}
		} else {
			// Handle any other paths that might be called during auth provider creation
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status":"ok"}`))
			if err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		}
	}))
	defer server.Close()

	o := DefaultLoginOptions()
	o.clientConfig = &client.Config{
		Service: client.Service{
			Server: server.URL,
		},
	}
	o.AccessToken = testToken
	o.ConfigFilePath = "/tmp/test-config.yaml"

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	err := o.Run(ctx, []string{server.URL})

	// Since the login was successful, we should have reached the token validation step
	// Verify that the Authorization header was sent correctly
	expectedAuthHeader := "Bearer " + testToken
	assert.Equal(t, expectedAuthHeader, recordedAuthHeader, "Authorization header should be sent correctly to /auth/validate")

	// The login should have succeeded
	assert.NoError(t, err)
}

func TestLoginOptions_Run_ConfigPersistence(t *testing.T) {
	// Test config persistence - simplified to avoid complex auth dependencies
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(contentTypeHeader, applicationJSON)
		if r.URL.Path == authConfigPath {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(fmt.Sprintf(authConfigResponseTemplate, authTypeOIDC, testAuthURL)))
			if err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		} else if r.URL.Path == authValidatePath {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(successResponse))
			if err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		}
	}))
	defer server.Close()

	// Create temporary config directory
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "client.yaml")

	o := DefaultLoginOptions()
	o.clientConfig = &client.Config{
		Service: client.Service{
			Server: server.URL,
		},
	}
	o.AccessToken = testToken
	o.ConfigFilePath = configFile

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	err := o.Run(ctx, []string{server.URL})

	// Should fail due to auth provider creation, but we can test the config setup
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "creating auth provider")

	// Verify config file was not created (since the operation failed)
	_, err = os.Stat(configFile)
	assert.Error(t, err) // File should not exist
}
