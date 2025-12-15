package validation

import (
	"testing"

	"github.com/flightctl/flightctl/internal/config/standalone"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStandaloneConfig_NilConfig(t *testing.T) {
	errs := ValidateStandaloneConfig(nil)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "config cannot be nil")
}

func TestValidateStandaloneConfig_MissingBaseDomain(t *testing.T) {
	config := &standalone.Config{
		Global: standalone.GlobalConfig{
			BaseDomain: "", // Missing
			Auth: standalone.AuthConfig{
				Type: standalone.AuthTypeNone,
			},
		},
	}

	errs := ValidateStandaloneConfig(config)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "global.baseDomain")
	assert.Contains(t, errs[0].Error(), "Required value")
}

func TestValidateStandaloneConfig_InvalidAuthType(t *testing.T) {
	testCases := []struct {
		name     string
		authType string
		wantErr  bool
	}{
		{
			name:     "valid oidc",
			authType: standalone.AuthTypeOIDC,
			wantErr:  false,
		},
		{
			name:     "valid aap",
			authType: standalone.AuthTypeAAP,
			wantErr:  false,
		},
		{
			name:     "valid oauth2",
			authType: standalone.AuthTypeOAuth2,
			wantErr:  false,
		},
		{
			name:     "valid none",
			authType: standalone.AuthTypeNone,
			wantErr:  false,
		},
		{
			name:     "invalid type",
			authType: "invalid",
			wantErr:  true,
		},
		{
			name:     "empty type",
			authType: "",
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := &standalone.Config{
				Global: standalone.GlobalConfig{
					BaseDomain: "example.com",
					Auth: standalone.AuthConfig{
						Type: tc.authType,
					},
				},
			}

			errs := ValidateStandaloneConfig(config)
			if tc.wantErr {
				require.NotEmpty(t, errs)
				assert.Contains(t, errs[0].Error(), "global.auth.type")
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidateStandaloneConfig_MultipleErrors(t *testing.T) {
	config := &standalone.Config{
		Global: standalone.GlobalConfig{
			BaseDomain: "", // Error 1 & 2: Missing baseDomain (Required + Invalid)
			Auth: standalone.AuthConfig{
				Type: "invalid", // Error 3: Invalid auth type
			},
		},
	}

	errs := ValidateStandaloneConfig(config)
	assert.Len(t, errs, 3, "should have 3 validation errors (2 for empty baseDomain, 1 for invalid auth type)")

	allErrors := errs[0].Error() + errs[1].Error() + errs[2].Error()
	assert.Contains(t, allErrors, "baseDomain")
	assert.Contains(t, allErrors, "auth")
}

func TestValidateFQDN(t *testing.T) {
	testCases := []struct {
		name    string
		fqdn    string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid simple FQDN",
			fqdn:    "example.com",
			wantErr: false,
		},
		{
			name:    "valid subdomain FQDN",
			fqdn:    "api.example.com",
			wantErr: false,
		},
		{
			name:    "valid multi-level FQDN",
			fqdn:    "my-api.services.example.com",
			wantErr: false,
		},
		{
			name:    "valid FQDN with numbers",
			fqdn:    "api1.example2.com",
			wantErr: false,
		},
		{
			name:    "valid FQDN with hyphens",
			fqdn:    "my-api-server.my-domain.com",
			wantErr: false,
		},
		{
			name:    "empty string",
			fqdn:    "",
			wantErr: true,
			errMsg:  "Required value",
		},
		{
			name:    "simple hostname without dot",
			fqdn:    "localhost",
			wantErr: false,
		},
		{
			name:    "simple hostname with hyphen",
			fqdn:    "my-host",
			wantErr: false,
		},
		{
			name:    "simple hostname with numbers",
			fqdn:    "host123",
			wantErr: false,
		},
		{
			name:    "simple hostname alphanumeric",
			fqdn:    "web01",
			wantErr: false,
		},
		{
			name:    "IPv4 address",
			fqdn:    "192.168.1.1",
			wantErr: true,
			errMsg:  "Invalid value",
		},
		{
			name:    "IPv6 address",
			fqdn:    "2001:db8::1",
			wantErr: true,
			errMsg:  "Invalid value",
		},
		{
			name:    "underscore in hostname",
			fqdn:    "invalid_host.example.com",
			wantErr: true,
			errMsg:  "Invalid value",
		},
		{
			name:    "starts with hyphen",
			fqdn:    "-invalid.example.com",
			wantErr: true,
			errMsg:  "Invalid value",
		},
		{
			name:    "ends with hyphen",
			fqdn:    "invalid-.example.com",
			wantErr: true,
			errMsg:  "Invalid value",
		},
		{
			name:    "ends with dot",
			fqdn:    "example.com.",
			wantErr: true,
			errMsg:  "Invalid value",
		},
		{
			name:    "double dot",
			fqdn:    "example..com",
			wantErr: true,
			errMsg:  "Invalid value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := &standalone.Config{
				Global: standalone.GlobalConfig{
					BaseDomain: tc.fqdn,
					Auth: standalone.AuthConfig{
						Type: standalone.AuthTypeNone,
					},
				},
			}

			errs := ValidateStandaloneConfig(config)
			if tc.wantErr {
				require.NotEmpty(t, errs, "expected validation error for FQDN: %s", tc.fqdn)
				assert.Contains(t, errs[0].Error(), tc.errMsg)
			} else {
				assert.Empty(t, errs, "expected no validation error for FQDN: %s", tc.fqdn)
			}
		})
	}
}
