package v1alpha1

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthProviderHideSensitiveData(t *testing.T) {
	tests := []struct {
		name              string
		setupProvider     func() *AuthProvider
		expectedType      string
		checkSecretHidden bool
	}{
		{
			name: "OIDC provider type preserved",
			setupProvider: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				staticAssignment := AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				}
				err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment)
				if err != nil {
					panic(err) // Should never happen in setup
				}

				oidcSpec := OIDCProviderSpec{
					ProviderType:           Oidc,
					Issuer:                 "https://oidc.example.com",
					ClientId:               "oidc-client-id",
					ClientSecret:           lo.ToPtr("oidc-secret"),
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}

				provider := &AuthProvider{
					Metadata: ObjectMeta{
						Name: lo.ToPtr("oidc-provider"),
					},
				}
				err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
				if err != nil {
					panic(err) // Should never happen in setup
				}
				return provider
			},
			expectedType:      string(Oidc),
			checkSecretHidden: true,
		},
		{
			name: "OAuth2 provider type preserved",
			setupProvider: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				staticAssignment := AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				}
				err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment)
				if err != nil {
					panic(err) // Should never happen in setup
				}

				oauth2Spec := OAuth2ProviderSpec{
					ProviderType:           OAuth2ProviderSpecProviderTypeOauth2,
					Issuer:                 lo.ToPtr("https://oauth2.example.com"),
					ClientId:               "oauth2-client-id",
					ClientSecret:           lo.ToPtr("oauth2-secret"),
					AuthorizationUrl:       "https://oauth2.example.com/authorize",
					TokenUrl:               "https://oauth2.example.com/token",
					UserinfoUrl:            "https://oauth2.example.com/userinfo",
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}

				provider := &AuthProvider{
					Metadata: ObjectMeta{
						Name: lo.ToPtr("oauth2-provider"),
					},
				}
				err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
				if err != nil {
					panic(err) // Should never happen in setup
				}
				return provider
			},
			expectedType:      string(OAuth2ProviderSpecProviderTypeOauth2),
			checkSecretHidden: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.setupProvider()

			// Hide sensitive data
			err := provider.HideSensitiveData()
			require.NoError(t, err)

			// Check that discriminator is preserved
			discriminator, err := provider.Spec.Discriminator()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedType, discriminator, "Provider type should be preserved")

			// Check that secret is hidden
			if tt.checkSecretHidden {
				switch discriminator {
				case string(Oidc):
					oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
					require.NoError(t, err)
					require.NotNil(t, oidcSpec.ClientSecret)
					assert.Equal(t, "*****", *oidcSpec.ClientSecret, "OIDC client secret should be hidden")
				case string(OAuth2ProviderSpecProviderTypeOauth2):
					oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
					require.NoError(t, err)
					require.NotNil(t, oauth2Spec.ClientSecret)
					assert.Equal(t, "*****", *oauth2Spec.ClientSecret, "OAuth2 client secret should be hidden")
				}
			}
		})
	}
}

func TestAuthProviderListHideSensitiveData(t *testing.T) {
	assignment := AuthOrganizationAssignment{}
	staticAssignment := AuthStaticOrganizationAssignment{
		Type:             AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "test-org",
	}
	err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment)
	require.NoError(t, err)

	// Create OIDC provider
	oidcSpec := OIDCProviderSpec{
		ProviderType:           Oidc,
		Issuer:                 "https://oidc.example.com",
		ClientId:               "oidc-client-id",
		ClientSecret:           lo.ToPtr("oidc-secret"),
		Enabled:                lo.ToPtr(true),
		OrganizationAssignment: assignment,
	}
	oidcProvider := AuthProvider{
		Metadata: ObjectMeta{
			Name: lo.ToPtr("oidc-provider"),
		},
	}
	err = oidcProvider.Spec.FromOIDCProviderSpec(oidcSpec)
	require.NoError(t, err)

	// Create OAuth2 provider
	oauth2Spec := OAuth2ProviderSpec{
		ProviderType:           OAuth2ProviderSpecProviderTypeOauth2,
		Issuer:                 lo.ToPtr("https://oauth2.example.com"),
		ClientId:               "oauth2-client-id",
		ClientSecret:           lo.ToPtr("oauth2-secret"),
		AuthorizationUrl:       "https://oauth2.example.com/authorize",
		TokenUrl:               "https://oauth2.example.com/token",
		UserinfoUrl:            "https://oauth2.example.com/userinfo",
		Enabled:                lo.ToPtr(true),
		OrganizationAssignment: assignment,
	}
	oauth2Provider := AuthProvider{
		Metadata: ObjectMeta{
			Name: lo.ToPtr("oauth2-provider"),
		},
	}
	err2 := oauth2Provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
	require.NoError(t, err2)

	// Create list
	list := &AuthProviderList{
		Items: []AuthProvider{oidcProvider, oauth2Provider},
	}

	// Hide sensitive data
	err = list.HideSensitiveData()
	require.NoError(t, err)

	// Verify OIDC provider
	oidcDiscriminator, err := list.Items[0].Spec.Discriminator()
	require.NoError(t, err)
	assert.Equal(t, string(Oidc), oidcDiscriminator, "OIDC provider type should be preserved")

	oidcSpec, err = list.Items[0].Spec.AsOIDCProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, oidcSpec.ClientSecret)
	assert.Equal(t, "*****", *oidcSpec.ClientSecret, "OIDC client secret should be hidden")

	// Verify OAuth2 provider
	oauth2Discriminator, err := list.Items[1].Spec.Discriminator()
	require.NoError(t, err)
	assert.Equal(t, string(OAuth2ProviderSpecProviderTypeOauth2), oauth2Discriminator, "OAuth2 provider type should be preserved")

	oauth2Spec, err = list.Items[1].Spec.AsOAuth2ProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, oauth2Spec.ClientSecret)
	assert.Equal(t, "*****", *oauth2Spec.ClientSecret, "OAuth2 client secret should be hidden")
}
