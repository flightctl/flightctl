package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Test setup helpers
// ---------------------------------------------------------------------------

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

func strPtr(s string) *string {
	return &s
}

func staticOrgAssignment() domain.AuthOrganizationAssignment {
	var oa domain.AuthOrganizationAssignment
	_ = oa.FromAuthStaticOrganizationAssignment(domain.AuthStaticOrganizationAssignment{
		Type: domain.AuthStaticOrganizationAssignmentTypeStatic,
	})
	return oa
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

func extractPlaintextFields(m map[string]any) map[string]string {
	result := make(map[string]string)
	collectStrings(m, "", result, false)
	return result
}

func extractEncryptedFields(m map[string]any) map[string]string {
	result := make(map[string]string)
	collectStrings(m, "", result, true)
	return result
}

func collectStrings(m map[string]any, prefix string, result map[string]string, wantEncrypted bool) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			isEnc := encryption.IsEncrypted([]byte(val))
			if wantEncrypted == isEnc {
				result[key] = val
			}
		case map[string]any:
			collectStrings(val, key, result, wantEncrypted)
		case []any:
			for i, elem := range val {
				elemKey := fmt.Sprintf("%s[%d]", key, i)
				if nested, ok := elem.(map[string]any); ok {
					collectStrings(nested, elemKey, result, wantEncrypted)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Encryption format stability
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Handler registration
// ---------------------------------------------------------------------------

func TestEncryptionHandlers_Registry(t *testing.T) {
	_ = setupTestEncryption(t)

	handlers := EncryptionHandlers()
	require.Len(t, handlers, 3, "Should have 3 handlers registered")

	require.Contains(t, handlers, domain.RepositoryKind)
	require.Contains(t, handlers, domain.AuthProviderKind)
	require.Contains(t, handlers, domain.DeviceKind)

	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}

	for modelName, handler := range handlers {
		t.Run(modelName, func(t *testing.T) {
			require.NotNil(t, handler)

			var model any
			switch modelName {
			case domain.RepositoryKind:
				model = &Repository{}
			case domain.AuthProviderKind:
				model = &AuthProvider{}
			case domain.DeviceKind:
				model = &Device{}
			default:
				t.Fatalf("Unknown model type: %s", modelName)
			}

			err := handler(context.Background(), model, noopEncrypt)
			require.NoError(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// Registry helpers
// ---------------------------------------------------------------------------

func TestRegistryHash_Deterministic(t *testing.T) {
	hash1 := RegistryHash()
	require.NotEmpty(t, hash1)

	hash2 := RegistryHash()
	require.Equal(t, hash1, hash2, "Hash should be deterministic across calls")

	require.Len(t, hash1, 64, "SHA-256 hex digest should be 64 chars")
}

func TestRegistryHash_ChangesWhenFieldAdded(t *testing.T) {
	originalHash := RegistryHash()

	original := make([]EncryptedField, len(encryptionRegistry))
	copy(original, encryptionRegistry)

	encryptionRegistry = append(encryptionRegistry, EncryptedField{
		Kind: domain.RepositoryKind,
		Path: []string{"Spec", "httpConfig", "newSecret"},
	})
	t.Cleanup(func() { encryptionRegistry = original })

	newHash := RegistryHash()
	require.NotEqual(t, originalHash, newHash,
		"Hash should change when a new encrypted field is added")
}

func TestPathsForKind_AllKinds(t *testing.T) {
	repoPaths := PathsForKind(domain.RepositoryKind)
	require.Len(t, repoPaths, 7, "Repository should have 7 encrypted paths")

	for _, p := range repoPaths {
		require.True(t, len(p) >= 2, "Each path should have at least 2 segments (field + key)")
		require.Equal(t, "Spec", p[0], "First segment should be 'Spec'")
	}

	apPaths := PathsForKind(domain.AuthProviderKind)
	require.Len(t, apPaths, 1, "AuthProvider should have 1 encrypted path")
	require.Equal(t, []string{"Spec", "clientSecret"}, apPaths[0])

	devicePaths := PathsForKind(domain.DeviceKind)
	require.Len(t, devicePaths, 2, "Device should have 2 encrypted paths")
	require.Equal(t, []string{"RenderedConfig"}, devicePaths[0])
	require.Equal(t, []string{"RenderedApplications"}, devicePaths[1])

	unknownPaths := PathsForKind("UnknownKind")
	require.Nil(t, unknownPaths, "Unknown kind should return nil")
}

func TestEncryptionRegistry_Completeness(t *testing.T) {
	kindCounts := make(map[string]int)
	for _, ef := range encryptionRegistry {
		kindCounts[ef.Kind]++
	}

	require.Equal(t, 7, kindCounts[domain.RepositoryKind],
		"Registry should have 7 Repository entries")
	require.Equal(t, 1, kindCounts[domain.AuthProviderKind],
		"Registry should have 1 AuthProvider entry")
	require.Equal(t, 2, kindCounts[domain.DeviceKind],
		"Registry should have 2 Device entries")
}

// ---------------------------------------------------------------------------
// pascalToSnake helper
// ---------------------------------------------------------------------------

func TestPascalToSnake(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"RenderedConfig", "rendered_config"},
		{"RenderedApplications", "rendered_applications"},
		{"Spec", "spec"},
		{"ID", "id"},
		{"ResourceVersion", "resource_version"},
		{"", ""},
	}
	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			require.Equal(t, tc.expected, pascalToSnake(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// Repository — encrypt end-to-end
// ---------------------------------------------------------------------------

func TestEncryptHandler_RepositoryGitHTTP(t *testing.T) {
	mgr := setupTestEncryption(t)

	testCases := []struct {
		name           string
		apiRepo        *domain.Repository
		encryptedKeys  []string
		plaintextVals  []string
		encryptedCount int
	}{
		{
			name: "When encrypting GitRepoSpec with httpConfig it should encrypt password and token but not username",
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
			encryptedKeys:  []string{"testpass", "testtoken"},
			plaintextVals:  []string{"testuser"},
			encryptedCount: 2,
		},
		{
			name: "When encrypting GitRepoSpec with TLS it should encrypt key and cert",
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
			encryptedKeys:  []string{"base64-encoded-private-key", "base64-encoded-certificate"},
			encryptedCount: 2,
		},
		{
			name: "When encrypting GitRepoSpec with all httpConfig fields it should encrypt all sensitive ones",
			apiRepo: &domain.Repository{
				Metadata: domain.ObjectMeta{Name: strPtr("test-all-http")},
				Spec: func() domain.RepositorySpec {
					var spec domain.RepositorySpec
					_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
						HttpConfig: &domain.HttpConfig{
							Username: strPtr("myuser"),
							Password: strPtr("mypass"),
							Token:    strPtr("mytoken"),
							TlsKey:   strPtr("my-tls-key"),
							TlsCrt:   strPtr("my-tls-crt"),
						},
					})
					return spec
				}(),
			},
			encryptedKeys:  []string{"mypass", "mytoken", "my-tls-key", "my-tls-crt"},
			plaintextVals:  []string{"myuser"},
			encryptedCount: 4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			modelRepo, err := NewRepositoryFromApiResource(tc.apiRepo)
			require.NoError(t, err)

			handler := EncryptionHandlers()[domain.RepositoryKind]
			err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
			require.NoError(t, err)

			specJSON, err := json.Marshal(modelRepo.Spec.Data)
			require.NoError(t, err)
			specStr := string(specJSON)

			for _, val := range tc.encryptedKeys {
				require.NotContains(t, specStr, val,
					"Plaintext '%s' found in spec after encryption", val)
			}
			for _, val := range tc.plaintextVals {
				require.Contains(t, specStr, val,
					"Non-sensitive '%s' should remain in plaintext", val)
			}

			var specMap map[string]any
			require.NoError(t, json.Unmarshal(specJSON, &specMap))
			encrypted := extractEncryptedFields(specMap)
			require.Len(t, encrypted, tc.encryptedCount,
				"Should have exactly %d encrypted fields", tc.encryptedCount)
		})
	}
}

func TestEncryptHandler_RepositoryGitSSH(t *testing.T) {
	mgr := setupTestEncryption(t)

	apiRepo := &domain.Repository{
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
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	handler := EncryptionHandlers()[domain.RepositoryKind]
	err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)
	specStr := string(specJSON)

	require.NotContains(t, specStr, "ssh-key-content")
	require.NotContains(t, specStr, "ssh-passphrase")
	require.Contains(t, specStr, "enc:v1:default:")
}

func TestEncryptHandler_RepositoryOCI(t *testing.T) {
	mgr := setupTestEncryption(t)

	apiRepo := &domain.Repository{
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
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	handler := EncryptionHandlers()[domain.RepositoryKind]
	err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)
	specStr := string(specJSON)

	require.NotContains(t, specStr, "ocipass")
	require.Contains(t, specStr, "ociuser", "Username should remain in plaintext")
	require.Contains(t, specStr, "enc:v1:default:")
}

func TestEncryptHandler_RepositoryHTTPRepo(t *testing.T) {
	mgr := setupTestEncryption(t)

	apiRepo := &domain.Repository{
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
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	handler := EncryptionHandlers()[domain.RepositoryKind]
	err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)
	specStr := string(specJSON)

	require.NotContains(t, specStr, "http-repo-pass")
	require.NotContains(t, specStr, "http-repo-token")
	require.Contains(t, specStr, "enc:v1:default:")
}

// ---------------------------------------------------------------------------
// Repository — decrypt round-trip
// ---------------------------------------------------------------------------

func TestEncryptHandler_RepositoryDecryptRoundTrip(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	apiRepo := &domain.Repository{
		Metadata: domain.ObjectMeta{Name: strPtr("test-roundtrip")},
		Spec: func() domain.RepositorySpec {
			var spec domain.RepositorySpec
			_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
				HttpConfig: &domain.HttpConfig{
					Username: strPtr("testuser"),
					Password: strPtr("testpass"),
					Token:    strPtr("testtoken"),
				},
				SshConfig: &domain.SshConfig{
					SshPrivateKey:        strPtr("ssh-key"),
					PrivateKeyPassphrase: strPtr("ssh-passphrase"),
				},
			})
			return spec
		}(),
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	originalSpecJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)

	handler := EncryptionHandlers()[domain.RepositoryKind]
	err = handler(ctx, modelRepo, mgr.ProcessEncryption)
	require.NoError(t, err)

	encryptedSpecJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)

	var encryptedMap map[string]any
	require.NoError(t, json.Unmarshal(encryptedSpecJSON, &encryptedMap))
	encFields := extractEncryptedFields(encryptedMap)

	var originalMap map[string]any
	require.NoError(t, json.Unmarshal(originalSpecJSON, &originalMap))
	originalAll := map[string]string{}
	collectStrings(originalMap, "", originalAll, false)

	for key, encValue := range encFields {
		decrypted, err := mgr.Decrypt(ctx, []byte(encValue))
		require.NoError(t, err, "Failed to decrypt field %s", key)
		require.Equal(t, originalAll[key], string(decrypted),
			"Field %s should decrypt back to its original plaintext", key)
	}

	originalPlaintext := extractPlaintextFields(originalMap)
	encryptedPlaintext := extractPlaintextFields(encryptedMap)

	for key, val := range originalPlaintext {
		if _, wasEncrypted := encFields[key]; wasEncrypted {
			continue
		}
		require.Equal(t, val, encryptedPlaintext[key],
			"Non-sensitive field %s should be preserved", key)
	}
}

// ---------------------------------------------------------------------------
// Repository — idempotency
// ---------------------------------------------------------------------------

func TestEncryptHandler_RepositoryIdempotent(t *testing.T) {
	mgr := setupTestEncryption(t)

	apiRepo := &domain.Repository{
		Metadata: domain.ObjectMeta{Name: strPtr("test-idempotent")},
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

	handler := EncryptionHandlers()[domain.RepositoryKind]

	err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterFirst, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)

	err = handler(context.Background(), modelRepo, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterSecond, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)

	require.JSONEq(t, string(afterFirst), string(afterSecond),
		"Encrypting an already-encrypted spec should be idempotent")
}

// ---------------------------------------------------------------------------
// Repository — no sensitive fields present
// ---------------------------------------------------------------------------

func TestEncryptHandler_RepositoryNoSensitiveFields(t *testing.T) {
	_ = setupTestEncryption(t)

	apiRepo := &domain.Repository{
		Metadata: domain.ObjectMeta{Name: strPtr("test-no-sensitive")},
		Spec: func() domain.RepositorySpec {
			var spec domain.RepositorySpec
			_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
				HttpConfig: &domain.HttpConfig{
					Username: strPtr("justuser"),
				},
			})
			return spec
		}(),
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	originalJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)

	handler := EncryptionHandlers()[domain.RepositoryKind]
	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}
	err = handler(context.Background(), modelRepo, noopEncrypt)
	require.NoError(t, err)

	afterJSON, err := json.Marshal(modelRepo.Spec.Data)
	require.NoError(t, err)
	require.JSONEq(t, string(originalJSON), string(afterJSON),
		"Spec with no sensitive fields should remain unchanged")
}

// ---------------------------------------------------------------------------
// Repository — fail closed
// ---------------------------------------------------------------------------

func TestEncryptHandler_RepositoryFailClosed(t *testing.T) {
	_ = setupTestEncryption(t)

	apiRepo := &domain.Repository{
		Metadata: domain.ObjectMeta{Name: strPtr("test-fail")},
		Spec: func() domain.RepositorySpec {
			var spec domain.RepositorySpec
			_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
				HttpConfig: &domain.HttpConfig{
					Password: strPtr("secret-password"),
				},
			})
			return spec
		}(),
	}

	modelRepo, err := NewRepositoryFromApiResource(apiRepo)
	require.NoError(t, err)

	errEncrypt := fmt.Errorf("KMS unavailable")
	failingEncrypt := func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errEncrypt
	}

	handler := EncryptionHandlers()[domain.RepositoryKind]
	err = handler(context.Background(), modelRepo, failingEncrypt)
	require.ErrorIs(t, err, errEncrypt)
}

// ---------------------------------------------------------------------------
// Repository — nil spec
// ---------------------------------------------------------------------------

func TestEncryptHandler_RepositoryNilSpec(t *testing.T) {
	_ = setupTestEncryption(t)

	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}

	repo := &Repository{}
	handler := EncryptionHandlers()[domain.RepositoryKind]
	err := handler(context.Background(), repo, noopEncrypt)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// AuthProvider — all provider types
// ---------------------------------------------------------------------------

func TestEncryptHandler_AuthProviderAllTypes(t *testing.T) {
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

			handler := EncryptionHandlers()[domain.AuthProviderKind]
			err = handler(context.Background(), modelAP, mgr.ProcessEncryption)
			require.NoError(t, err)

			specJSON, err := json.Marshal(modelAP.Spec.Data)
			require.NoError(t, err)
			specStr := string(specJSON)

			require.NotContains(t, specStr, tc.secret,
				"Plaintext clientSecret '%s' found in spec after encryption", tc.secret)
			require.Contains(t, specStr, "enc:v1:default:",
				"Spec should contain encrypted clientSecret with proper format")
		})
	}
}

// ---------------------------------------------------------------------------
// AuthProvider — decrypt round-trip
// ---------------------------------------------------------------------------

func TestEncryptHandler_AuthProviderDecryptRoundTrip(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	testCases := []struct {
		name    string
		apiProv *domain.AuthProvider
		secret  string
	}{
		{
			name: "When decrypting OIDC provider it should recover original clientSecret",
			apiProv: &domain.AuthProvider{
				Metadata: domain.ObjectMeta{Name: strPtr("test-oidc-rt")},
				Spec: func() domain.AuthProviderSpec {
					var spec domain.AuthProviderSpec
					_ = spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
						ClientId:               "my-client",
						ClientSecret:           "oidc-roundtrip-secret",
						Issuer:                 "https://issuer.example.com",
						ProviderType:           domain.Oidc,
						OrganizationAssignment: staticOrgAssignment(),
					})
					return spec
				}(),
			},
			secret: "oidc-roundtrip-secret",
		},
		{
			name: "When decrypting AAP provider it should recover original clientSecret",
			apiProv: &domain.AuthProvider{
				Metadata: domain.ObjectMeta{Name: strPtr("test-aap-rt")},
				Spec: func() domain.AuthProviderSpec {
					var spec domain.AuthProviderSpec
					_ = spec.FromAapProviderSpec(domain.AapProviderSpec{
						ClientId:         "aap-client",
						ClientSecret:     "aap-roundtrip-secret",
						ApiUrl:           "https://aap.example.com/api",
						AuthorizationUrl: "https://aap.example.com/authorize",
						TokenUrl:         "https://aap.example.com/token",
						ProviderType:     domain.Aap,
					})
					return spec
				}(),
			},
			secret: "aap-roundtrip-secret",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			modelAP, err := NewAuthProviderFromApiResource(tc.apiProv)
			require.NoError(t, err)

			handler := EncryptionHandlers()[domain.AuthProviderKind]
			err = handler(ctx, modelAP, mgr.ProcessEncryption)
			require.NoError(t, err)

			specJSON, err := json.Marshal(modelAP.Spec.Data)
			require.NoError(t, err)

			var specMap map[string]any
			require.NoError(t, json.Unmarshal(specJSON, &specMap))

			encryptedSecret := extractClientSecret(t, specMap)
			require.True(t, encryption.IsEncrypted([]byte(encryptedSecret)),
				"clientSecret should be encrypted")

			decrypted, err := mgr.Decrypt(ctx, []byte(encryptedSecret))
			require.NoError(t, err)
			require.Equal(t, tc.secret, string(decrypted),
				"Decrypted clientSecret should match original")
		})
	}
}

// ---------------------------------------------------------------------------
// AuthProvider — idempotency
// ---------------------------------------------------------------------------

func TestEncryptHandler_AuthProviderIdempotent(t *testing.T) {
	mgr := setupTestEncryption(t)

	apiProv := &domain.AuthProvider{
		Metadata: domain.ObjectMeta{Name: strPtr("test-idempotent")},
		Spec: func() domain.AuthProviderSpec {
			var spec domain.AuthProviderSpec
			_ = spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
				ClientId:               "my-client",
				ClientSecret:           "idempotent-secret",
				Issuer:                 "https://issuer.example.com",
				ProviderType:           domain.Oidc,
				OrganizationAssignment: staticOrgAssignment(),
			})
			return spec
		}(),
	}

	modelAP, err := NewAuthProviderFromApiResource(apiProv)
	require.NoError(t, err)

	handler := EncryptionHandlers()[domain.AuthProviderKind]

	err = handler(context.Background(), modelAP, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterFirst, err := json.Marshal(modelAP.Spec.Data)
	require.NoError(t, err)

	err = handler(context.Background(), modelAP, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterSecond, err := json.Marshal(modelAP.Spec.Data)
	require.NoError(t, err)

	require.JSONEq(t, string(afterFirst), string(afterSecond),
		"Encrypting an already-encrypted spec should be idempotent")
}

// ---------------------------------------------------------------------------
// AuthProvider — fail closed
// ---------------------------------------------------------------------------

func TestEncryptHandler_AuthProviderFailClosed(t *testing.T) {
	_ = setupTestEncryption(t)

	apiProv := &domain.AuthProvider{
		Metadata: domain.ObjectMeta{Name: strPtr("test-fail")},
		Spec: func() domain.AuthProviderSpec {
			var spec domain.AuthProviderSpec
			_ = spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
				ClientId:     "client",
				ClientSecret: "top-secret",
				Issuer:       "https://issuer.example.com",
				ProviderType: domain.Oidc,
			})
			return spec
		}(),
	}

	modelAP, err := NewAuthProviderFromApiResource(apiProv)
	require.NoError(t, err)

	errEncrypt := fmt.Errorf("KMS unavailable")
	failingEncrypt := func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errEncrypt
	}

	handler := EncryptionHandlers()[domain.AuthProviderKind]
	err = handler(context.Background(), modelAP, failingEncrypt)
	require.ErrorIs(t, err, errEncrypt)
}

// ---------------------------------------------------------------------------
// AuthProvider — nil spec
// ---------------------------------------------------------------------------

func TestEncryptHandler_AuthProviderNilSpec(t *testing.T) {
	_ = setupTestEncryption(t)

	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}

	ap := &AuthProvider{}
	handler := EncryptionHandlers()[domain.AuthProviderKind]
	err := handler(context.Background(), ap, noopEncrypt)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// AuthProvider — non-secret fields preserved
// ---------------------------------------------------------------------------

func TestEncryptHandler_AuthProviderNonSecretPreserved(t *testing.T) {
	mgr := setupTestEncryption(t)

	apiProv := &domain.AuthProvider{
		Metadata: domain.ObjectMeta{Name: strPtr("test-preserve")},
		Spec: func() domain.AuthProviderSpec {
			var spec domain.AuthProviderSpec
			_ = spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
				ClientId:               "my-preserved-client",
				ClientSecret:           "my-secret",
				Issuer:                 "https://issuer.example.com",
				ProviderType:           domain.Oidc,
				OrganizationAssignment: staticOrgAssignment(),
			})
			return spec
		}(),
	}

	modelAP, err := NewAuthProviderFromApiResource(apiProv)
	require.NoError(t, err)

	handler := EncryptionHandlers()[domain.AuthProviderKind]
	err = handler(context.Background(), modelAP, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(modelAP.Spec.Data)
	require.NoError(t, err)
	specStr := string(specJSON)

	require.Contains(t, specStr, "my-preserved-client",
		"clientId should remain in plaintext")
	require.Contains(t, specStr, "https://issuer.example.com",
		"issuer URL should remain in plaintext")
	require.NotContains(t, specStr, "my-secret",
		"clientSecret should be encrypted")
}

// ---------------------------------------------------------------------------
// AuthProvider — map encrypt (GORM partial updates)
// ---------------------------------------------------------------------------

func TestEncryptHandler_AuthProviderMapEncryptsClientSecret(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	var spec domain.AuthProviderSpec
	require.NoError(t, spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
		ClientId:     "my-client",
		ClientSecret: "super-secret-value",
		Issuer:       "https://issuer.example.com",
		ProviderType: domain.Oidc,
	}))

	m := map[string]any{
		"spec": MakeJSONField(spec),
	}

	handler := EncryptionHandlers()[domain.AuthProviderKind]
	err := handler(ctx, m, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(m["spec"])
	require.NoError(t, err)
	specStr := string(specJSON)

	require.NotContains(t, specStr, "super-secret-value",
		"clientSecret should be encrypted in map update")
	require.Contains(t, specStr, "enc:v1:default:",
		"clientSecret should contain encryption prefix")
	require.Contains(t, specStr, "my-client",
		"clientId should remain in plaintext")
	require.Contains(t, specStr, "https://issuer.example.com",
		"issuer should remain in plaintext")
}

func TestEncryptHandler_AuthProviderMapDecryptRoundTrip(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	var spec domain.AuthProviderSpec
	require.NoError(t, spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
		ClientId:     "roundtrip-client",
		ClientSecret: "roundtrip-secret",
		Issuer:       "https://issuer.example.com",
		ProviderType: domain.Oidc,
	}))

	m := map[string]any{
		"spec": MakeJSONField(spec),
	}

	handler := EncryptionHandlers()[domain.AuthProviderKind]
	err := handler(ctx, m, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(m["spec"])
	require.NoError(t, err)

	var decryptedSpec map[string]any
	require.NoError(t, json.Unmarshal(specJSON, &decryptedSpec))

	encSecret := decryptedSpec["clientSecret"].(string)
	require.Contains(t, encSecret, "enc:v1:default:")

	decrypted, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(encSecret))
	require.NoError(t, err)
	require.Equal(t, "roundtrip-secret", string(decrypted))
}

func TestEncryptHandler_AuthProviderMapNoSecret(t *testing.T) {
	_ = setupTestEncryption(t)

	var spec domain.AuthProviderSpec
	require.NoError(t, spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
		ClientId:     "no-secret-client",
		Issuer:       "https://issuer.example.com",
		ProviderType: domain.Oidc,
	}))

	m := map[string]any{
		"spec": MakeJSONField(spec),
	}

	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}

	handler := EncryptionHandlers()[domain.AuthProviderKind]
	err := handler(context.Background(), m, noopEncrypt)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Repository — map encrypt (GORM partial updates)
// ---------------------------------------------------------------------------

func TestEncryptHandler_RepositoryMapEncryptsPassword(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	repoSpec := domain.RepositorySpec{}
	require.NoError(t, repoSpec.FromHttpRepoSpec(domain.HttpRepoSpec{
		Url:  "https://repo.example.com",
		Type: "http",
		HttpConfig: &domain.HttpConfig{
			Password: strPtr("my-http-password"),
			Token:    strPtr("my-http-token"),
		},
	}))

	m := map[string]any{
		"spec": MakeJSONField(repoSpec),
	}

	handler := EncryptionHandlers()[domain.RepositoryKind]
	err := handler(ctx, m, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(m["spec"])
	require.NoError(t, err)
	specStr := string(specJSON)

	require.NotContains(t, specStr, "my-http-password",
		"password should be encrypted")
	require.NotContains(t, specStr, "my-http-token",
		"token should be encrypted")
	require.Contains(t, specStr, "https://repo.example.com",
		"url should remain in plaintext")
}

func TestEncryptHandler_RepositoryMapDecryptRoundTrip(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	repoSpec := domain.RepositorySpec{}
	require.NoError(t, repoSpec.FromHttpRepoSpec(domain.HttpRepoSpec{
		Url:  "https://repo.example.com",
		Type: "http",
		HttpConfig: &domain.HttpConfig{
			Password: strPtr("decrypt-me-password"),
			Token:    strPtr("decrypt-me-token"),
		},
	}))

	m := map[string]any{
		"spec": MakeJSONField(repoSpec),
	}

	handler := EncryptionHandlers()[domain.RepositoryKind]
	err := handler(ctx, m, mgr.ProcessEncryption)
	require.NoError(t, err)

	specJSON, err := json.Marshal(m["spec"])
	require.NoError(t, err)

	var specMap map[string]any
	require.NoError(t, json.Unmarshal(specJSON, &specMap))

	httpConfig := specMap["httpConfig"].(map[string]any)

	encPass := httpConfig["password"].(string)
	require.Contains(t, encPass, "enc:v1:default:")
	decryptedPass, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(encPass))
	require.NoError(t, err)
	require.Equal(t, "decrypt-me-password", string(decryptedPass))

	encToken := httpConfig["token"].(string)
	require.Contains(t, encToken, "enc:v1:default:")
	decryptedToken, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(encToken))
	require.NoError(t, err)
	require.Equal(t, "decrypt-me-token", string(decryptedToken))
}

// ---------------------------------------------------------------------------
// Device — struct encrypt
// ---------------------------------------------------------------------------

func TestEncryptHandler_DeviceStructRenderedFields(t *testing.T) {
	mgr := setupTestEncryption(t)

	configInput := `[{"inline":[{"path":"/etc/file","content":"secret-content","mode":420}],"name":""}]`
	appsInput := `[{"appType":"container","name":"app","image":"img:v1","envVars":{"SECRET":"value123"}}]`

	device := &Device{
		RenderedConfig:       MakeJSONField(json.RawMessage(configInput)),
		RenderedApplications: MakeJSONField(json.RawMessage(appsInput)),
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), device, mgr.ProcessEncryption)
	require.NoError(t, err)

	require.NotContains(t, string(device.RenderedConfig.Data), "secret-content")
	require.Contains(t, string(device.RenderedConfig.Data), "enc:v1:default:")

	require.NotContains(t, string(device.RenderedApplications.Data), "value123")
	require.Contains(t, string(device.RenderedApplications.Data), "enc:v1:default:")
}

func TestEncryptHandler_DeviceStructNilFields(t *testing.T) {
	_ = setupTestEncryption(t)

	device := &Device{}
	handler := EncryptionHandlers()[domain.DeviceKind]
	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}
	err := handler(context.Background(), device, noopEncrypt)
	require.NoError(t, err)
}

func TestEncryptHandler_DeviceStructDecryptRoundTrip(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	configInput := `[{"inline":[{"path":"/etc/file","content":"my-secret","mode":420}],"name":""}]`
	appsInput := `[{"appType":"container","name":"app","image":"img:v1","envVars":{"KEY":"app-secret"}}]`

	device := &Device{
		RenderedConfig:       MakeJSONField(json.RawMessage(configInput)),
		RenderedApplications: MakeJSONField(json.RawMessage(appsInput)),
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(ctx, device, mgr.ProcessEncryption)
	require.NoError(t, err)

	configResult, err := decryptRenderedField(ctx, device.RenderedConfig)
	require.NoError(t, err)
	require.JSONEq(t, configInput, string(configResult))

	appsResult, err := decryptRenderedField(ctx, device.RenderedApplications)
	require.NoError(t, err)
	require.JSONEq(t, appsInput, string(appsResult))
}

func TestEncryptHandler_DeviceStructIdempotent(t *testing.T) {
	mgr := setupTestEncryption(t)

	configInput := `[{"inline":[{"path":"/etc/file","content":"secret","mode":420}],"name":""}]`
	device := &Device{
		RenderedConfig: MakeJSONField(json.RawMessage(configInput)),
	}

	handler := EncryptionHandlers()[domain.DeviceKind]

	err := handler(context.Background(), device, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterFirst := string(device.RenderedConfig.Data)

	err = handler(context.Background(), device, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterSecond := string(device.RenderedConfig.Data)

	require.Equal(t, afterFirst, afterSecond,
		"Encrypting already-encrypted device should be idempotent")
}

func TestEncryptHandler_DeviceStructFailClosed(t *testing.T) {
	_ = setupTestEncryption(t)

	device := &Device{
		RenderedConfig: MakeJSONField(json.RawMessage(`[{"inline":[{"path":"/etc/file","content":"secret","mode":420}],"name":""}]`)),
	}

	errEncrypt := fmt.Errorf("KMS unavailable")
	failingEncrypt := func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errEncrypt
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), device, failingEncrypt)
	require.ErrorIs(t, err, errEncrypt)
}

func TestEncryptHandler_DeviceOnlyConfig(t *testing.T) {
	mgr := setupTestEncryption(t)

	device := &Device{
		RenderedConfig: MakeJSONField(json.RawMessage(`[{"inline":[{"path":"/etc/secret.conf","content":"password=hunter2","mode":420}],"name":""}]`)),
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), device, mgr.ProcessEncryption)
	require.NoError(t, err)

	require.NotContains(t, string(device.RenderedConfig.Data), "password=hunter2")
	require.Contains(t, string(device.RenderedConfig.Data), "enc:v1:default:")
	require.Nil(t, device.RenderedApplications)
}

func TestEncryptHandler_DeviceOnlyApps(t *testing.T) {
	mgr := setupTestEncryption(t)

	device := &Device{
		RenderedApplications: MakeJSONField(json.RawMessage(`[{"appType":"container","name":"myapp","image":"img:v1","envVars":{"DB_PASSWORD":"secret123"}}]`)),
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), device, mgr.ProcessEncryption)
	require.NoError(t, err)

	require.NotContains(t, string(device.RenderedApplications.Data), "secret123")
	require.Contains(t, string(device.RenderedApplications.Data), "enc:v1:default:")
	require.Nil(t, device.RenderedConfig)
}

// ---------------------------------------------------------------------------
// Device — legacy unencrypted data
// ---------------------------------------------------------------------------

func TestEncryptHandler_DeviceLegacyUnencryptedData(t *testing.T) {
	setupTestEncryption(t)
	ctx := context.Background()

	configInput := `[{"inline":[{"path":"/etc/file","content":"plaintext-value","mode":420}],"name":""}]`

	result, err := decryptRenderedField(ctx, MakeJSONField(json.RawMessage(configInput)))
	require.NoError(t, err)
	require.JSONEq(t, configInput, string(result))

	result, err = decryptRenderedField(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, result)

	result, err = decryptRenderedField(ctx, MakeJSONField(json.RawMessage{}))
	require.NoError(t, err)
	require.Nil(t, result)
}

// ---------------------------------------------------------------------------
// Device — map encrypt (GORM partial updates)
// ---------------------------------------------------------------------------

func TestEncryptHandler_DeviceMapRenderedFields(t *testing.T) {
	mgr := setupTestEncryption(t)

	configVal := `[{"inline":[{"path":"/etc/file","content":"secret-content","mode":420}],"name":""}]`
	appsVal := `[{"appType":"container","name":"app","image":"img:v1","envVars":{"SECRET":"value123"}}]`

	m := map[string]any{
		"rendered_config":       &configVal,
		"rendered_applications": &appsVal,
		"resource_version":      42,
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), m, mgr.ProcessEncryption)
	require.NoError(t, err)

	require.NotContains(t, configVal, "secret-content",
		"Config plaintext should be encrypted")
	require.NotContains(t, appsVal, "value123",
		"Apps plaintext should be encrypted")

	require.Equal(t, 42, m["resource_version"],
		"resource_version should not be changed")
}

func TestEncryptHandler_DeviceMapIdempotent(t *testing.T) {
	mgr := setupTestEncryption(t)

	configVal := `[{"inline":[{"path":"/etc/file","content":"secret","mode":420}],"name":""}]`

	m := map[string]any{
		"rendered_config": &configVal,
	}

	handler := EncryptionHandlers()[domain.DeviceKind]

	err := handler(context.Background(), m, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterFirst := configVal

	err = handler(context.Background(), m, mgr.ProcessEncryption)
	require.NoError(t, err)
	afterSecond := configVal

	require.Equal(t, afterFirst, afterSecond,
		"Encrypting already-encrypted map value should be idempotent")
}

func TestEncryptHandler_DeviceMapOnlyConfig(t *testing.T) {
	mgr := setupTestEncryption(t)

	configVal := `[{"inline":[{"path":"/etc/secret.conf","content":"password=hunter2","mode":420}],"name":""}]`

	m := map[string]any{
		"rendered_config":  &configVal,
		"resource_version": 1,
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), m, mgr.ProcessEncryption)
	require.NoError(t, err)

	require.NotContains(t, configVal, "password=hunter2")
}

func TestEncryptHandler_DeviceMapMissingKeys(t *testing.T) {
	_ = setupTestEncryption(t)

	m := map[string]any{
		"annotations":      "some-value",
		"resource_version": 1,
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}
	err := handler(context.Background(), m, noopEncrypt)
	require.NoError(t, err)
}

func TestEncryptHandler_DeviceMapNilValue(t *testing.T) {
	_ = setupTestEncryption(t)

	m := map[string]any{
		"rendered_config": nil,
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}
	err := handler(context.Background(), m, noopEncrypt)
	require.NoError(t, err)
}

func TestEncryptHandler_DeviceMapEmptyString(t *testing.T) {
	_ = setupTestEncryption(t)

	emptyVal := ""
	m := map[string]any{
		"rendered_config": &emptyVal,
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}
	err := handler(context.Background(), m, noopEncrypt)
	require.NoError(t, err)
	require.Equal(t, "", emptyVal, "Empty string should remain empty")
}

func TestEncryptHandler_DeviceMapFailClosed(t *testing.T) {
	_ = setupTestEncryption(t)

	configVal := `[{"inline":[{"path":"/etc/file","content":"secret","mode":420}],"name":""}]`
	m := map[string]any{
		"rendered_config": &configVal,
	}

	errEncrypt := fmt.Errorf("KMS unavailable")
	failingEncrypt := func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errEncrypt
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), m, failingEncrypt)
	require.ErrorIs(t, err, errEncrypt)
}

func TestEncryptHandler_DeviceMapUnsupportedTypeFailsClosed(t *testing.T) {
	_ = setupTestEncryption(t)

	m := map[string]any{
		"rendered_config": "not-a-pointer",
	}

	noopEncrypt := func(_ context.Context, data []byte) ([]byte, error) {
		return data, nil
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(context.Background(), m, noopEncrypt)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported type")
}

func TestEncryptHandler_DeviceMapRoundTrip(t *testing.T) {
	mgr := setupTestEncryption(t)
	ctx := context.Background()

	configInput := `[{"inline":[{"path":"/etc/file","content":"secret-data","mode":420}],"name":""}]`
	appsInput := `[{"appType":"container","name":"app","image":"img:v1","envVars":{"KEY":"secret-val"}}]`

	configStr := configInput
	appsStr := appsInput
	m := map[string]any{
		"rendered_config":       &configStr,
		"rendered_applications": &appsStr,
	}

	handler := EncryptionHandlers()[domain.DeviceKind]
	err := handler(ctx, m, mgr.ProcessEncryption)
	require.NoError(t, err)

	require.NotContains(t, configStr, "secret-data")
	require.NotContains(t, appsStr, "secret-val")

	configResult, err := decryptRenderedField(ctx, MakeJSONField(json.RawMessage(configStr)))
	require.NoError(t, err)
	require.JSONEq(t, configInput, string(configResult))

	appsResult, err := decryptRenderedField(ctx, MakeJSONField(json.RawMessage(appsStr)))
	require.NoError(t, err)
	require.JSONEq(t, appsInput, string(appsResult))
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func setupBenchEncryption(b *testing.B) *encryption.Manager {
	b.Helper()

	dir := b.TempDir()
	keyPath := filepath.Join(dir, "key")

	key, err := crypto.GenerateAES256Key()
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
		b.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.Encryption = &config.EncryptionConfig{
		Keys:        []config.EncryptionKeyConfig{{ID: "default", Path: keyPath}},
		ActiveKeyID: "default",
	}

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	if err := encryption.InitGlobalEncryption(logger, cfg); err != nil {
		b.Fatal(err)
	}

	mgr := encryption.GlobalManager()
	if mgr == nil {
		b.Fatal("encryption manager is nil")
	}
	return mgr
}

func makeRepoModel(b *testing.B, secretSize int) *Repository {
	b.Helper()
	secret := strings.Repeat("s", secretSize)
	apiRepo := &domain.Repository{
		Metadata: domain.ObjectMeta{Name: strPtr("bench-repo")},
		Spec: func() domain.RepositorySpec {
			var spec domain.RepositorySpec
			_ = spec.FromGitRepoSpec(domain.GitRepoSpec{
				HttpConfig: &domain.HttpConfig{
					Username: strPtr("user"),
					Password: strPtr(secret),
					Token:    strPtr(secret),
					TlsKey:   strPtr(secret),
					TlsCrt:   strPtr(secret),
				},
				SshConfig: &domain.SshConfig{
					SshPrivateKey:        strPtr(secret),
					PrivateKeyPassphrase: strPtr(secret),
				},
			})
			return spec
		}(),
	}
	model, err := NewRepositoryFromApiResource(apiRepo)
	if err != nil {
		b.Fatal(err)
	}
	return model
}

func makeAuthProviderModel(b *testing.B, secretSize int) *AuthProvider {
	b.Helper()
	secret := strings.Repeat("s", secretSize)
	apiProv := &domain.AuthProvider{
		Metadata: domain.ObjectMeta{Name: strPtr("bench-ap")},
		Spec: func() domain.AuthProviderSpec {
			var spec domain.AuthProviderSpec
			_ = spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
				ClientId:               "bench-client",
				ClientSecret:           secret,
				Issuer:                 "https://issuer.example.com",
				ProviderType:           domain.Oidc,
				OrganizationAssignment: staticOrgAssignment(),
			})
			return spec
		}(),
	}
	model, err := NewAuthProviderFromApiResource(apiProv)
	if err != nil {
		b.Fatal(err)
	}
	return model
}

func makeDeviceModel(b *testing.B, configSize int) *Device {
	b.Helper()
	content := strings.Repeat("x", configSize)
	configJSON := fmt.Sprintf(`[{"inline":[{"path":"/etc/secret","content":"%s","mode":420}],"name":""}]`, content)
	appsJSON := fmt.Sprintf(`[{"appType":"container","name":"app","image":"img:v1","envVars":{"SECRET":"%s"}}]`, content)
	return &Device{
		RenderedConfig:       MakeJSONField(json.RawMessage(configJSON)),
		RenderedApplications: MakeJSONField(json.RawMessage(appsJSON)),
	}
}

func BenchmarkEncryptHandler_Repository(b *testing.B) {
	mgr := setupBenchEncryption(b)
	handler := EncryptionHandlers()[domain.RepositoryKind]
	ctx := context.Background()

	for _, size := range []int{32, 256, 1024, 4096} {
		b.Run(fmt.Sprintf("secret_%dB", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				model := makeRepoModel(b, size)
				b.StartTimer()
				if err := handler(ctx, model, mgr.ProcessEncryption); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncryptHandler_AuthProvider(b *testing.B) {
	mgr := setupBenchEncryption(b)
	handler := EncryptionHandlers()[domain.AuthProviderKind]
	ctx := context.Background()

	for _, size := range []int{32, 256, 1024, 4096} {
		b.Run(fmt.Sprintf("secret_%dB", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				model := makeAuthProviderModel(b, size)
				b.StartTimer()
				if err := handler(ctx, model, mgr.ProcessEncryption); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncryptHandler_Device(b *testing.B) {
	mgr := setupBenchEncryption(b)
	handler := EncryptionHandlers()[domain.DeviceKind]
	ctx := context.Background()

	for _, size := range []int{256, 1024, 4096, 16384, 65536} {
		b.Run(fmt.Sprintf("payload_%dB", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				model := makeDeviceModel(b, size)
				b.StartTimer()
				if err := handler(ctx, model, mgr.ProcessEncryption); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
