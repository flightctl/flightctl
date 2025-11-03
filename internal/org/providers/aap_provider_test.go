package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/stretchr/testify/require"
)

type mockAAPIdentity struct {
	userID          string
	superuser       bool
	platformAuditor bool
}

func (m *mockAAPIdentity) GetUsername() string {
	return "testuser"
}

func (m *mockAAPIdentity) GetUID() string {
	return m.userID
}

func (m *mockAAPIdentity) GetOrganizations() []common.ReportedOrganization {
	return []common.ReportedOrganization{}
}

func (m *mockAAPIdentity) GetRoles() []string {
	return []string{}
}

func (m *mockAAPIdentity) GetIssuer() *identity.Issuer {
	return nil
}

func (m *mockAAPIdentity) IsSuperuser() bool {
	return m.superuser
}

func (m *mockAAPIdentity) IsPlatformAuditor() bool {
	return m.platformAuditor
}

type mockAAPClient struct {
	organization    *aap.AAPOrganization
	organizationErr error

	allOrganizations    []*aap.AAPOrganization
	allOrganizationsErr error

	userOrganizations    []*aap.AAPOrganization
	userOrganizationsErr error

	userTeams    []*aap.AAPTeam
	userTeamsErr error
}

func (m *mockAAPClient) GetOrganization(token string, organizationID string) (*aap.AAPOrganization, error) {
	if m.organizationErr != nil {
		return nil, m.organizationErr
	}
	return m.organization, nil
}

func (m *mockAAPClient) ListUserTeams(token string, userID string) ([]*aap.AAPTeam, error) {
	if m.userTeamsErr != nil {
		return nil, m.userTeamsErr
	}
	return m.userTeams, nil
}

func (m *mockAAPClient) ListOrganizations(token string) ([]*aap.AAPOrganization, error) {
	if m.allOrganizationsErr != nil {
		return nil, m.allOrganizationsErr
	}
	return m.allOrganizations, nil
}

func (m *mockAAPClient) ListUserOrganizations(token string, userID string) ([]*aap.AAPOrganization, error) {
	if m.userOrganizationsErr != nil {
		return nil, m.userOrganizationsErr
	}
	return m.userOrganizations, nil
}

type mockCache struct {
	cache map[string]bool
}

func (m *mockCache) Get(key string) (bool, bool) {
	if value, exists := m.cache[key]; exists {
		return true, value
	}
	return false, false
}

func (m *mockCache) Set(key string, value bool) {
	if m.cache == nil {
		m.cache = make(map[string]bool)
	}
	m.cache[key] = value
}

func TestAAPOrganizationProvider_IsMemberOf(t *testing.T) {
	tests := []struct {
		name         string
		identity     common.Identity
		orgID        string
		expected     bool
		expectedErr  error
		setupMocks   func(*mockAAPClient, *mockCache)
		contextSetup func() context.Context
	}{
		{
			name: "superuser has access to existing organization",
			identity: &mockAAPIdentity{
				userID:    "4",
				superuser: true,
			},
			orgID:       "23",
			expected:    true,
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.organization = &aap.AAPOrganization{ID: 23, Name: "Test Org"}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "platform auditor has access to existing organization",
			identity: &mockAAPIdentity{
				userID:          "4",
				platformAuditor: true,
			},
			orgID:       "23",
			expected:    true,
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.organization = &aap.AAPOrganization{ID: 23, Name: "Test Org"}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user has team-based membership",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "23",
			expected:    true,
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userTeams = []*aap.AAPTeam{
					{
						ID: 1,
						SummaryFields: aap.AAPTeamSummaryFields{
							Organization: aap.AAPOrganization{ID: 23, Name: "Test Org"},
						},
					},
				}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user with forbidden organization access",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "456",
			expected:    false,
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.organizationErr = aap.ErrForbidden
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user with forbidden organization access but has team-based membership",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "456",
			expected:    true,
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.organizationErr = aap.ErrForbidden
				client.userTeams = []*aap.AAPTeam{
					{
						ID: 1,
						SummaryFields: aap.AAPTeamSummaryFields{
							Organization: aap.AAPOrganization{ID: 456, Name: "Test Org"},
						},
					},
				}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "cached membership returns true",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "456",
			expected:    true,
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				cache.Set("123:456", true)
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user can access organization",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "23",
			expected:    true,
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.organization = &aap.AAPOrganization{ID: 23, Name: "Test Org"}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user not member of organization",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "456",
			expected:    false,
			expectedErr: nil,
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name:        "non-AAP identity",
			identity:    &common.BaseIdentity{},
			orgID:       "456",
			expected:    false,
			expectedErr: errors.New("cannot get organizations claims from a non-token identity (got *common.BaseIdentity)"),
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "empty user ID",
			identity: &mockAAPIdentity{
				userID: "",
			},
			orgID:       "456",
			expected:    false,
			expectedErr: errors.New("user ID is required"),
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "missing token in context",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "456",
			expected:    false,
			expectedErr: errors.New("token is required"),
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.Background()
			},
		},
		{
			name: "superuser checking non-existent organization",
			identity: &mockAAPIdentity{
				userID:    "123",
				superuser: true,
			},
			orgID:       "nonexistent",
			expected:    false,
			expectedErr: aap.ErrNotFound,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.organizationErr = aap.ErrNotFound
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "client returns unhandled error",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			orgID:       "456",
			expected:    false,
			expectedErr: errors.New("unhandled error"),
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.organizationErr = errors.New("unhandled error")
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAAPClient{}
			mockCacheInstance := &mockCache{
				cache: map[string]bool{},
			}

			tt.setupMocks(mockClient, mockCacheInstance)

			provider := &AAPProvider{
				client: mockClient,
				cache:  mockCacheInstance,
			}

			ctx := tt.contextSetup()
			result, err := provider.IsMemberOf(ctx, tt.identity, tt.orgID)

			if tt.expectedErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErr.Error())
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAAPOrganizationProvider_GetUserOrganizations(t *testing.T) {
	tests := []struct {
		name         string
		identity     common.Identity
		expected     []org.ExternalOrganization
		expectedErr  error
		setupMocks   func(*mockAAPClient, *mockCache)
		contextSetup func() context.Context
	}{
		{
			name: "superuser gets all organizations",
			identity: &mockAAPIdentity{
				userID:    "123",
				superuser: true,
			},
			expected: []org.ExternalOrganization{
				{ID: "1", Name: "Org 1"},
				{ID: "2", Name: "Org 2"},
			},
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.allOrganizations = []*aap.AAPOrganization{
					{ID: 1, Name: "Org 1"},
					{ID: 2, Name: "Org 2"},
				}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "platform auditor gets all organizations",
			identity: &mockAAPIdentity{
				userID:          "123",
				platformAuditor: true,
			},
			expected: []org.ExternalOrganization{
				{ID: "1", Name: "Org 1"},
				{ID: "2", Name: "Org 2"},
			},
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.allOrganizations = []*aap.AAPOrganization{
					{ID: 1, Name: "Org 1"},
					{ID: 2, Name: "Org 2"},
				}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "superuser fails when GetOrganizations returns error",
			identity: &mockAAPIdentity{
				userID:    "123",
				superuser: true,
			},
			expected:    nil,
			expectedErr: errors.New("service unavailable"),
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.allOrganizationsErr = errors.New("service unavailable")
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user gets user-scoped organizations from direct membership",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected: []org.ExternalOrganization{
				{ID: "1", Name: "User Org 1"},
				{ID: "2", Name: "User Org 2"},
			},
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userOrganizations = []*aap.AAPOrganization{
					{ID: 1, Name: "User Org 1"},
					{ID: 2, Name: "User Org 2"},
				}
				client.userTeams = []*aap.AAPTeam{}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user gets organizations from team membership only",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected: []org.ExternalOrganization{
				{ID: "3", Name: "Team Org 1"},
				{ID: "4", Name: "Team Org 2"},
			},
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userOrganizations = []*aap.AAPOrganization{}
				client.userTeams = []*aap.AAPTeam{
					{
						ID: 1,
						SummaryFields: aap.AAPTeamSummaryFields{
							Organization: aap.AAPOrganization{ID: 3, Name: "Team Org 1"},
						},
					},
					{
						ID: 2,
						SummaryFields: aap.AAPTeamSummaryFields{
							Organization: aap.AAPOrganization{ID: 4, Name: "Team Org 2"},
						},
					},
				}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user gets merged organizations from both direct membership and teams",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected: []org.ExternalOrganization{
				{ID: "1", Name: "Direct Org"},
				{ID: "2", Name: "Team Org"},
			},
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userOrganizations = []*aap.AAPOrganization{
					{ID: 1, Name: "Direct Org"},
				}
				client.userTeams = []*aap.AAPTeam{
					{
						ID: 1,
						SummaryFields: aap.AAPTeamSummaryFields{
							Organization: aap.AAPOrganization{ID: 2, Name: "Team Org"},
						},
					},
				}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user with duplicate organizations from direct and team membership",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected: []org.ExternalOrganization{
				{ID: "1", Name: "Same Org"},
			},
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userOrganizations = []*aap.AAPOrganization{
					{ID: 1, Name: "Same Org"},
				}
				client.userTeams = []*aap.AAPTeam{
					{
						ID: 1,
						SummaryFields: aap.AAPTeamSummaryFields{
							Organization: aap.AAPOrganization{ID: 1, Name: "Same Org"},
						},
					},
				}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user fails when GetUserOrganizations returns error",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected:    nil,
			expectedErr: errors.New("unauthorized"),
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userOrganizationsErr = errors.New("unauthorized")
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "regular user fails when GetUserTeams returns error",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected:    nil,
			expectedErr: errors.New("teams not accessible"),
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userOrganizations = []*aap.AAPOrganization{
					{ID: 1, Name: "User Org"},
				}
				client.userTeamsErr = errors.New("teams not accessible")
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name:        "non-AAP identity returns error",
			identity:    &common.BaseIdentity{},
			expected:    nil,
			expectedErr: errors.New("cannot get organizations claims from a non-token identity (got *common.BaseIdentity)"),
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "empty user ID returns error",
			identity: &mockAAPIdentity{
				userID: "",
			},
			expected:    nil,
			expectedErr: errors.New("user ID is required"),
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
		{
			name: "missing token in context for superuser",
			identity: &mockAAPIdentity{
				userID:    "123",
				superuser: true,
			},
			expected:    nil,
			expectedErr: errors.New("token is required"),
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.Background()
			},
		},
		{
			name: "missing token in context for regular user",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected:    nil,
			expectedErr: errors.New("token is required"),
			setupMocks:  func(client *mockAAPClient, cache *mockCache) {},
			contextSetup: func() context.Context {
				return context.Background()
			},
		},
		{
			name: "empty organizations for regular user",
			identity: &mockAAPIdentity{
				userID: "123",
			},
			expected:    []org.ExternalOrganization{},
			expectedErr: nil,
			setupMocks: func(client *mockAAPClient, cache *mockCache) {
				client.userOrganizations = []*aap.AAPOrganization{}
				client.userTeams = []*aap.AAPTeam{}
			},
			contextSetup: func() context.Context {
				return context.WithValue(context.Background(), consts.TokenCtxKey, "test-token")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAAPClient{}
			mockCacheInstance := &mockCache{
				cache: make(map[string]bool),
			}

			tt.setupMocks(mockClient, mockCacheInstance)

			provider := &AAPProvider{
				client: mockClient,
				cache:  mockCacheInstance,
			}

			ctx := tt.contextSetup()
			result, err := provider.GetUserOrganizations(ctx, tt.identity)

			if tt.expectedErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErr.Error())
			} else {
				require.NoError(t, err)
			}

			if tt.expected != nil {
				require.Len(t, result, len(tt.expected))

				// Convert result to map for easier comparison (order doesn't matter due to map iteration)
				resultMap := make(map[string]string)
				for _, org := range result {
					resultMap[org.ID] = org.Name
				}

				// Check each expected organization
				for _, expectedOrg := range tt.expected {
					require.Contains(t, resultMap, expectedOrg.ID)
					require.Equal(t, expectedOrg.Name, resultMap[expectedOrg.ID])
				}
			} else {
				require.Nil(t, result)
			}

			// Verify cache was updated for successful cases with valid user ID
			if err == nil && tt.identity != nil {
				if aapIdentity, ok := tt.identity.(*mockAAPIdentity); ok && aapIdentity.userID != "" {
					for _, org := range result {
						cacheKey := aapIdentity.userID + ":" + org.ID
						present, isMember := mockCacheInstance.Get(cacheKey)
						require.True(t, present, "Cache should contain key for org %s", org.ID)
						require.True(t, isMember, "Cache should be updated for org %s", org.ID)
					}
				}
			}
		})
	}
}
