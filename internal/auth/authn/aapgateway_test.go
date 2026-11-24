package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAapGatewayAuth_OrganizationConflictWarning(t *testing.T) {
	// This test captures the process logger; keep it nonparallel and restore it.
	logger := logrus.StandardLogger()
	originalHooks := logger.ReplaceHooks(make(logrus.LevelHooks))
	originalLevel := logger.GetLevel()
	logger.SetLevel(logrus.WarnLevel)
	hook := logtest.NewGlobal()
	t.Cleanup(func() {
		logger.ReplaceHooks(originalHooks)
		logger.SetLevel(originalLevel)
	})

	tests := []struct {
		name      string
		orgs      []aap.AAPOrganization
		conflicts int
		ids       map[string]int64
	}{
		{name: "When mappings are valid it should retain metadata without warning", orgs: []aap.AAPOrganization{{ID: 1, Name: "One"}, {ID: 2, Name: "Two"}}, ids: map[string]int64{"One": 1, "Two": 2}},
		{name: "When identical mappings repeat it should not warn", orgs: []aap.AAPOrganization{{ID: 1, Name: "One"}, {ID: 1, Name: "One"}}, ids: map[string]int64{"One": 1}},
		{name: "When IDs are missing or nonpositive it should omit metadata without warning", orgs: []aap.AAPOrganization{{Name: "Missing"}, {ID: 0, Name: "Zero"}, {ID: -1, Name: "Negative"}}},
		{name: "When names are empty it should not count positive IDs as conflicts", orgs: []aap.AAPOrganization{{ID: 42}, {ID: 43}, {}}},
		{name: "When a valid mapping precedes invalid data it should stay omitted without warning", orgs: []aap.AAPOrganization{{ID: 1, Name: "One"}, {Name: "One"}, {ID: 1, Name: "One"}}},
		{name: "When invalid data precedes a valid mapping it should stay omitted without warning", orgs: []aap.AAPOrganization{{ID: -1, Name: "One"}, {ID: 1, Name: "One"}}},
		{name: "When one name has multiple positive IDs it should count that key once", orgs: []aap.AAPOrganization{{ID: 1, Name: "One"}, {ID: 2, Name: "One"}, {ID: 3, Name: "One"}, {ID: 1, Name: "One"}}, conflicts: 1},
		{name: "When names share a positive ID it should count every key once", orgs: []aap.AAPOrganization{{ID: 1, Name: "One"}, {ID: 1, Name: "Two"}, {ID: 1, Name: "Three"}, {ID: 1, Name: "Two"}}, conflicts: 3},
		{name: "When conflicts overlap it should deduplicate affected keys", orgs: []aap.AAPOrganization{{ID: 1, Name: "One"}, {ID: 2, Name: "One"}, {ID: 1, Name: "Two"}, {ID: 2, Name: "Three"}, {ID: 3, Name: "Four"}, {ID: 4, Name: "Four"}}, conflicts: 4},
		{name: "When invalid observations surround positive conflicts it should still warn", orgs: []aap.AAPOrganization{{Name: "One"}, {ID: 1, Name: "One"}, {Name: "One"}, {ID: 2, Name: "One"}, {ID: -1, Name: "One"}, {ID: 1, Name: "One"}}, conflicts: 1},
		{name: "When an invalid observation precedes a shared ID it should count both keys", orgs: []aap.AAPOrganization{{ID: 1, Name: "One"}, {Name: "One"}, {ID: 1, Name: "Two"}}, conflicts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook.Reset()
			orgs := append([]aap.AAPOrganization{{ID: 42, Name: "Stable"}}, tt.orgs...)
			var requests, lookups atomic.Int32
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var results any
				switch r.URL.Path {
				case "/api/gateway/v1/me/":
					results = []aap.AAPUser{{ID: 7, Username: "example-user", IsPlatformAuditor: true,
						SummaryFields: aap.AAPUserSummaryFields{Resource: aap.AAPResourceSummary{AnsibleID: "example-resource"}}}}
				case "/api/gateway/v1/organizations":
					lookups.Add(1)
					results = orgs
				case "/api/gateway/v1/users/7/teams", "/api/controller/v2/role_user_assignments/":
					results = []any{}
				default:
					t.Errorf("unexpected Gateway request: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"results": results, "next": nil}))
			}))
			defer gateway.Close()
			provider, err := NewAapGatewayAuth(api.ObjectMeta{}, api.AapProviderSpec{
				ApiUrl: gateway.URL, OrganizationNamePrefix: lo.ToPtr("aap-"),
			}, &tls.Config{MinVersion: tls.VersionTLS12}, 0)
			require.NoError(t, err)
			identity, err := provider.GetIdentity(context.Background(), "synthetic-token")
			require.NoError(t, err)
			require.EqualValues(t, 1, lookups.Load())
			freshRequests := requests.Load()
			entries := hook.AllEntries()
			if tt.conflicts == 0 {
				require.Empty(t, entries)
			} else {
				require.Len(t, entries, 1)
				require.Equal(t, logrus.WarnLevel, entries[0].Level)
				require.Equal(t, "Conflicting AAP organization mappings; omitting original external IDs", entries[0].Message)
				require.Equal(t, logrus.Fields{"conflicting_organization_count": tt.conflicts}, entries[0].Data)
			}
			wantIDs := map[string]int64{"aap-Stable": 42}
			for name, id := range tt.ids {
				wantIDs["aap-"+name] = id
			}
			wantNames := map[string]bool{}
			for _, org := range orgs {
				wantNames["aap-"+org.Name] = true
			}
			require.Len(t, identity.GetOrganizations(), len(wantNames))
			aapIdentity, ok := identity.(common.AAPOrganizationIdentity)
			require.True(t, ok)
			for _, org := range identity.GetOrganizations() {
				require.True(t, wantNames[org.Name])
				require.Equal(t, org.Name, org.ID)
				require.False(t, org.IsInternalID)
				require.Equal(t, []string{api.ExternalRoleViewer}, org.Roles)
				id, found := aapIdentity.GetAAPOrganizationID(org.ID)
				wantID, wantFound := wantIDs[org.ID]
				require.Equal(t, wantFound, found, org.ID)
				require.Equal(t, wantID, id, org.ID)
			}
			cached, err := provider.GetIdentity(context.Background(), "synthetic-token")
			require.NoError(t, err)
			require.Same(t, identity, cached)
			require.Equal(t, freshRequests, requests.Load())
			require.Equal(t, entries, hook.AllEntries())
		})
	}
}

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
