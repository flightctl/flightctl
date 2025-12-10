package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create a test OAuth2Auth instance
func createTestOAuth2Auth(t *testing.T, spec api.OAuth2ProviderSpec) *OAuth2Auth {
	t.Helper()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	metadata := api.ObjectMeta{
		Name: lo.ToPtr("test-oauth2-provider"),
	}

	oauth2Auth, err := NewOAuth2Auth(metadata, spec, nil, log)
	require.NoError(t, err)

	// Start the provider with a background context
	ctx := context.Background()
	err = oauth2Auth.Start(ctx)
	require.NoError(t, err)

	return oauth2Auth
}

// Helper function to create a basic OAuth2ProviderSpec
func createBasicOAuth2Spec() api.OAuth2ProviderSpec {
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "test-org",
	}
	_ = assignment.FromAuthStaticOrganizationAssignment(staticAssignment)

	roleAssignment := api.AuthRoleAssignment{}
	staticRoleAssignment := api.AuthStaticRoleAssignment{
		Type:  api.AuthStaticRoleAssignmentTypeStatic,
		Roles: []string{"viewer"},
	}
	_ = roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)

	spec := api.OAuth2ProviderSpec{
		ProviderType:           api.Oauth2,
		AuthorizationUrl:       "https://oauth2.example.com/authorize",
		TokenUrl:               "https://oauth2.example.com/token",
		UserinfoUrl:            "https://oauth2.example.com/userinfo",
		ClientId:               "test-client-id",
		ClientSecret:           lo.ToPtr("test-client-secret"),
		Enabled:                lo.ToPtr(true),
		OrganizationAssignment: assignment,
		RoleAssignment:         roleAssignment,
	}
	// Set defaults for tests (validation would set these for API-created providers)
	defaultUsernameClaim := []string{"preferred_username"}
	spec.UsernameClaim = &defaultUsernameClaim
	spec.Issuer = &spec.AuthorizationUrl
	return spec
}

func TestOAuth2Auth_NewOAuth2Auth(t *testing.T) {
	log := logrus.New()

	tests := []struct {
		name        string
		metadata    api.ObjectMeta
		spec        api.OAuth2ProviderSpec
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid OAuth2 spec",
			metadata:    api.ObjectMeta{Name: lo.ToPtr("test-provider")},
			spec:        createBasicOAuth2Spec(),
			expectError: false,
		},
		{
			name:     "missing authorizationUrl",
			metadata: api.ObjectMeta{Name: lo.ToPtr("test-provider")},
			spec: func() api.OAuth2ProviderSpec {
				spec := createBasicOAuth2Spec()
				spec.AuthorizationUrl = ""
				return spec
			}(),
			expectError: true,
			errorMsg:    "authorizationUrl is required",
		},
		{
			name:     "missing tokenUrl",
			metadata: api.ObjectMeta{Name: lo.ToPtr("test-provider")},
			spec: func() api.OAuth2ProviderSpec {
				spec := createBasicOAuth2Spec()
				spec.TokenUrl = ""
				return spec
			}(),
			expectError: true,
			errorMsg:    "tokenUrl is required",
		},
		{
			name:     "missing userinfoUrl",
			metadata: api.ObjectMeta{Name: lo.ToPtr("test-provider")},
			spec: func() api.OAuth2ProviderSpec {
				spec := createBasicOAuth2Spec()
				spec.UserinfoUrl = ""
				return spec
			}(),
			expectError: true,
			errorMsg:    "userinfoUrl is required",
		},
		{
			name:     "missing clientId",
			metadata: api.ObjectMeta{Name: lo.ToPtr("test-provider")},
			spec: func() api.OAuth2ProviderSpec {
				spec := createBasicOAuth2Spec()
				spec.ClientId = ""
				return spec
			}(),
			expectError: true,
			errorMsg:    "clientId is required",
		},
		{
			name:     "missing clientSecret",
			metadata: api.ObjectMeta{Name: lo.ToPtr("test-provider")},
			spec: func() api.OAuth2ProviderSpec {
				spec := createBasicOAuth2Spec()
				spec.ClientSecret = nil
				return spec
			}(),
			expectError: true,
			errorMsg:    "clientSecret is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oauth2Auth, err := NewOAuth2Auth(tt.metadata, tt.spec, nil, log)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, oauth2Auth)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, oauth2Auth)
			}
		})
	}
}

func TestOAuth2Auth_IntrospectRFC7662(t *testing.T) {
	// Create a test RFC 7662 introspection server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		// Check Basic Auth
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "test-client-id", username)
		assert.Equal(t, "test-client-secret", password)

		// Parse form data
		err := r.ParseForm()
		assert.NoError(t, err)
		token := r.FormValue("token")

		w.Header().Set("Content-Type", "application/json")
		if token == "valid-token" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"active": true,
			})
		} else if token == "invalid-token" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"active": false,
			})
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	// Create OAuth2Auth with RFC 7662 introspection
	spec := createBasicOAuth2Spec()
	introspection := &api.OAuth2Introspection{}
	rfc7662Spec := api.Rfc7662IntrospectionSpec{
		Type: api.Rfc7662,
		Url:  server.URL,
	}
	err := introspection.FromRfc7662IntrospectionSpec(rfc7662Spec)
	require.NoError(t, err)
	spec.Introspection = introspection

	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	tests := []struct {
		name        string
		token       string
		expectError bool
	}{
		{
			name:        "valid token",
			token:       "valid-token",
			expectError: false,
		},
		{
			name:        "invalid token",
			token:       "invalid-token",
			expectError: true,
		},
		{
			name:        "malformed token",
			token:       "malformed",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := oauth2Auth.ValidateToken(ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOAuth2Auth_IntrospectGitHub(t *testing.T) {
	// Create a test GitHub introspection server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "/applications/test-client-id/token")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))

		// Check Basic Auth
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "test-client-id", username)
		assert.Equal(t, "test-client-secret", password)

		// Parse request body
		var reqBody map[string]string
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		assert.NoError(t, err)
		token := reqBody["access_token"]

		if token == "valid-token" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    123,
				"token": token,
			})
		} else if token == "invalid-token" {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	// Create OAuth2Auth with GitHub introspection
	spec := createBasicOAuth2Spec()
	introspection := &api.OAuth2Introspection{}
	githubSpec := api.GitHubIntrospectionSpec{
		Type: api.Github,
		Url:  lo.ToPtr(server.URL),
	}
	err := introspection.FromGitHubIntrospectionSpec(githubSpec)
	require.NoError(t, err)
	spec.Introspection = introspection

	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	tests := []struct {
		name        string
		token       string
		expectError bool
	}{
		{
			name:        "valid token",
			token:       "valid-token",
			expectError: false,
		},
		{
			name:        "invalid token",
			token:       "invalid-token",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := oauth2Auth.ValidateToken(ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOAuth2Auth_IntrospectJWT(t *testing.T) {
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
		_, _ = w.Write(jwksBytes)
	}))
	defer jwksServer.Close()

	// Create OAuth2Auth with JWT introspection
	spec := createBasicOAuth2Spec()
	spec.Issuer = lo.ToPtr("https://test-issuer.com")
	introspection := &api.OAuth2Introspection{}
	jwtSpec := api.JwtIntrospectionSpec{
		Type:    api.Jwt,
		JwksUrl: jwksServer.URL,
	}
	err = introspection.FromJwtIntrospectionSpec(jwtSpec)
	require.NoError(t, err)
	spec.Introspection = introspection

	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	// Create valid JWT token
	now := time.Now()
	validToken, err := jwt.NewBuilder().
		Issuer(*spec.Issuer).
		Subject("test-user").
		Audience([]string{spec.ClientId}).
		IssuedAt(now).
		Expiration(now.Add(time.Hour)).
		Build()
	require.NoError(t, err)
	validTokenBytes, err := jwt.Sign(validToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create expired JWT token
	expiredToken, err := jwt.NewBuilder().
		Issuer(*spec.Issuer).
		Subject("test-user").
		Audience([]string{spec.ClientId}).
		IssuedAt(now.Add(-2 * time.Hour)).
		Expiration(now.Add(-time.Hour)).
		Build()
	require.NoError(t, err)
	expiredTokenBytes, err := jwt.Sign(expiredToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create token with wrong issuer
	wrongIssuerToken, err := jwt.NewBuilder().
		Issuer("https://wrong-issuer.com").
		Subject("test-user").
		Audience([]string{spec.ClientId}).
		IssuedAt(now).
		Expiration(now.Add(time.Hour)).
		Build()
	require.NoError(t, err)
	wrongIssuerTokenBytes, err := jwt.Sign(wrongIssuerToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create token with wrong audience
	wrongAudienceToken, err := jwt.NewBuilder().
		Issuer(*spec.Issuer).
		Subject("test-user").
		Audience([]string{"wrong-audience"}).
		IssuedAt(now).
		Expiration(now.Add(time.Hour)).
		Build()
	require.NoError(t, err)
	wrongAudienceTokenBytes, err := jwt.Sign(wrongAudienceToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	tests := []struct {
		name        string
		token       string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid JWT token",
			token:       string(validTokenBytes),
			expectError: false,
		},
		{
			name:        "expired JWT token",
			token:       string(expiredTokenBytes),
			expectError: true,
			errorMsg:    "failed to validate JWT token",
		},
		{
			name:        "wrong issuer",
			token:       string(wrongIssuerTokenBytes),
			expectError: true,
			errorMsg:    "token issuer",
		},
		{
			name:        "wrong audience",
			token:       string(wrongAudienceTokenBytes),
			expectError: true,
			errorMsg:    "token audience",
		},
		{
			name:        "not a JWT token",
			token:       "not-a-jwt-token",
			expectError: true,
			errorMsg:    "failed to parse JWT token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := oauth2Auth.ValidateToken(ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOAuth2Auth_IntrospectJWT_CustomIssuerAndAudience(t *testing.T) {
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
		_, _ = w.Write(jwksBytes)
	}))
	defer jwksServer.Close()

	// Create OAuth2Auth with JWT introspection with custom issuer and audience
	spec := createBasicOAuth2Spec()
	introspection := &api.OAuth2Introspection{}
	jwtSpec := api.JwtIntrospectionSpec{
		Type:     api.Jwt,
		JwksUrl:  jwksServer.URL,
		Issuer:   lo.ToPtr("https://custom-issuer.com"),
		Audience: lo.ToPtr([]string{"custom-audience-1", "custom-audience-2"}),
	}
	err = introspection.FromJwtIntrospectionSpec(jwtSpec)
	require.NoError(t, err)
	spec.Introspection = introspection

	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	// Create valid JWT token with custom issuer and audience
	now := time.Now()
	validToken, err := jwt.NewBuilder().
		Issuer(*jwtSpec.Issuer).
		Subject("test-user").
		Audience([]string{"custom-audience-1"}).
		IssuedAt(now).
		Expiration(now.Add(time.Hour)).
		Build()
	require.NoError(t, err)
	validTokenBytes, err := jwt.Sign(validToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	ctx := context.Background()
	err = oauth2Auth.ValidateToken(ctx, string(validTokenBytes))
	assert.NoError(t, err)
}

func TestOAuth2Auth_GetIdentity(t *testing.T) {
	// Create a test userinfo server
	userinfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		w.Header().Set("Content-Type", "application/json")

		if token == "valid-token" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sub":                "user-123",
				"preferred_username": "testuser",
				"email":              "testuser@example.com",
			})
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer userinfoServer.Close()

	// Create OAuth2Auth
	spec := createBasicOAuth2Spec()
	spec.UserinfoUrl = userinfoServer.URL

	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	tests := []struct {
		name         string
		token        string
		expectError  bool
		expectedUID  string
		expectedUser string
	}{
		{
			name:         "valid token",
			token:        "valid-token",
			expectError:  false,
			expectedUID:  "testuser",
			expectedUser: "testuser",
		},
		{
			name:        "invalid token",
			token:       "invalid-token",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			identity, err := oauth2Auth.GetIdentity(ctx, tt.token)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, identity)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, identity)
				assert.Equal(t, tt.expectedUID, identity.GetUID())
				assert.Equal(t, tt.expectedUser, identity.GetUsername())

				// Verify caching - second call should use cache
				identity2, err := oauth2Auth.GetIdentity(ctx, tt.token)
				assert.NoError(t, err)
				assert.NotNil(t, identity2)
				assert.Equal(t, tt.expectedUID, identity2.GetUID())
			}
		})
	}
}

func TestOAuth2Auth_GetIdentity_FiltersFlightctlAdmin_NotCreatedBySuperAdmin(t *testing.T) {
	// Create a test userinfo server that returns flightctl-admin role
	userinfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		w.Header().Set("Content-Type", "application/json")

		if token == "token-with-admin-role" {
			// Return userinfo with flightctl-admin role in roles claim
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sub":                "user-123",
				"preferred_username": "testuser",
				"email":              "testuser@example.com",
				"roles": []interface{}{
					api.ExternalRoleAdmin,
					api.ExternalRoleViewer,
					api.ExternalRoleOperator,
				},
			})
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer userinfoServer.Close()

	// Create OAuth2Auth with dynamic role mapping (NOT created by super admin - no annotation)
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "test-org",
	}
	_ = assignment.FromAuthStaticOrganizationAssignment(staticAssignment)

	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"roles"},
	}
	_ = roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)

	spec := api.OAuth2ProviderSpec{
		ProviderType:           api.Oauth2,
		AuthorizationUrl:       "https://oauth2.example.com/authorize",
		TokenUrl:               "https://oauth2.example.com/token",
		UserinfoUrl:            userinfoServer.URL,
		ClientId:               "test-client-id",
		ClientSecret:           lo.ToPtr("test-client-secret"),
		Enabled:                lo.ToPtr(true),
		OrganizationAssignment: assignment,
		RoleAssignment:         roleAssignment,
	}
	defaultUsernameClaim := []string{"preferred_username"}
	spec.UsernameClaim = &defaultUsernameClaim
	spec.Issuer = &spec.AuthorizationUrl

	// Create metadata WITHOUT the createdBySuperAdmin annotation (simulating non-super-admin creation)
	metadata := api.ObjectMeta{
		Name: lo.ToPtr("test-oauth2-provider"),
		// No annotations - not created by super admin
	}

	oauth2Auth, err := NewOAuth2Auth(metadata, spec, nil, logrus.New())
	require.NoError(t, err)

	ctx := context.Background()
	err = oauth2Auth.Start(ctx)
	require.NoError(t, err)
	defer oauth2Auth.Stop()

	// Get identity with token that has flightctl-admin role in userinfo
	identity, err := oauth2Auth.GetIdentity(ctx, "token-with-admin-role")
	require.NoError(t, err)
	require.NotNil(t, identity)

	// Verify that the identity does NOT have super admin privileges
	// Even though userinfo returned flightctl-admin, it should be filtered out
	assert.False(t, identity.IsSuperAdmin(), "Identity should NOT have super admin privileges when AP was not created by super admin")

	// Verify that other roles are still present
	organizations := identity.GetOrganizations()
	require.Len(t, organizations, 1, "Should have one organization")
	assert.Equal(t, "test-org", organizations[0].Name)
	// flightctl-admin should be filtered out, but other roles should be present
	assert.NotContains(t, organizations[0].Roles, api.ExternalRoleAdmin, "flightctl-admin role should be filtered out")
	assert.Contains(t, organizations[0].Roles, api.ExternalRoleViewer, "viewer role should be present")
	assert.Contains(t, organizations[0].Roles, api.ExternalRoleOperator, "operator role should be present")
}

func TestOAuth2Auth_GetIdentity_AllowsFlightctlAdmin_CreatedBySuperAdmin(t *testing.T) {
	// Create a test userinfo server that returns flightctl-admin role
	userinfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		w.Header().Set("Content-Type", "application/json")

		if token == "token-with-admin-role" {
			// Return userinfo with flightctl-admin role in roles claim
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sub":                "user-123",
				"preferred_username": "adminuser",
				"email":              "adminuser@example.com",
				"roles": []interface{}{
					api.ExternalRoleAdmin,
					api.ExternalRoleViewer,
				},
			})
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer userinfoServer.Close()

	// Create OAuth2Auth with dynamic role mapping (created by super admin - with annotation)
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "test-org",
	}
	_ = assignment.FromAuthStaticOrganizationAssignment(staticAssignment)

	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"roles"},
	}
	_ = roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)

	spec := api.OAuth2ProviderSpec{
		ProviderType:           api.Oauth2,
		AuthorizationUrl:       "https://oauth2.example.com/authorize",
		TokenUrl:               "https://oauth2.example.com/token",
		UserinfoUrl:            userinfoServer.URL,
		ClientId:               "test-client-id",
		ClientSecret:           lo.ToPtr("test-client-secret"),
		Enabled:                lo.ToPtr(true),
		OrganizationAssignment: assignment,
		RoleAssignment:         roleAssignment,
	}
	defaultUsernameClaim := []string{"preferred_username"}
	spec.UsernameClaim = &defaultUsernameClaim
	spec.Issuer = &spec.AuthorizationUrl

	// Create metadata WITH the createdBySuperAdmin annotation (simulating super-admin creation)
	annotations := map[string]string{
		api.AuthProviderAnnotationCreatedBySuperAdmin: "true",
	}
	metadata := api.ObjectMeta{
		Name:        lo.ToPtr("test-oauth2-provider"),
		Annotations: &annotations,
	}

	oauth2Auth, err := NewOAuth2Auth(metadata, spec, nil, logrus.New())
	require.NoError(t, err)

	ctx := context.Background()
	err = oauth2Auth.Start(ctx)
	require.NoError(t, err)
	defer oauth2Auth.Stop()

	// Get identity with token that has flightctl-admin role in userinfo
	identity, err := oauth2Auth.GetIdentity(ctx, "token-with-admin-role")
	require.NoError(t, err)
	require.NotNil(t, identity)

	// Verify that the identity DOES have super admin privileges
	// Since AP was created by super admin, flightctl-admin role should be allowed
	assert.True(t, identity.IsSuperAdmin(), "Identity should have super admin privileges when AP was created by super admin")

	// Verify that flightctl-admin role is converted to flightctl-org-admin in the organization
	organizations := identity.GetOrganizations()
	require.Len(t, organizations, 1, "Should have one organization")
	assert.Equal(t, "test-org", organizations[0].Name)
	// flightctl-admin is converted to flightctl-org-admin in BuildReportedOrganizations
	assert.Contains(t, organizations[0].Roles, api.ExternalRoleOrgAdmin, "flightctl-org-admin role should be present (converted from flightctl-admin)")
	assert.Contains(t, organizations[0].Roles, api.ExternalRoleViewer, "viewer role should be present")
}

func TestOAuth2Auth_IntrospectJWT_CacheLifecycle(t *testing.T) {
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
		_, _ = w.Write(jwksBytes)
	}))
	defer jwksServer.Close()

	// Create OAuth2Auth with JWT introspection
	spec := createBasicOAuth2Spec()
	spec.Issuer = lo.ToPtr("https://test-issuer.com")
	introspection := &api.OAuth2Introspection{}
	jwtSpec := api.JwtIntrospectionSpec{
		Type:    api.Jwt,
		JwksUrl: jwksServer.URL,
	}
	err = introspection.FromJwtIntrospectionSpec(jwtSpec)
	require.NoError(t, err)
	spec.Introspection = introspection

	oauth2Auth := createTestOAuth2Auth(t, spec)

	// Create valid JWT token
	now := time.Now()
	validToken, err := jwt.NewBuilder().
		Issuer(*spec.Issuer).
		Subject("test-user").
		Audience([]string{spec.ClientId}).
		IssuedAt(now).
		Expiration(now.Add(time.Hour)).
		Build()
	require.NoError(t, err)
	validTokenBytes, err := jwt.Sign(validToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Validate token (this initializes the JWKS cache)
	ctx := context.Background()
	err = oauth2Auth.ValidateToken(ctx, string(validTokenBytes))
	assert.NoError(t, err)

	// Verify cache is initialized
	assert.NotNil(t, oauth2Auth.jwksCache)

	// Stop the provider
	oauth2Auth.Stop()

	// The cache should still be accessible but the provider context is cancelled
	// This test verifies the provider lifecycle doesn't break the JWKS cache
	assert.NotNil(t, oauth2Auth.jwksCache)
}

func TestOAuth2Auth_IntrospectJWT_ProviderContextUsed(t *testing.T) {
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
		_, _ = w.Write(jwksBytes)
	}))
	defer jwksServer.Close()

	// Create OAuth2Auth with JWT introspection
	spec := createBasicOAuth2Spec()
	spec.Issuer = lo.ToPtr("https://test-issuer.com")
	introspection := &api.OAuth2Introspection{}
	jwtSpec := api.JwtIntrospectionSpec{
		Type:    api.Jwt,
		JwksUrl: jwksServer.URL,
	}
	err = introspection.FromJwtIntrospectionSpec(jwtSpec)
	require.NoError(t, err)
	spec.Introspection = introspection

	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	// Create valid JWT token
	now := time.Now()
	validToken, err := jwt.NewBuilder().
		Issuer(*spec.Issuer).
		Subject("test-user").
		Audience([]string{spec.ClientId}).
		IssuedAt(now).
		Expiration(now.Add(time.Hour)).
		Build()
	require.NoError(t, err)
	validTokenBytes, err := jwt.Sign(validToken, jwt.WithKey(jwa.HS256, testKey))
	require.NoError(t, err)

	// Create a short-lived request context
	requestCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Validate token (JWKS cache was initialized during Start(), not tied to request context)
	err = oauth2Auth.ValidateToken(requestCtx, string(validTokenBytes))
	assert.NoError(t, err)

	// Wait for request context to expire
	time.Sleep(150 * time.Millisecond)

	// JWKS cache should still work because it uses the provider's lifecycle context, not the expired requestCtx
	newRequestCtx := context.Background()
	err = oauth2Auth.ValidateToken(newRequestCtx, string(validTokenBytes))
	assert.NoError(t, err, "JWKS cache should continue working after request context expires")
}

func TestOAuth2Auth_StartStop(t *testing.T) {
	spec := createBasicOAuth2Spec()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	metadata := api.ObjectMeta{
		Name: lo.ToPtr("test-oauth2-provider"),
	}

	oauth2Auth, err := NewOAuth2Auth(metadata, spec, nil, log)
	require.NoError(t, err)

	// Test Start
	ctx := context.Background()
	err = oauth2Auth.Start(ctx)
	assert.NoError(t, err)
	assert.True(t, oauth2Auth.started)
	assert.NotNil(t, oauth2Auth.cancel)

	// Test double Start
	err = oauth2Auth.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	// Test Stop
	oauth2Auth.Stop()

	// Test double Stop (should be idempotent)
	oauth2Auth.Stop()
}

func TestOAuth2Auth_GetAuthToken(t *testing.T) {
	spec := createBasicOAuth2Spec()
	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	tests := []struct {
		name          string
		authHeader    string
		expectError   bool
		expectedToken string
	}{
		{
			name:          "valid Bearer token",
			authHeader:    "Bearer valid-token-123",
			expectError:   false,
			expectedToken: "valid-token-123",
		},
		{
			name:        "missing Authorization header",
			authHeader:  "",
			expectError: true,
		},
		{
			name:        "invalid format - no Bearer prefix",
			authHeader:  "Token abc123",
			expectError: true,
		},
		{
			name:        "empty token",
			authHeader:  "Bearer ",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			token, err := oauth2Auth.GetAuthToken(req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedToken, token)
			}
		})
	}
}

func TestOAuth2Auth_GetAuthConfig(t *testing.T) {
	spec := createBasicOAuth2Spec()
	spec.Issuer = lo.ToPtr("https://test-issuer.com")

	oauth2Auth := createTestOAuth2Auth(t, spec)
	defer oauth2Auth.Stop()

	authConfig := oauth2Auth.GetAuthConfig()

	assert.NotNil(t, authConfig)
	assert.Equal(t, api.AuthConfigAPIVersion, authConfig.ApiVersion)
	assert.NotNil(t, authConfig.OrganizationsEnabled)
	assert.True(t, *authConfig.OrganizationsEnabled)
	assert.NotNil(t, authConfig.Providers)
	assert.Len(t, *authConfig.Providers, 1)

	provider := (*authConfig.Providers)[0]
	assert.Equal(t, oauth2Auth.metadata.Name, authConfig.DefaultProvider)

	// Verify the spec can be converted back to OAuth2ProviderSpec
	oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
	assert.NoError(t, err)
	assert.Equal(t, spec.ClientId, oauth2Spec.ClientId)
	assert.Equal(t, spec.AuthorizationUrl, oauth2Spec.AuthorizationUrl)
	// Note: Client secret is present in the struct but would be masked during JSON marshaling
	assert.NotNil(t, oauth2Spec.ClientSecret, "client secret is present in struct")
}

func TestOAuth2Auth_TLSConfig(t *testing.T) {
	spec := createBasicOAuth2Spec()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	metadata := api.ObjectMeta{
		Name: lo.ToPtr("test-oauth2-provider"),
	}

	// Create with custom TLS config
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test code
	}

	oauth2Auth, err := NewOAuth2Auth(metadata, spec, tlsConfig, log)
	require.NoError(t, err)

	// Verify TLS config is set
	assert.NotNil(t, oauth2Auth.tlsConfig)
	assert.True(t, oauth2Auth.tlsConfig.InsecureSkipVerify)
}
