package authn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestOIDCAuth creates an OIDCAuth instance for testing without OIDC discovery
func createTestOIDCAuth(jwksUri string) *OIDCAuth {
	ctx := context.Background()

	// Create a role extractor with a simple groups claim
	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"groups"},
	}
	_ = roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)

	oidcSpec := api.OIDCProviderSpec{
		UsernameClaim: &[]string{"preferred_username"},
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	oidcAuth := &OIDCAuth{
		metadata:      api.ObjectMeta{},
		spec:          oidcSpec,
		jwksUri:       jwksUri,
		client:        &http.Client{Timeout: 5 * time.Second},
		roleExtractor: NewRoleExtractor(roleAssignment, false, log),
		organizationExtractor: &OrganizationExtractor{
			orgConfig: nil, // No org config for basic tests
		},
		log: log,
	}

	// Initialize JWKS cache and mark discovery as complete to bypass lazy initialization
	oidcAuth.jwksCache = jwk.NewCache(ctx)
	_ = oidcAuth.jwksCache.Register(jwksUri, jwk.WithMinRefreshInterval(15*time.Minute))

	// Mark discovery as complete to prevent ensureDiscovery from running
	oidcAuth.discoveryOnce.Do(func() {
		// Discovery already done manually for testing
	})

	return oidcAuth
}

func TestOIDCAuth_parseAndCreateIdentity(t *testing.T) {
	// Create a test JWKS server
	testKey, err := jwk.FromRaw([]byte("test-secret-key-that-is-at-least-32-bytes-long"))
	require.NoError(t, err)
	require.NoError(t, testKey.Set(jwk.KeyIDKey, "test-key-id"))
	require.NoError(t, testKey.Set(jwk.AlgorithmKey, jwa.HS256))

	testKeySet := jwk.NewSet()
	require.NoError(t, testKeySet.AddKey(testKey))

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksBytes, _ := json.Marshal(testKeySet)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(jwksBytes)
		if err != nil {
			http.Error(w, "Failed to write response", http.StatusInternalServerError)
		}
	}))
	defer jwksServer.Close()

	// Create a valid test token with claims
	validToken := jwt.New()
	require.NoError(t, validToken.Set("sub", "test-user-id"))
	require.NoError(t, validToken.Set("preferred_username", "testuser"))
	require.NoError(t, validToken.Set("exp", time.Now().Add(time.Hour).Unix()))
	require.NoError(t, validToken.Set("iat", time.Now().Unix()))
	validTokenBytes, err := jwt.Sign(validToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create a token with wrong type claims
	wrongTypeToken := jwt.New()
	require.NoError(t, wrongTypeToken.Set("sub", "test-user-id"))
	require.NoError(t, wrongTypeToken.Set("preferred_username", []string{"array", "instead", "of", "string"}))
	require.NoError(t, wrongTypeToken.Set("exp", time.Now().Add(time.Hour).Unix()))
	require.NoError(t, wrongTypeToken.Set("iat", time.Now().Unix()))
	wrongTypeTokenBytes, err := jwt.Sign(wrongTypeToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create existing identity for context test
	existingIdentity := &JWTIdentity{
		BaseIdentity: common.BaseIdentity{},
	}
	existingIdentity.SetUID("existing-user")
	existingIdentity.SetUsername("existing-username")

	tests := []struct {
		name             string
		ctx              context.Context
		uri              string
		token            string
		expectError      bool
		expectedUID      string
		expectedUsername string
		errorContains    string
	}{
		{
			name:          "jwk fetch failure",
			ctx:           context.Background(),
			uri:           "http://invalid-url-that-does-not-exist",
			token:         string(validTokenBytes),
			expectError:   true,
			errorContains: "failed to get JWK set from cache",
		},
		{
			name:          "invalid token",
			ctx:           context.Background(),
			uri:           jwksServer.URL,
			token:         "invalid.jwt.token",
			expectError:   true,
			errorContains: "failed to parse JWT token",
		},
		{
			name:             "valid token with both claims",
			ctx:              context.Background(),
			uri:              jwksServer.URL,
			token:            string(validTokenBytes),
			expectError:      false,
			expectedUID:      "test-user-id",
			expectedUsername: "testuser",
		},
		{
			name:             "valid token with wrong type claims",
			ctx:              context.Background(),
			uri:              jwksServer.URL,
			token:            string(wrongTypeTokenBytes),
			expectError:      false,
			expectedUID:      "test-user-id",
			expectedUsername: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oidcAuth := createTestOIDCAuth(tt.uri)
			identity, err := oidcAuth.parseAndCreateIdentity(tt.ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, identity)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, identity)
				assert.Equal(t, tt.expectedUID, identity.GetUID())
				assert.Equal(t, tt.expectedUsername, identity.GetUsername())

				// Ensure the parsed token is set (except for existing identity case)
				if tt.name != "existing identity in context" {
					assert.NotNil(t, identity.parsedToken)
				}
			}
		})
	}
}

func TestOIDCAuth_extractOrganizations(t *testing.T) {
	// Create a test JWKS server
	testKey, err := jwk.FromRaw([]byte("test-secret-key-that-is-at-least-32-bytes-long"))
	require.NoError(t, err)
	require.NoError(t, testKey.Set(jwk.KeyIDKey, "test-key-id"))
	require.NoError(t, testKey.Set(jwk.AlgorithmKey, jwa.HS256))

	testKeySet := jwk.NewSet()
	require.NoError(t, testKeySet.AddKey(testKey))

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksBytes, _ := json.Marshal(testKeySet)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(jwksBytes)
		if err != nil {
			http.Error(w, "Failed to write response", http.StatusInternalServerError)
		}
	}))
	defer jwksServer.Close()

	// Create a test token with organization claim
	now := time.Now()
	token, err := jwt.NewBuilder().
		Issuer("https://test-issuer.com").
		Subject("test-user-id").
		Audience([]string{"test-audience"}).
		IssuedAt(now).
		Expiration(now.Add(time.Hour)).
		Claim("preferred_username", "testuser").
		Claim("groups", []string{"admin", "user"}).
		Claim("organization", "test-org").
		Build()
	require.NoError(t, err)

	tokenBytes, err := jwt.Sign(token, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	tests := []struct {
		name         string
		orgConfig    *common.AuthOrganizationsConfig
		expectedOrgs []common.ReportedOrganization
	}{
		{
			name:         "no org config",
			orgConfig:    nil,
			expectedOrgs: []common.ReportedOrganization{},
		},
		{
			name:         "organizations disabled",
			orgConfig:    nil,
			expectedOrgs: []common.ReportedOrganization{},
		},
		{
			name: "static organization assignment",
			orgConfig: func() *common.AuthOrganizationsConfig {
				assignment := api.AuthOrganizationAssignment{}
				staticAssignment := api.AuthStaticOrganizationAssignment{
					Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
					OrganizationName: "static-org",
				}
				_ = assignment.FromAuthStaticOrganizationAssignment(staticAssignment)
				return &common.AuthOrganizationsConfig{
					OrganizationAssignment: &assignment,
				}
			}(),
			expectedOrgs: []common.ReportedOrganization{{Name: "static-org", IsInternalID: false, ID: "static-org", Roles: []string{"admin", "user"}}},
		},
		{
			name: "dynamic organization assignment",
			orgConfig: func() *common.AuthOrganizationsConfig {
				assignment := api.AuthOrganizationAssignment{}
				dynamicAssignment := api.AuthDynamicOrganizationAssignment{
					Type:      api.AuthDynamicOrganizationAssignmentTypeDynamic,
					ClaimPath: []string{"organization"},
				}
				_ = assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment)
				return &common.AuthOrganizationsConfig{
					OrganizationAssignment: &assignment,
				}
			}(),
			expectedOrgs: []common.ReportedOrganization{{Name: "test-org", IsInternalID: false, ID: "test-org", Roles: []string{"admin", "user"}}},
		},
		{
			name: "per-user organization assignment",
			orgConfig: func() *common.AuthOrganizationsConfig {
				assignment := api.AuthOrganizationAssignment{}
				perUserAssignment := api.AuthPerUserOrganizationAssignment{
					Type:                   api.PerUser,
					OrganizationNamePrefix: stringPtr("user-"),
					OrganizationNameSuffix: stringPtr("-org"),
				}
				_ = assignment.FromAuthPerUserOrganizationAssignment(perUserAssignment)
				return &common.AuthOrganizationsConfig{
					OrganizationAssignment: &assignment,
				}
			}(),
			expectedOrgs: []common.ReportedOrganization{{Name: "user-testuser-org", IsInternalID: false, ID: "user-testuser-org", Roles: []string{"admin", "user"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oidcAuth := createTestOIDCAuth(jwksServer.URL)
			oidcAuth.orgConfig = tt.orgConfig
			oidcAuth.organizationExtractor = &OrganizationExtractor{
				orgConfig: tt.orgConfig,
			}

			identity, err := oidcAuth.parseAndCreateIdentity(context.Background(), string(tokenBytes))
			require.NoError(t, err)

			organizations := identity.GetOrganizations()
			assert.Equal(t, tt.expectedOrgs, organizations)
		})
	}
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}
