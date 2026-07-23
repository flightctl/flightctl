package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestEncryption(t *testing.T) *encryption.Manager {
	t.Helper()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")

	key, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath, []byte(key), 0600))

	cfg := config.NewDefault()
	cfg.Encryption = &config.EncryptionConfig{
		Keys:        []config.EncryptionKeyConfig{{ID: "default", Path: keyPath}},
		ActiveKeyID: "default",
	}

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	err = encryption.InitGlobalEncryption(logger, cfg)
	require.NoError(t, err)

	mgr := encryption.GlobalManager()
	require.NotNil(t, mgr)
	return mgr
}

func TestEncryptPathsGetters_WhenCallerMutatesItShouldNotAffectGlobals(t *testing.T) {
	repoPaths := RepositoryEncryptPaths()
	require.NotEmpty(t, repoPaths)
	repoPaths[0][0] = "mutated"

	freshRepo := RepositoryEncryptPaths()
	assert.NotEqual(t, "mutated", freshRepo[0][0])

	apPaths := AuthProviderEncryptPaths()
	require.NotEmpty(t, apPaths)
	apPaths[0][0] = "mutated"

	freshAP := AuthProviderEncryptPaths()
	assert.NotEqual(t, "mutated", freshAP[0][0])
}

func TestEncryptionFieldPaths_WhenComparedToHandlersItShouldCoverSameKinds(t *testing.T) {
	handlers := EncryptionHandlers()
	paths := EncryptionFieldPaths()
	require.Equal(t, len(handlers), len(paths))
	for kind := range handlers {
		kindPaths, ok := EncryptPathsForKind(kind)
		require.True(t, ok, "handler kind %q missing from EncryptionFieldPaths", kind)
		require.NotEmpty(t, kindPaths, "handler kind %q has empty encrypt paths", kind)
	}
	for kind := range paths {
		_, ok := handlers[kind]
		require.True(t, ok, "EncryptionFieldPaths kind %q missing from EncryptionHandlers", kind)
	}
}

func TestEncryptionHandlersRegistry(t *testing.T) {
	mgr := setupTestEncryption(t)

	mockEncryptFunc := func(ctx context.Context, data []byte) ([]byte, error) {
		return data, nil
	}
	_ = mgr

	for modelName, handler := range EncryptionHandlers() {
		t.Run(modelName, func(t *testing.T) {
			require.NotNil(t, handler, "Encryption handler for %s is nil", modelName)

			var model any
			switch modelName {
			case "Repository":
				model = &Repository{}
			case "AuthProvider":
				model = &AuthProvider{}
			default:
				t.Fatalf("Unknown model type in registry: %s - add a case for it in this test", modelName)
			}

			err := handler(context.Background(), model, mockEncryptFunc)
			if err != nil {
				t.Logf("Handler %s returned error (expected for empty model): %v", modelName, err)
			}
		})
	}
}

func TestEncryptionFormatStability(t *testing.T) {
	mgr := setupTestEncryption(t)

	plaintext := []byte("test-secret")
	encrypted, err := mgr.Encrypt(context.Background(), plaintext)
	require.NoError(t, err)

	encryptedStr := string(encrypted)
	require.True(t, encryption.IsEncrypted(encrypted), "Should be recognized as encrypted")
	require.Contains(t, encryptedStr, "enc:v1:", "Should have enc:v1: prefix")
	require.Contains(t, encryptedStr, "default:", "Should have keyID 'default'")

	decrypted, err := mgr.Decrypt(context.Background(), encrypted)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted, "Decrypted value should match plaintext")
}

func TestRepositoryEncryptionEndToEnd(t *testing.T) {
	mgr := setupTestEncryption(t)

	testCases := []struct {
		name        string
		apiRepo     *domain.Repository
		checkValues []string
	}{
		{
			name: "When encrypting GitRepoSpec with httpConfig it should encrypt password and token",
			apiRepo: &domain.Repository{
				Metadata: domain.ObjectMeta{Name: strPtr("test-http")},
				Spec: func() domain.RepositorySpec {
					var spec domain.RepositorySpec
					_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
						HttpConfig: &domain.HttpConfig{
							Username: strPtr("testuser"),
							Password: strPtr("testpass"),
							Token:    strPtr("testtoken"),
						},
					})
					return spec
				}(),
			},
			checkValues: []string{"testpass", "testtoken"},
		},
		{
			name: "When encrypting GitRepoSpec with httpConfig it should encrypt TLS key and cert",
			apiRepo: &domain.Repository{
				Metadata: domain.ObjectMeta{Name: strPtr("test-tls")},
				Spec: func() domain.RepositorySpec {
					var spec domain.RepositorySpec
					_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
						HttpConfig: &domain.HttpConfig{
							TlsKey: strPtr("base64-encoded-private-key"),
							TlsCrt: strPtr("base64-encoded-certificate"),
						},
					})
					return spec
				}(),
			},
			checkValues: []string{"base64-encoded-private-key", "base64-encoded-certificate"},
		},
		{
			name: "When encrypting GitRepoSpec with sshConfig it should encrypt private key and passphrase",
			apiRepo: &domain.Repository{
				Metadata: domain.ObjectMeta{Name: strPtr("test-ssh")},
				Spec: func() domain.RepositorySpec {
					var spec domain.RepositorySpec
					_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
						SshConfig: &domain.SshConfig{
							SshPrivateKey:        strPtr("ssh-key-content"),
							PrivateKeyPassphrase: strPtr("ssh-passphrase"),
						},
					})
					return spec
				}(),
			},
			checkValues: []string{"ssh-key-content", "ssh-passphrase"},
		},
		{
			name: "When encrypting OciRepoSpec with ociAuth it should encrypt password",
			apiRepo: &domain.Repository{
				Metadata: domain.ObjectMeta{Name: strPtr("test-oci")},
				Spec: func() domain.RepositorySpec {
					var ociAuth domain.OciAuth
					_ = ociAuth.FromDockerAuth(domain.DockerAuth{
						AuthType: domain.OciAuthTypeDocker,
						Username: "ociuser",
						Password: "ocipass",
					})
					var spec domain.RepositorySpec
					_ = spec.FromOciRepoSpec(domain.OciRepoSpec{
						OciAuth: &ociAuth,
					})
					return spec
				}(),
			},
			checkValues: []string{"ocipass"},
		},
		{
			name: "When encrypting HttpRepoSpec with httpConfig it should encrypt password and token",
			apiRepo: &domain.Repository{
				Metadata: domain.ObjectMeta{Name: strPtr("test-http-repo")},
				Spec: func() domain.RepositorySpec {
					var spec domain.RepositorySpec
					_ = spec.FromHttpRepoSpec(domain.HttpRepoSpec{
						HttpConfig: &domain.HttpConfig{
							Password: strPtr("http-repo-pass"),
							Token:    strPtr("http-repo-token"),
						},
					})
					return spec
				}(),
			},
			checkValues: []string{"http-repo-pass", "http-repo-token"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			modelRepo, err := NewRepositoryFromApiResource(tc.apiRepo)
			require.NoError(t, err)

			handler := EncryptionHandlers()["Repository"]
			err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
			require.NoError(t, err)

			specJSON, err := json.Marshal(modelRepo.Spec.Data)
			require.NoError(t, err)
			specStr := string(specJSON)

			for _, plaintext := range tc.checkValues {
				require.NotContains(t, specStr, plaintext,
					"Plaintext '%s' found in spec after encryption", plaintext)
			}

			require.Contains(t, specStr, "enc:v1:default:",
				"Spec should contain encrypted values with proper format")
		})
	}
}

func TestRepositoryEncryption_UsernameNotEncrypted(t *testing.T) {
	mgr := setupTestEncryption(t)

	apiRepo := &domain.Repository{
		Metadata: domain.ObjectMeta{Name: strPtr("test-username")},
		Spec: func() domain.RepositorySpec {
			var spec domain.RepositorySpec
			_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
				HttpConfig: &domain.HttpConfig{
					Username: strPtr("testuser"),
					Password: strPtr("testpass"),
				},
			})
			return spec
		}(),
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	handler := EncryptionHandlers()["Repository"]
	err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)
	specStr := string(specJSON)

	require.Contains(t, specStr, "testuser",
		"Username should NOT be encrypted - it is not a sensitive field")
	require.NotContains(t, specStr, "testpass",
		"Password should be encrypted")
}

func staticOrgAssignment() domain.AuthOrganizationAssignment {
	var oa domain.AuthOrganizationAssignment
	_ = oa.FromAuthStaticOrganizationAssignment(domain.AuthStaticOrganizationAssignment{
		Type: domain.AuthStaticOrganizationAssignmentTypeStatic,
	})
	return oa
}

func TestAuthProviderEncryptionEndToEnd(t *testing.T) {
	mgr := setupTestEncryption(t)

	testCases := []struct {
		name    string
		apiProv *domain.AuthProvider
		secret  string
	}{
		{
			name: "When encrypting OIDC provider it should encrypt clientSecret",
			apiProv: &domain.AuthProvider{
				Metadata: domain.ObjectMeta{Name: strPtr("test-oidc")},
				Spec: func() domain.AuthProviderSpec {
					var spec domain.AuthProviderSpec
					_ = spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
						ClientId:               "my-client",
						ClientSecret:           "oidc-secret-value",
						Issuer:                 "https://issuer.example.com",
						ProviderType:           domain.Oidc,
						OrganizationAssignment: staticOrgAssignment(),
					})
					return spec
				}(),
			},
			secret: "oidc-secret-value",
		},
		{
			name: "When encrypting OAuth2 provider it should encrypt clientSecret",
			apiProv: &domain.AuthProvider{
				Metadata: domain.ObjectMeta{Name: strPtr("test-oauth2")},
				Spec: func() domain.AuthProviderSpec {
					var spec domain.AuthProviderSpec
					_ = spec.FromOAuth2ProviderSpec(domain.OAuth2ProviderSpec{
						ClientId:               "my-client",
						ClientSecret:           "oauth2-secret-value",
						AuthorizationUrl:       "https://auth.example.com/authorize",
						TokenUrl:               "https://auth.example.com/token",
						UserinfoUrl:            "https://auth.example.com/userinfo",
						ProviderType:           domain.Oauth2,
						OrganizationAssignment: staticOrgAssignment(),
					})
					return spec
				}(),
			},
			secret: "oauth2-secret-value",
		},
		{
			name: "When encrypting OpenShift provider it should encrypt clientSecret",
			apiProv: &domain.AuthProvider{
				Metadata: domain.ObjectMeta{Name: strPtr("test-openshift")},
				Spec: func() domain.AuthProviderSpec {
					var spec domain.AuthProviderSpec
					_ = spec.FromOpenShiftProviderSpec(domain.OpenShiftProviderSpec{
						ClientSecret:           strPtr("openshift-secret-value"),
						ClusterControlPlaneUrl: strPtr("https://api.cluster.example.com:6443"),
						ProviderType:           domain.Openshift,
					})
					return spec
				}(),
			},
			secret: "openshift-secret-value",
		},
		{
			name: "When encrypting AAP provider it should encrypt clientSecret",
			apiProv: &domain.AuthProvider{
				Metadata: domain.ObjectMeta{Name: strPtr("test-aap")},
				Spec: func() domain.AuthProviderSpec {
					var spec domain.AuthProviderSpec
					_ = spec.FromAapProviderSpec(domain.AapProviderSpec{
						ClientId:         "my-client",
						ClientSecret:     "aap-secret-value",
						ApiUrl:           "https://aap.example.com/api",
						AuthorizationUrl: "https://aap.example.com/authorize",
						TokenUrl:         "https://aap.example.com/token",
						ProviderType:     domain.Aap,
					})
					return spec
				}(),
			},
			secret: "aap-secret-value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			modelAP, err := NewAuthProviderFromApiResource(tc.apiProv)
			require.NoError(t, err)

			handler := EncryptionHandlers()["AuthProvider"]
			err = handler(context.Background(), modelAP, mgr.ProcessEncryption)
			require.NoError(t, err)

			specJSON, err := json.Marshal(modelAP.Spec.Data)
			require.NoError(t, err)
			specStr := string(specJSON)

			require.NotContains(t, specStr, tc.secret,
				"Plaintext clientSecret '%s' found in spec after encryption", tc.secret)
			require.Contains(t, specStr, "enc:v1:default:",
				"Spec should contain encrypted clientSecret with proper format")

			// Verify decrypt round-trip: extract the encrypted clientSecret from
			// the spec JSON and decrypt it back to the original value.
			var specMap map[string]any
			require.NoError(t, json.Unmarshal(specJSON, &specMap))

			encryptedSecret := extractClientSecret(t, specMap)
			require.True(t, encryption.IsEncrypted([]byte(encryptedSecret)),
				"clientSecret should be encrypted in spec")

			decrypted, err := mgr.Decrypt(context.Background(), []byte(encryptedSecret))
			require.NoError(t, err)
			require.Equal(t, tc.secret, string(decrypted),
				"Decrypted clientSecret should match original plaintext")
		})
	}
}

func extractClientSecret(t *testing.T, specMap map[string]any) string {
	t.Helper()
	val, ok := specMap["clientSecret"]
	if ok {
		s, ok := val.(string)
		require.True(t, ok, "clientSecret should be a string")
		return s
	}
	t.Fatal("clientSecret not found in spec map")
	return ""
}

func TestRepositoryEncryption_FailClosed(t *testing.T) {
	_ = setupTestEncryption(t)

	apiRepo := &domain.Repository{
		Metadata: domain.ObjectMeta{Name: strPtr("test-fail-closed")},
		Spec: func() domain.RepositorySpec {
			var spec domain.RepositorySpec
			_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
				HttpConfig: &domain.HttpConfig{
					Password: strPtr("secret-password"),
					Token:    strPtr("secret-token"),
				},
			})
			return spec
		}(),
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	originalSpec, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)

	errEncrypt := fmt.Errorf("KMS unavailable")
	failingEncrypt := func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errEncrypt
	}

	handler := EncryptionHandlers()["Repository"]
	err = handler(context.Background(), modelRepo, failingEncrypt)
	require.ErrorIs(t, err, errEncrypt)

	afterSpec, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)
	require.JSONEq(t, string(originalSpec), string(afterSpec),
		"spec must remain unmodified after encryption failure")
}

func TestAuthProviderEncryption_FailClosed(t *testing.T) {
	_ = setupTestEncryption(t)

	apiProv := &domain.AuthProvider{
		Metadata: domain.ObjectMeta{Name: strPtr("test-fail-closed")},
		Spec: func() domain.AuthProviderSpec {
			var spec domain.AuthProviderSpec
			_ = spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
				Issuer:       "https://issuer.example.com",
				ClientId:     "client-id",
				ClientSecret: "top-secret",
				ProviderType: domain.Oidc,
			})
			return spec
		}(),
	}

	modelAP, err := NewAuthProviderFromApiResource(apiProv)
	require.NoError(t, err)

	originalSpec, err := json.Marshal(modelAP.Spec.Data)
	require.NoError(t, err)

	errEncrypt := fmt.Errorf("KMS unavailable")
	failingEncrypt := func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errEncrypt
	}

	handler := EncryptionHandlers()["AuthProvider"]
	err = handler(context.Background(), modelAP, failingEncrypt)
	require.ErrorIs(t, err, errEncrypt)

	afterSpec, err := json.Marshal(modelAP.Spec.Data)
	require.NoError(t, err)
	require.JSONEq(t, string(originalSpec), string(afterSpec),
		"spec must remain unmodified after encryption failure")
}

func strPtr(s string) *string {
	return &s
}
