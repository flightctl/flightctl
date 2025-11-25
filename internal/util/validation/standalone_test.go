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
	assert.Contains(t, errs[0].Error(), "must be set")
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
			BaseDomain: "", // Error 1: Missing baseDomain
			Auth: standalone.AuthConfig{
				Type: "invalid", // Error 2: Invalid auth type
			},
		},
	}

	errs := ValidateStandaloneConfig(config)
	assert.Len(t, errs, 2, "should have 2 validation errors")

	allErrors := errs[0].Error() + errs[1].Error()
	assert.Contains(t, allErrors, "baseDomain")
	assert.Contains(t, allErrors, "auth.type")
}
