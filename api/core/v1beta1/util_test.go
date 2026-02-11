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

func TestRepositoryHideSensitiveData(t *testing.T) {
	tests := []struct {
		name            string
		setupRepository func() *Repository
		checkHidden     func(t *testing.T, repo *Repository)
	}{
		{
			name: "OCI repository hides password",
			setupRepository: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromOciRepoSpec(OciRepoSpec{
					Registry: "quay.io",
					Type:     OciRepoSpecTypeOci,
					OciAuth:  newOciAuth("myuser", "mysecretpassword"),
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("oci-repo")},
					Spec:     spec,
				}
			},
			checkHidden: func(t *testing.T, repo *Repository) {
				ociSpec, err := repo.Spec.AsOciRepoSpec()
				require.NoError(t, err)
				assert.Equal(t, "quay.io", ociSpec.Registry)
				require.NotNil(t, ociSpec.OciAuth)
				dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
				require.NoError(t, err)
				assert.Equal(t, "myuser", dockerAuth.Username)
				assert.Equal(t, "*****", dockerAuth.Password)
			},
		},
		{
			name: "HTTP repository hides password",
			setupRepository: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromHttpRepoSpec(HttpRepoSpec{
					Url:  "https://example.com/repo",
					Type: HttpRepoSpecTypeHttp,
					HttpConfig: &HttpConfig{
						Username: lo.ToPtr("httpuser"),
						Password: lo.ToPtr("httppassword"),
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("http-repo")},
					Spec:     spec,
				}
			},
			checkHidden: func(t *testing.T, repo *Repository) {
				httpSpec, err := repo.Spec.AsHttpRepoSpec()
				require.NoError(t, err)
				assert.Equal(t, "https://example.com/repo", httpSpec.Url)
				assert.Equal(t, "httpuser", *httpSpec.HttpConfig.Username)
				assert.Equal(t, "*****", *httpSpec.HttpConfig.Password)
			},
		},
		{
			name: "SSH repository hides private key",
			setupRepository: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromGitRepoSpec(GitRepoSpec{
					Url:  "git@github.com:org/repo.git",
					Type: GitRepoSpecTypeGit,
					SshConfig: &SshConfig{
						SshPrivateKey:        lo.ToPtr("-----BEGIN RSA PRIVATE KEY-----"),
						PrivateKeyPassphrase: lo.ToPtr("keypassphrase"),
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("ssh-repo")},
					Spec:     spec,
				}
			},
			checkHidden: func(t *testing.T, repo *Repository) {
				gitSpec, err := repo.Spec.AsGitRepoSpec()
				require.NoError(t, err)
				assert.Equal(t, "git@github.com:org/repo.git", gitSpec.Url)
				assert.Equal(t, "*****", *gitSpec.SshConfig.SshPrivateKey)
				assert.Equal(t, "*****", *gitSpec.SshConfig.PrivateKeyPassphrase)
			},
		},
		{
			name: "Git repository without auth has no sensitive data",
			setupRepository: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromGitRepoSpec(GitRepoSpec{
					Url:  "https://github.com/org/repo",
					Type: GitRepoSpecTypeGit,
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("generic-repo")},
					Spec:     spec,
				}
			},
			checkHidden: func(t *testing.T, repo *Repository) {
				gitSpec, err := repo.Spec.AsGitRepoSpec()
				require.NoError(t, err)
				assert.Equal(t, "https://github.com/org/repo", gitSpec.Url)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := tt.setupRepository()
			err := repo.HideSensitiveData()
			require.NoError(t, err)
			tt.checkHidden(t, repo)
		})
	}
}

func TestRepositoryListHideSensitiveData(t *testing.T) {
	// Create OCI repository with password
	ociSpec := RepositorySpec{}
	err := ociSpec.FromOciRepoSpec(OciRepoSpec{
		Registry: "quay.io",
		Type:     OciRepoSpecTypeOci,
		OciAuth:  newOciAuth("ociuser", "ocisecret"),
	})
	require.NoError(t, err)
	ociRepo := Repository{
		Metadata: ObjectMeta{Name: lo.ToPtr("oci-repo")},
		Spec:     ociSpec,
	}

	// Create HTTP repository with password
	httpSpec := RepositorySpec{}
	err = httpSpec.FromHttpRepoSpec(HttpRepoSpec{
		Url:  "https://example.com/repo",
		Type: HttpRepoSpecTypeHttp,
		HttpConfig: &HttpConfig{
			Username: lo.ToPtr("httpuser"),
			Password: lo.ToPtr("httpsecret"),
		},
	})
	require.NoError(t, err)
	httpRepo := Repository{
		Metadata: ObjectMeta{Name: lo.ToPtr("http-repo")},
		Spec:     httpSpec,
	}

	// Create SSH repository with private key
	sshSpec := RepositorySpec{}
	err = sshSpec.FromGitRepoSpec(GitRepoSpec{
		Url:  "git@github.com:org/repo.git",
		Type: GitRepoSpecTypeGit,
		SshConfig: &SshConfig{
			SshPrivateKey:        lo.ToPtr("-----BEGIN RSA PRIVATE KEY-----"),
			PrivateKeyPassphrase: lo.ToPtr("keypassphrase"),
		},
	})
	require.NoError(t, err)
	sshRepo := Repository{
		Metadata: ObjectMeta{Name: lo.ToPtr("ssh-repo")},
		Spec:     sshSpec,
	}

	// Create list with all repository types
	list := &RepositoryList{
		Items: []Repository{ociRepo, httpRepo, sshRepo},
	}

	// Hide sensitive data
	err = list.HideSensitiveData()
	require.NoError(t, err)

	// Verify OCI repository password is hidden
	ociSpecResult, err := list.Items[0].Spec.AsOciRepoSpec()
	require.NoError(t, err)
	require.NotNil(t, ociSpecResult.OciAuth)
	dockerAuth, err := ociSpecResult.OciAuth.AsDockerAuth()
	require.NoError(t, err)
	assert.Equal(t, "ociuser", dockerAuth.Username)
	assert.Equal(t, "*****", dockerAuth.Password, "OCI password should be hidden in list")

	// Verify HTTP repository password is hidden
	httpSpecResult, err := list.Items[1].Spec.AsHttpRepoSpec()
	require.NoError(t, err)
	assert.Equal(t, "httpuser", *httpSpecResult.HttpConfig.Username)
	assert.Equal(t, "*****", *httpSpecResult.HttpConfig.Password, "HTTP password should be hidden in list")

	// Verify SSH repository private key is hidden
	sshSpecResult, err := list.Items[2].Spec.AsGitRepoSpec()
	require.NoError(t, err)
	assert.Equal(t, "*****", *sshSpecResult.SshConfig.SshPrivateKey, "SSH private key should be hidden in list")
	assert.Equal(t, "*****", *sshSpecResult.SshConfig.PrivateKeyPassphrase, "SSH passphrase should be hidden in list")
}

func TestRepositoryPreserveSensitiveData(t *testing.T) {
	tests := []struct {
		name           string
		setupExisting  func() *Repository
		setupNew       func() *Repository
		checkPreserved func(t *testing.T, repo *Repository)
	}{
		{
			name: "OCI repository preserves password when masked",
			setupExisting: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromOciRepoSpec(OciRepoSpec{
					Registry: "quay.io",
					Type:     OciRepoSpecTypeOci,
					OciAuth:  newOciAuth("myuser", "originalsecret"),
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("oci-repo")},
					Spec:     spec,
				}
			},
			setupNew: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromOciRepoSpec(OciRepoSpec{
					Registry: "quay.io",
					Type:     OciRepoSpecTypeOci,
					OciAuth:  newOciAuth("myuser", "*****"), // masked placeholder
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("oci-repo")},
					Spec:     spec,
				}
			},
			checkPreserved: func(t *testing.T, repo *Repository) {
				ociSpec, err := repo.Spec.AsOciRepoSpec()
				require.NoError(t, err)
				require.NotNil(t, ociSpec.OciAuth)
				dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
				require.NoError(t, err)
				assert.Equal(t, "originalsecret", dockerAuth.Password, "Password should be preserved from existing")
			},
		},
		{
			name: "HTTP repository preserves password when masked",
			setupExisting: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromHttpRepoSpec(HttpRepoSpec{
					Url:  "https://example.com/repo",
					Type: HttpRepoSpecTypeHttp,
					HttpConfig: &HttpConfig{
						Username: lo.ToPtr("httpuser"),
						Password: lo.ToPtr("httporiginal"),
						Token:    lo.ToPtr("tokenoriginal"),
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("http-repo")},
					Spec:     spec,
				}
			},
			setupNew: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromHttpRepoSpec(HttpRepoSpec{
					Url:  "https://example.com/repo",
					Type: HttpRepoSpecTypeHttp,
					HttpConfig: &HttpConfig{
						Username: lo.ToPtr("httpuser"),
						Password: lo.ToPtr("*****"), // masked placeholder
						Token:    lo.ToPtr("*****"), // masked placeholder
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("http-repo")},
					Spec:     spec,
				}
			},
			checkPreserved: func(t *testing.T, repo *Repository) {
				httpSpec, err := repo.Spec.AsHttpRepoSpec()
				require.NoError(t, err)
				assert.Equal(t, "httporiginal", *httpSpec.HttpConfig.Password, "Password should be preserved from existing")
				assert.Equal(t, "tokenoriginal", *httpSpec.HttpConfig.Token, "Token should be preserved from existing")
			},
		},
		{
			name: "SSH repository preserves private key when masked",
			setupExisting: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromGitRepoSpec(GitRepoSpec{
					Url:  "git@github.com:org/repo.git",
					Type: GitRepoSpecTypeGit,
					SshConfig: &SshConfig{
						SshPrivateKey:        lo.ToPtr("-----BEGIN RSA PRIVATE KEY-----"),
						PrivateKeyPassphrase: lo.ToPtr("originalpassphrase"),
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("ssh-repo")},
					Spec:     spec,
				}
			},
			setupNew: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromGitRepoSpec(GitRepoSpec{
					Url:  "git@github.com:org/repo.git",
					Type: GitRepoSpecTypeGit,
					SshConfig: &SshConfig{
						SshPrivateKey:        lo.ToPtr("*****"), // masked placeholder
						PrivateKeyPassphrase: lo.ToPtr("*****"), // masked placeholder
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("ssh-repo")},
					Spec:     spec,
				}
			},
			checkPreserved: func(t *testing.T, repo *Repository) {
				gitSpec, err := repo.Spec.AsGitRepoSpec()
				require.NoError(t, err)
				assert.Equal(t, "-----BEGIN RSA PRIVATE KEY-----", *gitSpec.SshConfig.SshPrivateKey, "SSH private key should be preserved from existing")
				assert.Equal(t, "originalpassphrase", *gitSpec.SshConfig.PrivateKeyPassphrase, "SSH passphrase should be preserved from existing")
			},
		},
		{
			name: "New password is used when not masked",
			setupExisting: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromHttpRepoSpec(HttpRepoSpec{
					Url:  "https://example.com/repo",
					Type: HttpRepoSpecTypeHttp,
					HttpConfig: &HttpConfig{
						Username: lo.ToPtr("httpuser"),
						Password: lo.ToPtr("oldpassword"),
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("http-repo")},
					Spec:     spec,
				}
			},
			setupNew: func() *Repository {
				spec := RepositorySpec{}
				err := spec.FromHttpRepoSpec(HttpRepoSpec{
					Url:  "https://example.com/repo",
					Type: HttpRepoSpecTypeHttp,
					HttpConfig: &HttpConfig{
						Username: lo.ToPtr("httpuser"),
						Password: lo.ToPtr("newpassword"), // actual new password
					},
				})
				if err != nil {
					panic(err)
				}
				return &Repository{
					Metadata: ObjectMeta{Name: lo.ToPtr("http-repo")},
					Spec:     spec,
				}
			},
			checkPreserved: func(t *testing.T, repo *Repository) {
				httpSpec, err := repo.Spec.AsHttpRepoSpec()
				require.NoError(t, err)
				assert.Equal(t, "newpassword", *httpSpec.HttpConfig.Password, "New password should be used")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing := tt.setupExisting()
			newRepo := tt.setupNew()
			err := newRepo.PreserveSensitiveData(existing)
			require.NoError(t, err)
			tt.checkPreserved(t, newRepo)
		})
	}
}

func TestAuthProviderPreserveSensitiveData(t *testing.T) {
	tests := []struct {
		name           string
		setupExisting  func() *AuthProvider
		setupNew       func() *AuthProvider
		checkPreserved func(t *testing.T, ap *AuthProvider)
	}{
		{
			name: "OIDC provider preserves client secret when masked",
			setupExisting: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				_ = assignment.FromAuthStaticOrganizationAssignment(AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				})
				oidcSpec := OIDCProviderSpec{
					ProviderType:           Oidc,
					Issuer:                 "https://oidc.example.com",
					ClientId:               "client-id",
					ClientSecret:           lo.ToPtr("originalsecret"),
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}
				provider := &AuthProvider{
					Metadata: ObjectMeta{Name: lo.ToPtr("oidc-provider")},
				}
				_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)
				return provider
			},
			setupNew: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				_ = assignment.FromAuthStaticOrganizationAssignment(AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				})
				oidcSpec := OIDCProviderSpec{
					ProviderType:           Oidc,
					Issuer:                 "https://oidc.example.com",
					ClientId:               "client-id",
					ClientSecret:           lo.ToPtr("*****"), // masked placeholder
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}
				provider := &AuthProvider{
					Metadata: ObjectMeta{Name: lo.ToPtr("oidc-provider")},
				}
				_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)
				return provider
			},
			checkPreserved: func(t *testing.T, ap *AuthProvider) {
				oidcSpec, err := ap.Spec.AsOIDCProviderSpec()
				require.NoError(t, err)
				assert.Equal(t, "originalsecret", *oidcSpec.ClientSecret, "Client secret should be preserved from existing")
			},
		},
		{
			name: "OAuth2 provider preserves client secret when masked",
			setupExisting: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				_ = assignment.FromAuthStaticOrganizationAssignment(AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				})
				oauth2Spec := OAuth2ProviderSpec{
					ProviderType:           Oauth2,
					AuthorizationUrl:       "https://oauth2.example.com/authorize",
					TokenUrl:               "https://oauth2.example.com/token",
					UserinfoUrl:            "https://oauth2.example.com/userinfo",
					ClientId:               "client-id",
					ClientSecret:           lo.ToPtr("oauth2secret"),
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}
				provider := &AuthProvider{
					Metadata: ObjectMeta{Name: lo.ToPtr("oauth2-provider")},
				}
				_ = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
				return provider
			},
			setupNew: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				_ = assignment.FromAuthStaticOrganizationAssignment(AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				})
				oauth2Spec := OAuth2ProviderSpec{
					ProviderType:           Oauth2,
					AuthorizationUrl:       "https://oauth2.example.com/authorize",
					TokenUrl:               "https://oauth2.example.com/token",
					UserinfoUrl:            "https://oauth2.example.com/userinfo",
					ClientId:               "client-id",
					ClientSecret:           lo.ToPtr("*****"), // masked placeholder
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}
				provider := &AuthProvider{
					Metadata: ObjectMeta{Name: lo.ToPtr("oauth2-provider")},
				}
				_ = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
				return provider
			},
			checkPreserved: func(t *testing.T, ap *AuthProvider) {
				oauth2Spec, err := ap.Spec.AsOAuth2ProviderSpec()
				require.NoError(t, err)
				assert.Equal(t, "oauth2secret", *oauth2Spec.ClientSecret, "Client secret should be preserved from existing")
			},
		},
		{
			name: "New client secret is used when not masked",
			setupExisting: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				_ = assignment.FromAuthStaticOrganizationAssignment(AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				})
				oidcSpec := OIDCProviderSpec{
					ProviderType:           Oidc,
					Issuer:                 "https://oidc.example.com",
					ClientId:               "client-id",
					ClientSecret:           lo.ToPtr("oldsecret"),
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}
				provider := &AuthProvider{
					Metadata: ObjectMeta{Name: lo.ToPtr("oidc-provider")},
				}
				_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)
				return provider
			},
			setupNew: func() *AuthProvider {
				assignment := AuthOrganizationAssignment{}
				_ = assignment.FromAuthStaticOrganizationAssignment(AuthStaticOrganizationAssignment{
					Type:             AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "test-org",
				})
				oidcSpec := OIDCProviderSpec{
					ProviderType:           Oidc,
					Issuer:                 "https://oidc.example.com",
					ClientId:               "client-id",
					ClientSecret:           lo.ToPtr("newsecret"), // actual new secret
					Enabled:                lo.ToPtr(true),
					OrganizationAssignment: assignment,
				}
				provider := &AuthProvider{
					Metadata: ObjectMeta{Name: lo.ToPtr("oidc-provider")},
				}
				_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)
				return provider
			},
			checkPreserved: func(t *testing.T, ap *AuthProvider) {
				oidcSpec, err := ap.Spec.AsOIDCProviderSpec()
				require.NoError(t, err)
				assert.Equal(t, "newsecret", *oidcSpec.ClientSecret, "New client secret should be used")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing := tt.setupExisting()
			newAP := tt.setupNew()
			err := newAP.PreserveSensitiveData(existing)
			require.NoError(t, err)
			tt.checkPreserved(t, newAP)
		})
	}
}
