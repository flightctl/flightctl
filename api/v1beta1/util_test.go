package v1beta1

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
					ProviderType:           Oauth2,
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
			expectedType:      string(Oauth2),
			checkSecretHidden: true,
		},
		{
			name: "OpenShift provider type preserved",
			setupProvider: func() *AuthProvider {
				openshiftSpec := OpenShiftProviderSpec{
					ProviderType:           Openshift,
					Issuer:                 lo.ToPtr("https://openshift.example.com"),
					ClientId:               lo.ToPtr("openshift-client-id"),
					ClientSecret:           lo.ToPtr("openshift-secret"),
					AuthorizationUrl:       lo.ToPtr("https://openshift.example.com/authorize"),
					TokenUrl:               lo.ToPtr("https://openshift.example.com/token"),
					ClusterControlPlaneUrl: lo.ToPtr("https://api.openshift.example.com"),
					Enabled:                lo.ToPtr(true),
				}

				provider := &AuthProvider{
					Metadata: ObjectMeta{
						Name: lo.ToPtr("openshift-provider"),
					},
				}
				err := provider.Spec.FromOpenShiftProviderSpec(openshiftSpec)
				if err != nil {
					panic(err) // Should never happen in setup
				}
				return provider
			},
			expectedType:      string(Openshift),
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
				case string(Oauth2):
					oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
					require.NoError(t, err)
					require.NotNil(t, oauth2Spec.ClientSecret)
					assert.Equal(t, "*****", *oauth2Spec.ClientSecret, "OAuth2 client secret should be hidden")
				case string(Openshift):
					openshiftSpec, err := provider.Spec.AsOpenShiftProviderSpec()
					require.NoError(t, err)
					require.NotNil(t, openshiftSpec.ClientSecret)
					assert.Equal(t, "*****", *openshiftSpec.ClientSecret, "OpenShift client secret should be hidden")
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
		ProviderType:           Oauth2,
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

	// Create OpenShift provider
	openshiftSpec := OpenShiftProviderSpec{
		ProviderType:           Openshift,
		Issuer:                 lo.ToPtr("https://openshift.example.com"),
		ClientId:               lo.ToPtr("openshift-client-id"),
		ClientSecret:           lo.ToPtr("openshift-secret"),
		AuthorizationUrl:       lo.ToPtr("https://openshift.example.com/authorize"),
		TokenUrl:               lo.ToPtr("https://openshift.example.com/token"),
		ClusterControlPlaneUrl: lo.ToPtr("https://api.openshift.example.com"),
		Enabled:                lo.ToPtr(true),
	}
	openshiftProvider := AuthProvider{
		Metadata: ObjectMeta{
			Name: lo.ToPtr("openshift-provider"),
		},
	}
	err3 := openshiftProvider.Spec.FromOpenShiftProviderSpec(openshiftSpec)
	require.NoError(t, err3)

	// Create list
	list := &AuthProviderList{
		Items: []AuthProvider{oidcProvider, oauth2Provider, openshiftProvider},
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
	assert.Equal(t, string(Oauth2), oauth2Discriminator, "OAuth2 provider type should be preserved")

	oauth2Spec, err = list.Items[1].Spec.AsOAuth2ProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, oauth2Spec.ClientSecret)
	assert.Equal(t, "*****", *oauth2Spec.ClientSecret, "OAuth2 client secret should be hidden")

	// Verify OpenShift provider
	openshiftDiscriminator, err := list.Items[2].Spec.Discriminator()
	require.NoError(t, err)
	assert.Equal(t, string(Openshift), openshiftDiscriminator, "OpenShift provider type should be preserved")

	openshiftSpec, err = list.Items[2].Spec.AsOpenShiftProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, openshiftSpec.ClientSecret)
	assert.Equal(t, "*****", *openshiftSpec.ClientSecret, "OpenShift client secret should be hidden")
}

func TestAuthConfigHideSensitiveData(t *testing.T) {
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
		ProviderType:           Oauth2,
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

	// Create OpenShift provider
	openshiftSpec := OpenShiftProviderSpec{
		ProviderType:           Openshift,
		Issuer:                 lo.ToPtr("https://openshift.example.com"),
		ClientId:               lo.ToPtr("openshift-client-id"),
		ClientSecret:           lo.ToPtr("openshift-secret"),
		AuthorizationUrl:       lo.ToPtr("https://openshift.example.com/authorize"),
		TokenUrl:               lo.ToPtr("https://openshift.example.com/token"),
		ClusterControlPlaneUrl: lo.ToPtr("https://api.openshift.example.com"),
		Enabled:                lo.ToPtr(true),
	}
	openshiftProvider := AuthProvider{
		Metadata: ObjectMeta{
			Name: lo.ToPtr("openshift-provider"),
		},
	}
	err3 := openshiftProvider.Spec.FromOpenShiftProviderSpec(openshiftSpec)
	require.NoError(t, err3)

	// Create AuthConfig with providers
	providers := []AuthProvider{oidcProvider, oauth2Provider, openshiftProvider}
	config := &AuthConfig{
		ApiVersion: "v1beta1",
		Providers:  &providers,
	}

	// Hide sensitive data
	err = config.HideSensitiveData()
	require.NoError(t, err)

	// Verify OIDC provider
	oidcDiscriminator, err := (*config.Providers)[0].Spec.Discriminator()
	require.NoError(t, err)
	assert.Equal(t, string(Oidc), oidcDiscriminator, "OIDC provider type should be preserved")

	oidcSpec, err = (*config.Providers)[0].Spec.AsOIDCProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, oidcSpec.ClientSecret)
	assert.Equal(t, "*****", *oidcSpec.ClientSecret, "OIDC client secret should be hidden")

	// Verify OAuth2 provider
	oauth2Discriminator, err := (*config.Providers)[1].Spec.Discriminator()
	require.NoError(t, err)
	assert.Equal(t, string(Oauth2), oauth2Discriminator, "OAuth2 provider type should be preserved")

	oauth2Spec, err = (*config.Providers)[1].Spec.AsOAuth2ProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, oauth2Spec.ClientSecret)
	assert.Equal(t, "*****", *oauth2Spec.ClientSecret, "OAuth2 client secret should be hidden")

	// Verify OpenShift provider
	openshiftDiscriminator, err := (*config.Providers)[2].Spec.Discriminator()
	require.NoError(t, err)
	assert.Equal(t, string(Openshift), openshiftDiscriminator, "OpenShift provider type should be preserved")

	openshiftSpec, err = (*config.Providers)[2].Spec.AsOpenShiftProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, openshiftSpec.ClientSecret)
	assert.Equal(t, "*****", *openshiftSpec.ClientSecret, "OpenShift client secret should be hidden")
}

func TestApplicationVolumeType(t *testing.T) {
	tests := []struct {
		name           string
		setupVolume    func(t *testing.T) ApplicationVolume
		expectedType   ApplicationVolumeProviderType
		expectError    bool
		errorSubstring string
	}{
		{
			name: "image only volume",
			setupVolume: func(t *testing.T) ApplicationVolume {
				var volume ApplicationVolume
				imageVolumeProvider := ImageVolumeProviderSpec{
					Image: ImageVolumeSource{
						Reference:  "quay.io/test/image:v1",
						PullPolicy: lo.ToPtr(PullIfNotPresent),
					},
				}
				err := volume.FromImageVolumeProviderSpec(imageVolumeProvider)
				require.NoError(t, err)
				return volume
			},
			expectedType: ImageApplicationVolumeProviderType,
			expectError:  false,
		},
		{
			name: "mount only volume",
			setupVolume: func(t *testing.T) ApplicationVolume {
				var volume ApplicationVolume
				mountVolumeProvider := MountVolumeProviderSpec{
					Mount: VolumeMount{
						Path: "/host/path:/container/path",
					},
				}
				err := volume.FromMountVolumeProviderSpec(mountVolumeProvider)
				require.NoError(t, err)
				return volume
			},
			expectedType: MountApplicationVolumeProviderType,
			expectError:  false,
		},
		{
			name: "image-mount volume",
			setupVolume: func(t *testing.T) ApplicationVolume {
				var volume ApplicationVolume
				imageMountVolumeProvider := ImageMountVolumeProviderSpec{
					Image: ImageVolumeSource{
						Reference:  "quay.io/test/image:v1",
						PullPolicy: lo.ToPtr(PullIfNotPresent),
					},
					Mount: VolumeMount{
						Path: "/host/path:/container/path",
					},
				}
				err := volume.FromImageMountVolumeProviderSpec(imageMountVolumeProvider)
				require.NoError(t, err)
				return volume
			},
			expectedType: ImageMountApplicationVolumeProviderType,
			expectError:  false,
		},
		{
			name: "empty volume",
			setupVolume: func(t *testing.T) ApplicationVolume {
				return ApplicationVolume{
					Name:  "empty-vol",
					union: []byte("{}"),
				}
			},
			expectedType:   "",
			expectError:    true,
			errorSubstring: "unable to determine application volume type",
		},
		{
			name: "invalid JSON in union",
			setupVolume: func(t *testing.T) ApplicationVolume {
				return ApplicationVolume{
					Name:  "invalid-vol",
					union: []byte("not valid json"),
				}
			},
			expectedType:   "",
			expectError:    true,
			errorSubstring: "invalid character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volume := tt.setupVolume(t)
			volumeType, err := volume.Type()

			if tt.expectError {
				require.Error(t, err)
				if tt.errorSubstring != "" {
					require.Contains(t, err.Error(), tt.errorSubstring)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedType, volumeType)
			}
		})
	}
}
