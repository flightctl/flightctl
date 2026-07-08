package service

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
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

// fakeOrganizationStore is a minimal in-memory organizationstore.Store fake for
// identity-mapper tests. Embedding the interface lets unimplemented methods panic
// if ever called, matching the pattern used elsewhere in this package's tests.
type fakeOrganizationStore struct {
	organizationstore.Store
	organizations []*model.Organization
	err           error
}

func (s *fakeOrganizationStore) Create(ctx context.Context, org *model.Organization) (*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.organizations = append(s.organizations, org)
	return org, nil
}

func (s *fakeOrganizationStore) UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	existingMap := make(map[string]*model.Organization)
	for _, org := range s.organizations {
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
			s.organizations = append(s.organizations, newOrg)
			result = append(result, newOrg)
		}
	}

	return result, nil
}

func (s *fakeOrganizationStore) List(ctx context.Context, listParams store.ListParams) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.organizations, nil
}

func (s *fakeOrganizationStore) ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	externalIDMap := make(map[string]bool)
	for _, id := range externalIDs {
		externalIDMap[id] = true
	}

	var result []*model.Organization
	for _, org := range s.organizations {
		if externalIDMap[org.ExternalID] {
			result = append(result, org)
		}
	}

	return result, nil
}

func (s *fakeOrganizationStore) ListByIDs(ctx context.Context, ids []string) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	idMap := make(map[string]bool)
	for _, id := range ids {
		idMap[id] = true
	}

	var result []*model.Organization
	for _, org := range s.organizations {
		if idMap[org.ID.String()] {
			result = append(result, org)
		}
	}

	return result, nil
}

func (s *fakeOrganizationStore) GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	for _, org := range s.organizations {
		if org.ID == id {
			return org, nil
		}
	}
	return nil, errors.New("organization not found")
}

// fakeCatalogStore is a minimal in-memory catalogstore.Store fake, only implementing
// Get/Create - the two methods OrgProvisioner actually calls.
type fakeCatalogStore struct {
	catalogstore.Store
	catalogs []*domain.Catalog
	getErr   error
}

func (s *fakeCatalogStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	for _, catalog := range s.catalogs {
		if name == *catalog.Metadata.Name {
			return catalog, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *fakeCatalogStore) Create(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog, callbackEvent store.EventCallback) (*domain.Catalog, error) {
	s.catalogs = append(s.catalogs, catalog)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.CatalogKind, orgId, *catalog.Metadata.Name, nil, catalog, true, nil)
	}
	return catalog, nil
}

func createTestOrganizationModel(id uuid.UUID, externalID string, displayName string) *model.Organization {
	return &model.Organization{
		ID:          id,
		ExternalID:  externalID,
		DisplayName: displayName,
	}
}

func createTestIdentityMapper(orgStore *fakeOrganizationStore, catalogStore *fakeCatalogStore) *IdentityMapper {
	return NewIdentityMapper(orgStore, NewOrgProvisioner(catalogStore, logrus.New()), logrus.New())
}

func TestMapIdentityToDB_SuperAdmin_NoReportedOrgs(t *testing.T) {
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	orgStore.organizations = []*model.Organization{org1, org2}

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
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	orgStore.organizations = []*model.Organization{org1, org2}

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
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	orgStore.organizations = []*model.Organization{org1}

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
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	// Setup store to return error
	orgStore.err = errors.New("database connection failed")

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
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	// Create existing organizations
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	orgStore.organizations = []*model.Organization{org1, org2}

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
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

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
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	orgStore.organizations = []*model.Organization{org1, org2}

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
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	org2 := createTestOrganizationModel(uuid.New(), "org-2", "Organization 2")
	org3 := createTestOrganizationModel(uuid.New(), "org-3", "Organization 3")
	orgStore.organizations = []*model.Organization{org1, org2, org3}

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

func TestMapIdentityToDB_RegularUser_NewOrg_CreatesDefaultCatalog(t *testing.T) {
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	identity := &mockIdentity{
		username: "user",
		uid:      "user-uid",
		organizations: []common.ReportedOrganization{
			{ID: "org-new", Name: "New Organization", IsInternalID: false},
		},
		isSuperAdmin: false,
	}

	ctx := context.Background()
	result, err := mapper.MapIdentityToDB(ctx, identity)

	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, "org-new", result[0].ExternalID)

	// Verify the default catalog was created for the new organization
	catalog, err := catalogStore.Get(ctx, result[0].ID, domain.DefaultCatalogName)
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Equal(t, domain.DefaultCatalogName, *catalog.Metadata.Name)
	require.Equal(t, domain.DefaultCatalogDisplayName, *catalog.Spec.DisplayName)
}

func TestMapIdentityToDB_RegularUser_ExistingOrg_DoesNotCreateDefaultCatalog(t *testing.T) {
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	orgStore.organizations = []*model.Organization{org1}

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

	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, org1.ID, result[0].ID)

	// Verify no default catalog was created for the existing organization
	_, err = catalogStore.Get(ctx, org1.ID, domain.DefaultCatalogName)
	require.Error(t, err, "Default catalog should not be created for an existing organization")
}

func TestMapIdentityToDB_SuperAdmin_NewReportedOrg_CreatesDefaultCatalog(t *testing.T) {
	orgStore := &fakeOrganizationStore{}
	catalogStore := &fakeCatalogStore{}
	mapper := createTestIdentityMapper(orgStore, catalogStore)

	// One pre-existing organization
	org1 := createTestOrganizationModel(uuid.New(), "org-1", "Organization 1")
	orgStore.organizations = []*model.Organization{org1}

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
	require.Len(t, result, 2)

	// Find the newly created org
	var newOrg *model.Organization
	for _, org := range result {
		if org.ExternalID == "org-new" {
			newOrg = org
			break
		}
	}
	require.NotNil(t, newOrg, "New organization should be created")

	// Verify the default catalog was created for the new organization
	catalog, err := catalogStore.Get(ctx, newOrg.ID, domain.DefaultCatalogName)
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Equal(t, domain.DefaultCatalogName, *catalog.Metadata.Name)
	require.Equal(t, domain.DefaultCatalogDisplayName, *catalog.Spec.DisplayName)
}
