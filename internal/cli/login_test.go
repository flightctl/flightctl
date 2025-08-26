package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoginOptions_Validate(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid URL",
			url:     "https://api.example.com",
			wantErr: false,
		},
		{
			name:    "valid URL with port",
			url:     "https://api.example.com:8443",
			wantErr: false,
		},
		{
			name:    "URL with extra path component",
			url:     "https://api.example.com/devicemanagement/devices",
			wantErr: true,
			errMsg:  "API URL contains an unexpected path component '/devicemanagement/devices'. The API URL should only contain the hostname and optionally a port. Try: https://api.example.com",
		},
		{
			name:    "URL with extra path component and port",
			url:     "https://api.example.com:8443/devicemanagement/devices",
			wantErr: true,
			errMsg:  "API URL contains an unexpected path component '/devicemanagement/devices'. The API URL should only contain the hostname and optionally a port. Try: https://api.example.com:8443",
		},
		{
			name:    "HTTP instead of HTTPS",
			url:     "http://api.example.com",
			wantErr: true,
			errMsg:  "the API URL must use HTTPS for secure communication. Please ensure the API URL starts with 'https://' and try again",
		},
		{
			name:    "uppercase HTTPS scheme",
			url:     "HTTPS://api.example.com",
			wantErr: false,
		},
		{
			name:    "leading/trailing whitespace",
			url:     "   https://api.example.com  ",
			wantErr: false,
		},
		{
			name:    "missing protocol",
			url:     "api.example.com",
			wantErr: true,
			errMsg:  "API URL is missing the protocol. Please ensure the API URL starts with 'https://'",
		},
		{
			name:    "invalid URL",
			url:     "not-a-url",
			wantErr: true,
			errMsg:  "API URL is missing the protocol. Please ensure the API URL starts with 'https://'",
		},
		{
			name:    "missing hostname but port present",
			url:     "https://:8443",
			wantErr: true,
			errMsg:  "API URL is missing a valid hostname. Please provide a complete URL with hostname",
		},
		{
			name:    "URL with double slashes in hostname",
			url:     "https://api//example.com",
			wantErr: true,
			errMsg:  "API URL contains an unexpected path component '//example.com'. The API URL should only contain the hostname and optionally a port. Try: https://api",
		},
		{
			name:    "URL with embedded credentials (userinfo)",
			url:     "https://user:pass@api.example.com",
			wantErr: true,
			errMsg:  "must not include username or password",
		},
		{
			name:    "empty hostname",
			url:     "https://",
			wantErr: true,
			errMsg:  "API URL is missing a valid hostname. Please provide a complete URL with hostname",
		},
		{
			name:    "URL with only port",
			url:     "https://:8443",
			wantErr: true,
			errMsg:  "API URL is missing a valid hostname. Please provide a complete URL with hostname",
		},
		{
			name:    "URL with query parameters",
			url:     "https://api.example.com?foo=bar&baz=qux",
			wantErr: true,
			errMsg:  "API URL contains unexpected query parameters '?foo=bar&baz=qux'. The API URL should only contain the hostname and optionally a port. Try: https://api.example.com",
		},
		{
			name:    "URL with fragment",
			url:     "https://api.example.com#section",
			wantErr: true,
			errMsg:  "API URL contains an unexpected fragment '#section'. The API URL should only contain the hostname and optionally a port. Try: https://api.example.com",
		},
		{
			name:    "URL with query parameters and fragment",
			url:     "https://api.example.com?foo=bar#section",
			wantErr: true,
			errMsg:  "API URL contains unexpected query parameters '?foo=bar'. The API URL should only contain the hostname and optionally a port. Try: https://api.example.com",
		},
		{
			name:    "IPv6 URL with port",
			url:     "https://[2001:db8::1]:8443",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with path component",
			url:     "https://[2001:db8::1]:8443/api/v1",
			wantErr: true,
			errMsg:  "API URL contains an unexpected path component '/api/v1'. The API URL should only contain the hostname and optionally a port. Try: https://[2001:db8::1]:8443",
		},
		{
			name:    "IPv6 URL with query parameters",
			url:     "https://[2001:db8::1]:8443?param=value",
			wantErr: true,
			errMsg:  "API URL contains unexpected query parameters '?param=value'. The API URL should only contain the hostname and optionally a port. Try: https://[2001:db8::1]:8443",
		},
		{
			name:    "IPv6 with port and extra path (corrected URL should preserve brackets)",
			url:     "https://[2001:db8::1]:8443/devices",
			wantErr: true,
			errMsg:  "Try: https://[2001:db8::1]:8443",
		},
		{
			name:    "trailing slash is allowed",
			url:     "https://api.example.com/",
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
