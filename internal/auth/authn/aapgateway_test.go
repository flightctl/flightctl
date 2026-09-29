package authn

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapRoleAssignmentsToOrgRoles_WithPrefix(t *testing.T) {
	tests := []struct {
		name            string
		roleAssignments []*aap.AAPRoleUserAssignment
		prefix          *string
		expected        map[string][]string
	}{
		{
			name:            "nil prefix returns unprefixed org names",
			roleAssignments: createUserRoleAssignments("OrgA", "admin"),
			prefix:          nil,
			expected:        map[string][]string{"OrgA": {"admin"}},
		},
		{
			name:            "empty prefix returns unprefixed org names",
			roleAssignments: createUserRoleAssignments("OrgA", "admin"),
			prefix:          lo.ToPtr(""),
			expected:        map[string][]string{"OrgA": {"admin"}},
		},
		{
			name:            "prefix is prepended to org name",
			roleAssignments: createUserRoleAssignments("OrgA", "admin"),
			prefix:          lo.ToPtr("aap-"),
			expected:        map[string][]string{"aap-OrgA": {"admin"}},
		},
		{
			name: "prefix applied to multiple orgs",
			roleAssignments: append(
				createUserRoleAssignments("OrgA", "admin"),
				createUserRoleAssignments("OrgB", "viewer")...,
			),
			prefix: lo.ToPtr("aap-"),
			expected: map[string][]string{
				"aap-OrgA": {"admin"},
				"aap-OrgB": {"viewer"},
			},
		},
		{
			name: "multiple roles for same org with prefix",
			roleAssignments: append(
				createUserRoleAssignments("OrgA", "admin"),
				createUserRoleAssignments("OrgA", "viewer")...,
			),
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{"aap-OrgA": {"admin", "viewer"}},
		},
		{
			name: "non-organization content types are ignored",
			roleAssignments: []*aap.AAPRoleUserAssignment{
				{
					ContentType: "shared.team",
					SummaryFields: aap.AAPRoleUserAssignmentSummaryFields{
						ContentObject:  aap.AAPContentObject{Name: "SomeTeam"},
						RoleDefinition: aap.AAPRoleDefinition{Name: "admin"},
					},
				},
			},
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{},
		},
	}

	auth := &AapGatewayAuth{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := auth.mapRoleAssignmentsToOrgRoles(tt.roleAssignments, tt.prefix)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMapTeamRoleAssignmentsToOrgRoles_WithPrefix(t *testing.T) {
	tests := []struct {
		name            string
		roleAssignments []*aap.AAPRoleTeamAssignment
		prefix          *string
		expected        map[string][]string
	}{
		{
			name:            "nil prefix returns unprefixed org names",
			roleAssignments: createTeamRoleAssignments("OrgA", "operator"),
			prefix:          nil,
			expected:        map[string][]string{"OrgA": {"operator"}},
		},
		{
			name:            "empty prefix returns unprefixed org names",
			roleAssignments: createTeamRoleAssignments("OrgA", "operator"),
			prefix:          lo.ToPtr(""),
			expected:        map[string][]string{"OrgA": {"operator"}},
		},
		{
			name:            "prefix is prepended to org name",
			roleAssignments: createTeamRoleAssignments("OrgA", "operator"),
			prefix:          lo.ToPtr("aap-"),
			expected:        map[string][]string{"aap-OrgA": {"operator"}},
		},
		{
			name: "prefix applied to multiple orgs from teams",
			roleAssignments: append(
				createTeamRoleAssignments("OrgA", "operator"),
				createTeamRoleAssignments("OrgB", "viewer")...,
			),
			prefix: lo.ToPtr("aap-"),
			expected: map[string][]string{
				"aap-OrgA": {"operator"},
				"aap-OrgB": {"viewer"},
			},
		},
		{
			name: "duplicate roles are not added",
			roleAssignments: append(
				createTeamRoleAssignments("OrgA", "viewer"),
				createTeamRoleAssignments("OrgA", "viewer")...,
			),
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{"aap-OrgA": {"viewer"}},
		},
		{
			name: "non-organization content types are ignored",
			roleAssignments: []*aap.AAPRoleTeamAssignment{
				{
					ContentType: "shared.team",
					SummaryFields: aap.AAPRoleTeamAssignmentSummaryFields{
						ContentObject:  aap.AAPContentObject{Name: "SomeTeam"},
						RoleDefinition: aap.AAPRoleDefinition{Name: "admin"},
					},
				},
			},
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{},
		},
	}

	auth := &AapGatewayAuth{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := auth.mapTeamRoleAssignmentsToOrgRoles(tt.roleAssignments, tt.prefix)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func createUserRoleAssignments(orgName, roleName string) []*aap.AAPRoleUserAssignment {
	return []*aap.AAPRoleUserAssignment{
		{
			ContentType: "shared.organization",
			SummaryFields: aap.AAPRoleUserAssignmentSummaryFields{
				ContentObject:  aap.AAPContentObject{Name: orgName},
				RoleDefinition: aap.AAPRoleDefinition{Name: roleName},
			},
		},
	}
}

func createTeamRoleAssignments(orgName, roleName string) []*aap.AAPRoleTeamAssignment {
	return []*aap.AAPRoleTeamAssignment{
		{
			ContentType: "shared.organization",
			SummaryFields: aap.AAPRoleTeamAssignmentSummaryFields{
				ContentObject:  aap.AAPContentObject{Name: orgName},
				RoleDefinition: aap.AAPRoleDefinition{Name: roleName},
			},
		},
	}
}

func TestNewAapGatewayAuth_CacheTTL(t *testing.T) {
	// Start a minimal HTTP server that returns valid JSON for the AAP client
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Logf("test server write error: %v", err)
		}
	}))
	defer srv.Close()

	providerName := "aap"
	metadata := api.ObjectMeta{Name: &providerName}
	spec := api.AapProviderSpec{
		ApiUrl:       srv.URL,
		ProviderType: api.Aap,
		Enabled:      lo.ToPtr(true),
	}
	tlsConfig := srv.Client().Transport.(*http.Transport).TLSClientConfig

	tests := []struct {
		name        string
		cacheTTL    time.Duration
		expectedTTL time.Duration
	}{
		{
			name:        "When cacheTTL is zero it should use the default 45s",
			cacheTTL:    0,
			expectedTTL: DefaultAAPIdentityCacheTTL,
		},
		{
			name:        "When cacheTTL is negative it should use the default 45s",
			cacheTTL:    -1 * time.Second,
			expectedTTL: DefaultAAPIdentityCacheTTL,
		},
		{
			name:        "When cacheTTL is a positive custom value it should be used",
			cacheTTL:    30 * time.Second,
			expectedTTL: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, err := NewAapGatewayAuth(metadata, spec, tlsConfig, tt.cacheTTL)
			require.NoError(t, err)
			require.NotNil(t, auth)
			require.NotNil(t, auth.cache)

			// Verify the configured TTL is applied to the cache by setting
			// an item and checking its expiration time.
			testIdentity := common.NewBaseIdentity("ttl-check", "0", nil)
			auth.cache.Set("ttl-probe", testIdentity, ttlcache.DefaultTTL)
			item := auth.cache.Get("ttl-probe")
			require.NotNil(t, item)
			actualTTL := time.Until(item.ExpiresAt())
			assert.InDelta(t, tt.expectedTTL.Seconds(), actualTTL.Seconds(), 1.0,
				"cache TTL should be approximately %s", tt.expectedTTL)
			auth.cache.Delete("ttl-probe")
		})
	}
}

func TestAapGatewayAuth_CacheHitWithinTTL(t *testing.T) {
	cache := ttlcache.New[string, common.Identity](
		ttlcache.WithTTL[string, common.Identity](5 * time.Second),
	)
	go cache.Start()
	defer cache.Stop()

	auth := &AapGatewayAuth{cache: cache}

	testIdentity := common.NewBaseIdentity("testuser", "123", nil)
	token := "test-token-hit"

	cache.Set(token, testIdentity, ttlcache.DefaultTTL)

	item := auth.cache.Get(token)
	require.NotNil(t, item, "When a token is cached it should be returned within the TTL window")
	assert.Equal(t, "testuser", item.Value().GetUsername())
}

func TestAapGatewayAuth_CacheExpiresAfterTTL(t *testing.T) {
	shortTTL := 50 * time.Millisecond
	cache := ttlcache.New[string, common.Identity](
		ttlcache.WithTTL[string, common.Identity](shortTTL),
		ttlcache.WithDisableTouchOnHit[string, common.Identity](),
	)
	go cache.Start()
	defer cache.Stop()

	auth := &AapGatewayAuth{cache: cache}

	testIdentity := common.NewBaseIdentity("testuser", "456", nil)
	token := "test-token-expire"

	cache.Set(token, testIdentity, ttlcache.DefaultTTL)

	// Verify the entry is present before TTL elapses
	item := auth.cache.Get(token)
	require.NotNil(t, item, "When a token is just cached it should be present before TTL elapses")

	// Poll until the cache entry expires, with a deadline to avoid hanging
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	expired := false
	for !expired {
		select {
		case <-deadline:
			t.Fatal("When the TTL has elapsed the cached entry should be absent, but it was still present after 2s")
		case <-ticker.C:
			if auth.cache.Get(token) == nil {
				expired = true
			}
		}
	}
	assert.True(t, expired, "When the TTL has elapsed the cached entry should be absent")
}

func TestAapGatewayAuth_GetAuthToken_MalformedToken(t *testing.T) {
	auth := &AapGatewayAuth{}

	tests := []struct {
		name        string
		authHeader  string
		expectError bool
	}{
		{
			name:        "When no Authorization header is present it should return an error",
			authHeader:  "",
			expectError: true,
		},
		{
			name:        "When the Authorization header has no Bearer prefix it should return an error",
			authHeader:  "garbage-not-a-bearer-token",
			expectError: true,
		},
		{
			name:        "When the token after Bearer is empty it should return an error",
			authHeader:  "Bearer ",
			expectError: true,
		},
		{
			name:        "When the Authorization header contains a valid Bearer token it should succeed",
			authHeader:  "Bearer some-valid-token",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			token, err := auth.GetAuthToken(req)
			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, "some-valid-token", token)
			}
		})
	}
}
