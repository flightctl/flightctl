package resolvers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockOrgStore implements OrgStore interface for testing
type mockOrgStore struct {
	getByIDResult    *model.Organization
	getByIDCallCount int
	getByIDError     error

	listByExternalIDsResult    []*model.Organization
	listByExternalIDsCallCount int
	listByExternalIDsError     error

	upsertManyResult    []*model.Organization
	upsertManyCallCount int
	upsertManyError     error
}

func (m *mockOrgStore) GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	m.getByIDCallCount++
	return m.getByIDResult, m.getByIDError
}

func (m *mockOrgStore) ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error) {
	m.listByExternalIDsCallCount++
	return m.listByExternalIDsResult, m.listByExternalIDsError
}

func (m *mockOrgStore) UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error) {
	m.upsertManyCallCount++
	return m.upsertManyResult, m.upsertManyError
}

// setupOrgInCache pre-populates the cache with an organization
func setupOrgInCache(resolver *ExternalResolver, id uuid.UUID, org *model.Organization, ttl time.Duration) {
	cacheTTL := ttlcache.NoTTL
	if ttl > 0 {
		cacheTTL = ttl
	}
	resolver.cache.Set(id, org, cacheTTL)
}

type mockExternalOrgProvider struct {
	orgs []org.ExternalOrganization
	err  error
}

func (m *mockExternalOrgProvider) GetUserOrganizations(ctx context.Context, identity common.Identity) ([]org.ExternalOrganization, error) {
	return m.orgs, m.err
}

func (m *mockExternalOrgProvider) IsMemberOf(ctx context.Context, identity common.Identity, orgID string) (bool, error) {
	return false, errors.New("not implemented in mock")
}

func TestExternalResolver_getOrg(t *testing.T) {
	ctx := context.Background()

	// Test organization data
	testOrgID := uuid.New()
	testOrg := &model.Organization{
		ID:          testOrgID,
		ExternalID:  "test-external-id",
		DisplayName: "Test Organization",
	}

	tests := []struct {
		name          string
		setupCache    func(*ExternalResolver)
		store         *mockOrgStore
		orgID         uuid.UUID
		ttl           time.Duration
		expectedOrg   *model.Organization
		expectedErr   string
		expectedCalls int
	}{
		{
			name: "cache hit returns cached organization",
			setupCache: func(r *ExternalResolver) {
				setupOrgInCache(r, testOrgID, testOrg, time.Hour)
			},
			store:         &mockOrgStore{getByIDResult: testOrg},
			orgID:         testOrgID,
			ttl:           time.Hour,
			expectedOrg:   testOrg,
			expectedErr:   "",
			expectedCalls: 0,
		},
		{
			name: "cache miss triggers store lookup success",
			setupCache: func(r *ExternalResolver) {
				// No cache setup - cache miss
			},
			store:         &mockOrgStore{getByIDResult: testOrg},
			orgID:         testOrgID,
			ttl:           time.Hour,
			expectedOrg:   testOrg,
			expectedErr:   "",
			expectedCalls: 1,
		},
		{
			name: "cache miss with not found error",
			setupCache: func(r *ExternalResolver) {
				// No cache setup - cache miss
			},
			store:         &mockOrgStore{getByIDError: errors.New("organization not found")},
			orgID:         testOrgID,
			ttl:           time.Hour,
			expectedOrg:   nil,
			expectedErr:   "organization not found",
			expectedCalls: 1,
		},
		{
			name: "different organization ID cache miss",
			setupCache: func(r *ExternalResolver) {
				// Cache different org
				differentID := uuid.New()
				setupOrgInCache(r, differentID, testOrg, time.Hour)
			},
			store: &mockOrgStore{
				getByIDResult: &model.Organization{
					ID:          testOrgID,
					ExternalID:  "different-external-id",
					DisplayName: "Different Organization",
				},
			},
			orgID: testOrgID,
			ttl:   time.Hour,
			expectedOrg: &model.Organization{
				ID:          testOrgID,
				ExternalID:  "different-external-id",
				DisplayName: "Different Organization",
			},
			expectedErr:   "",
			expectedCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewExternalResolver(context.Background(), tt.store, tt.ttl, nil, logrus.New())
			defer resolver.Close()

			tt.setupCache(resolver)

			result, err := resolver.getOrg(ctx, tt.orgID)
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, tt.expectedOrg.ID, result.ID)
				assert.Equal(t, tt.expectedOrg.ExternalID, result.ExternalID)
				assert.Equal(t, tt.expectedOrg.DisplayName, result.DisplayName)
			}
			assert.Equal(t, tt.expectedCalls, tt.store.getByIDCallCount)
		})
	}
}

func TestExternalResolver_GetUserOrganizations(t *testing.T) {
	ctx := context.Background()

	orgID := uuid.New()
	orgID2 := uuid.New()

	test := []struct {
		name                               string
		store                              *mockOrgStore
		externalOrgProvider                *mockExternalOrgProvider
		expectedListByExternalIDsCallCount int
		expectedUpsertManyCallCount        int
		expectedResult                     []*model.Organization
		expectedErrorString                string
	}{
		{
			name:  "no external orgs",
			store: &mockOrgStore{},
			externalOrgProvider: &mockExternalOrgProvider{
				orgs: []org.ExternalOrganization{},
			},
			expectedListByExternalIDsCallCount: 0,
			expectedUpsertManyCallCount:        0,
			expectedResult:                     []*model.Organization{},
			expectedErrorString:                "",
		},
		{
			name: "all orgs already exist in store",
			store: &mockOrgStore{
				listByExternalIDsResult: []*model.Organization{
					{ID: orgID, ExternalID: "1", DisplayName: "Test Org 1"},
					{ID: orgID2, ExternalID: "2", DisplayName: "Test Org 2"},
				},
			},
			externalOrgProvider: &mockExternalOrgProvider{
				orgs: []org.ExternalOrganization{
					{ID: "1", Name: "Test Org 1"},
					{ID: "2", Name: "Test Org 2"},
				},
			},
			expectedListByExternalIDsCallCount: 1,
			expectedUpsertManyCallCount:        0,
			expectedResult: []*model.Organization{
				{ID: orgID, ExternalID: "1", DisplayName: "Test Org 1"},
				{ID: orgID2, ExternalID: "2", DisplayName: "Test Org 2"},
			},
			expectedErrorString: "",
		},
		{
			name: "some orgs exist, some need to be created",
			store: &mockOrgStore{
				listByExternalIDsResult: []*model.Organization{
					{ID: orgID, ExternalID: "1", DisplayName: "Test Org 1"},
				},
				upsertManyResult: []*model.Organization{
					{ID: orgID2, ExternalID: "2", DisplayName: "Test Org 2"},
				},
			},
			externalOrgProvider: &mockExternalOrgProvider{
				orgs: []org.ExternalOrganization{
					{ID: "1", Name: "Test Org 1"},
					{ID: "2", Name: "Test Org 2"},
				},
			},
			expectedListByExternalIDsCallCount: 1,
			expectedUpsertManyCallCount:        1,
			expectedResult: []*model.Organization{
				{ID: orgID, ExternalID: "1", DisplayName: "Test Org 1"},
				{ID: orgID2, ExternalID: "2", DisplayName: "Test Org 2"},
			},
			expectedErrorString: "",
		},
		{
			name: "no orgs exist, all need to be created",
			store: &mockOrgStore{
				listByExternalIDsResult: []*model.Organization{},
				upsertManyResult: []*model.Organization{
					{ID: orgID, ExternalID: "1", DisplayName: "Test Org 1"},
					{ID: orgID2, ExternalID: "2", DisplayName: "Test Org 2"},
				},
			},
			externalOrgProvider: &mockExternalOrgProvider{
				orgs: []org.ExternalOrganization{
					{ID: "1", Name: "Test Org 1"},
					{ID: "2", Name: "Test Org 2"},
				},
			},
			expectedListByExternalIDsCallCount: 1,
			expectedUpsertManyCallCount:        1,
			expectedResult: []*model.Organization{
				{ID: orgID, ExternalID: "1", DisplayName: "Test Org 1"},
				{ID: orgID2, ExternalID: "2", DisplayName: "Test Org 2"},
			},
			expectedErrorString: "",
		},
		{
			name:  "error from external org provider",
			store: &mockOrgStore{},
			externalOrgProvider: &mockExternalOrgProvider{
				err: errors.New("error from external org provider"),
			},
			expectedListByExternalIDsCallCount: 0,
			expectedUpsertManyCallCount:        0,
			expectedResult:                     nil,
			expectedErrorString:                "error from external org provider",
		},
		{
			name: "error from store list call",
			store: &mockOrgStore{
				listByExternalIDsError: errors.New("error from store list call"),
			},
			externalOrgProvider: &mockExternalOrgProvider{
				orgs: []org.ExternalOrganization{
					{ID: "1", Name: "Test Org 1"},
				},
			},
			expectedListByExternalIDsCallCount: 1,
			expectedUpsertManyCallCount:        0,
			expectedResult:                     nil,
			expectedErrorString:                "error from store list call",
		},
		{
			name: "error from store upsert call",
			store: &mockOrgStore{
				listByExternalIDsResult: []*model.Organization{},
				upsertManyError:         errors.New("error from store upsert call"),
			},
			externalOrgProvider: &mockExternalOrgProvider{
				orgs: []org.ExternalOrganization{
					{ID: "1", Name: "Test Org 1"},
				},
			},
			expectedListByExternalIDsCallCount: 1,
			expectedUpsertManyCallCount:        1,
			expectedResult:                     nil,
			expectedErrorString:                "error from store upsert call",
		},
	}

	for _, tt := range test {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewExternalResolver(context.Background(), tt.store, 0, tt.externalOrgProvider, logrus.New())
			defer resolver.Close()

			orgs, err := resolver.GetUserOrganizations(ctx, common.NewBaseIdentity("test-user", "test-org", []string{}))

			if tt.expectedErrorString != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrorString)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.expectedListByExternalIDsCallCount, tt.store.listByExternalIDsCallCount)
			assert.Equal(t, tt.expectedUpsertManyCallCount, tt.store.upsertManyCallCount)
			assert.Equal(t, tt.expectedResult, orgs)
		})
	}
}
