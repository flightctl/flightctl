package service

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// mockIdentity implements common.Identity interface for testing
type mockIdentity struct {
	username      string
	uid           string
	organizations []common.ReportedOrganization
	isSuperAdmin  bool
}

func (m *mockIdentity) GetUsername() string {
	return m.username
}

func (m *mockIdentity) GetUID() string {
	return m.uid
}

func (m *mockIdentity) GetOrganizations() []common.ReportedOrganization {
	return m.organizations
}

func (m *mockIdentity) IsSuperAdmin() bool {
	return m.isSuperAdmin
}

func (m *mockIdentity) SetSuperAdmin(superAdmin bool) {
	m.isSuperAdmin = superAdmin
}

func (m *mockIdentity) GetIssuer() *identity.Issuer {
	return nil
}

func createTestIdentityMapper(mockStore *TestStore) *IdentityMapper {
	return NewIdentityMapper(mockStore, logrus.New())
}

func TestMapIdentityToDB_SuperAdmin_NoReportedOrgs(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1, org2})

	// Super admin with no reported organizations
	identity := &mockIdentity{
		username:      "admin",
		uid:           "admin-uid",
		organizations: []common.ReportedOrganization{},
		isSuperAdmin:  true,
	}

	ctx := context.Background()
	result, err := mapper.MapIdentityToDB(ctx, identity)

	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Contains(t, result, org1)
	require.Contains(t, result, org2)
}

func TestMapIdentityToDB_SuperAdmin_WithReportedOrgs_AllExist(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1, org2})

	// Super admin with reported organizations that already exist
	identity := &mockIdentity{
		username: "admin",
		uid:      "admin-uid",
		organizations: []common.ReportedOrganization{
			{ID: "org-1", Name: "Organization 1", IsInternalID: false},
		},
		isSuperAdmin: true,
	}

	ctx := context.Background()
	result, err := mapper.MapIdentityToDB(ctx, identity)

	require.NoError(t, err)
	require.Len(t, result, 2) // Should get all orgs, not just reported ones
	require.Contains(t, result, org1)
	require.Contains(t, result, org2)
}

func TestMapIdentityToDB_SuperAdmin_WithNewReportedOrg(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1})

	// Super admin with a new organization to create
	identity := &mockIdentity{
		username: "admin",
		uid:      "admin-uid",
		organizations: []common.ReportedOrganization{
			{ID: "org-new", Name: "New Organization", IsInternalID: false},
		},
		isSuperAdmin: true,
	}

	ctx := context.Background()
	result, err := mapper.MapIdentityToDB(ctx, identity)

	require.NoError(t, err)
	require.Len(t, result, 2) // Original org + newly created org

	// Verify the new org was created
	foundNewOrg := false
	for _, org := range result {
		if org.ExternalID == "org-new" && org.DisplayName == "New Organization" {
			foundNewOrg = true
			break
		}
	}
	require.True(t, foundNewOrg, "New organization should be created")
}

func TestMapIdentityToDB_SuperAdmin_DatabaseError(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	// Setup store to return error
	testError := errors.New("database connection failed")
	setupMockStoreWithError(mockStore, testError)

	identity := &mockIdentity{
		username:      "admin",
		uid:           "admin-uid",
		organizations: []common.ReportedOrganization{},
		isSuperAdmin:  true,
	}

	ctx := context.Background()
	result, err := mapper.MapIdentityToDB(ctx, identity)

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "failed to list all organizations for super admin")
}

func TestMapIdentityToDB_RegularUser_WithReportedOrgs(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1, org2})

	// Regular user (not super admin) with one reported organization
	identity := &mockIdentity{
		username: "user",
		uid:      "user-uid",
		organizations: []common.ReportedOrganization{
			{ID: "org-1", Name: "Organization 1", IsInternalID: false},
		},
		isSuperAdmin: false,
	}

	ctx := context.Background()
	result, err := mapper.MapIdentityToDB(ctx, identity)

	// Regular user should only get their reported organizations, not all of them
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, "org-1", result[0].ExternalID)
	require.Equal(t, org1.ID, result[0].ID)
}

func TestMapIdentityToDB_RegularUser_NoOrganizations(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	// Regular user with no organizations
	identity := &mockIdentity{
		username:      "user",
		uid:           "user-uid",
		organizations: []common.ReportedOrganization{},
		isSuperAdmin:  false,
	}

	ctx := context.Background()
	result, err := mapper.MapIdentityToDB(ctx, identity)

	require.NoError(t, err)
	require.Empty(t, result)
}

func TestIsMemberOf_SuperAdmin(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1, org2})

	// Super admin should have access to all organizations
	identity := &mockIdentity{
		username:      "admin",
		uid:           "admin-uid",
		organizations: []common.ReportedOrganization{},
		isSuperAdmin:  true,
	}

	ctx := context.Background()

	// Check membership for org1
	isMember, err := mapper.IsMemberOf(ctx, identity, org1.ID)
	require.NoError(t, err)
	require.True(t, isMember)

	// Check membership for org2
	isMember, err = mapper.IsMemberOf(ctx, identity, org2.ID)
	require.NoError(t, err)
	require.True(t, isMember)
}

func TestGetUserOrganizations_SuperAdmin(t *testing.T) {
	mockStore := &TestStore{}
	mapper := createTestIdentityMapper(mockStore)

	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	org3 := createTestOrganizationModel(uuid.New(), "org-3", "Organization 3")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1, org2, org3})

	// Super admin should get all organizations
	identity := &mockIdentity{
		username:      "admin",
		uid:           "admin-uid",
		organizations: []common.ReportedOrganization{},
		isSuperAdmin:  true,
	}

	ctx := context.Background()
	result, err := mapper.GetUserOrganizations(ctx, identity)

	require.NoError(t, err)
	require.Len(t, result, 3)
	require.Contains(t, result, org1)
	require.Contains(t, result, org2)
	require.Contains(t, result, org3)
}

// Add missing methods to DummyOrganization for proper testing
func (s *DummyOrganization) UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.organizations == nil {
		s.organizations = &[]*model.Organization{}
	}

	// Simulate upsert: check if orgs exist by external ID, otherwise add them
	existingMap := make(map[string]*model.Organization)
	for _, org := range *s.organizations {
		if org.ExternalID != "" {
			existingMap[org.ExternalID] = org
		}
	}

	var result []*model.Organization
	for _, newOrg := range orgs {
		if existing, found := existingMap[newOrg.ExternalID]; found {
			result = append(result, existing)
		} else {
			if newOrg.ID == uuid.Nil {
				newOrg.ID = uuid.New()
			}
			*s.organizations = append(*s.organizations, newOrg)
			result = append(result, newOrg)
		}
	}

	return result, nil
}

func (s *DummyOrganization) ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.organizations == nil {
		return []*model.Organization{}, nil
	}

	externalIDMap := make(map[string]bool)
	for _, id := range externalIDs {
		externalIDMap[id] = true
	}

	var result []*model.Organization
	for _, org := range *s.organizations {
		if externalIDMap[org.ExternalID] {
			result = append(result, org)
		}
	}

	return result, nil
}

func (s *DummyOrganization) ListByIDs(ctx context.Context, ids []string) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.organizations == nil {
		return []*model.Organization{}, nil
	}

	idMap := make(map[string]bool)
	for _, id := range ids {
		idMap[id] = true
	}

	var result []*model.Organization
	for _, org := range *s.organizations {
		if idMap[org.ID.String()] {
			result = append(result, org)
		}
	}

	return result, nil
}

func (s *DummyOrganization) GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.organizations == nil {
		return nil, errors.New("organization not found")
	}

	for _, org := range *s.organizations {
		if org.ID == id {
			return org, nil
		}
	}

	return nil, errors.New("organization not found")
}
