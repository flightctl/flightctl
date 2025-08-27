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
			wantErr: false,
		},
		{
			name:    "URL with extra path component and port",
			url:     "https://api.example.com:8443/devicemanagement/devices",
			wantErr: false,
		},
		{
			name:    "HTTP instead of HTTPS",
			url:     "http://api.example.com",
			wantErr: false,
		},
		{
			name:    "uppercase HTTPS scheme",
			url:     "HTTPS://api.example.com",
			wantErr: false,
		},
		{
			name:    "uppercase HTTP scheme",
			url:     "HTTP://api.example.com",
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
			errMsg:  "api.example.com is missing the protocol. Please ensure the API URL starts with 'http://' or 'https://'",
		},
		{
			name:    "invalid URL",
			url:     "not-a-url",
			wantErr: true,
			errMsg:  "not-a-url is missing the protocol. Please ensure the API URL starts with 'http://' or 'https://'",
		},
		{
			name:    "invalid protocol ftp",
			url:     "ftp://api.example.com",
			wantErr: true,
			errMsg:  "ftp://api.example.com is missing the protocol. Please ensure the API URL starts with 'http://' or 'https://'",
		},
		{
			name:    "invalid protocol ssh",
			url:     "ssh://api.example.com",
			wantErr: true,
			errMsg:  "ssh://api.example.com is missing the protocol. Please ensure the API URL starts with 'http://' or 'https://'",
		},
		{
			name:    "invalid protocol telnet",
			url:     "telnet://api.example.com",
			wantErr: true,
			errMsg:  "telnet://api.example.com is missing the protocol. Please ensure the API URL starts with 'http://' or 'https://'",
		},
		{
			name:    "missing hostname but port present",
			url:     "https://:8443",
			wantErr: true,
			errMsg:  "https://:8443 is missing a valid hostname. Please provide a complete URL with hostname",
		},
		{
			name:    "URL with double slashes in hostname",
			url:     "https://api//example.com",
			wantErr: false,
		},
		{
			name:    "URL with embedded credentials (userinfo)",
			url:     "https://user:pass@api.example.com",
			wantErr: false,
		},
		{
			name:    "URL with embedded credentials and port",
			url:     "https://user:pass@api.example.com:8443",
			wantErr: false,
		},
		{
			name:    "URL with embedded credentials and path",
			url:     "https://user:pass@api.example.com/api/v1",
			wantErr: false,
		},
		{
			name:    "URL with embedded credentials and query parameters",
			url:     "https://user:pass@api.example.com?param=value",
			wantErr: false,
		},
		{
			name:    "URL with embedded credentials and fragment",
			url:     "https://user:pass@api.example.com#section",
			wantErr: false,
		},
		{
			name:    "empty hostname",
			url:     "https://",
			wantErr: true,
			errMsg:  "https:// is missing a valid hostname. Please provide a complete URL with hostname",
		},

		{
			name:    "URL with query parameters",
			url:     "https://api.example.com?foo=bar&baz=qux",
			wantErr: false,
		},
		{
			name:    "URL with fragment",
			url:     "https://api.example.com#section",
			wantErr: false,
		},
		{
			name:    "URL with query parameters and fragment",
			url:     "https://api.example.com?foo=bar#section",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with port",
			url:     "https://[2001:db8::1]:8443",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with path component",
			url:     "https://[2001:db8::1]:8443/api/v1",
			wantErr: false,
		},
		{
			name:    "IPv6 URL with query parameters",
			url:     "https://[2001:db8::1]:8443?param=value",
			wantErr: false,
		},
		{
			name:    "IPv6 with port and extra path",
			url:     "https://[2001:db8::1]:8443/devices",
			wantErr: false,
		},
		{
			name:    "IPv6 URL without port with path",
			url:     "https://[2001:db8::1]/api/v1",
			wantErr: false,
		},
		{
			name:    "IPv6 URL without port with query",
			url:     "https://[2001:db8::1]?param=value",
			wantErr: false,
		},
		{
			name:    "IPv6 URL without port with fragment",
			url:     "https://[2001:db8::1]#section",
			wantErr: false,
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
