package organization

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeOrganizationStore is a small in-memory implementation of internal/store/organization.Store.
type fakeOrganizationStore struct {
	organizations []*model.Organization
	err           error
}

func (f *fakeOrganizationStore) InitialMigration(ctx context.Context) error { return f.err }

func (f *fakeOrganizationStore) Create(ctx context.Context, org *model.Organization) (*model.Organization, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.organizations = append(f.organizations, org)
	return org, nil
}

func (f *fakeOrganizationStore) UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.organizations = append(f.organizations, orgs...)
	return orgs, nil
}

func (f *fakeOrganizationStore) List(ctx context.Context, listParams store.ListParams) ([]*model.Organization, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.organizations, nil
}

func (f *fakeOrganizationStore) ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error) {
	return nil, nil
}

func (f *fakeOrganizationStore) ListByIDs(ctx context.Context, ids []string) ([]*model.Organization, error) {
	return nil, nil
}

func (f *fakeOrganizationStore) GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	return nil, nil
}

func newTestHandler(orgs []*model.Organization) (*ServiceHandler, *fakeOrganizationStore) {
	fakeStore := &fakeOrganizationStore{organizations: orgs}
	return NewServiceHandler(fakeStore), fakeStore
}

func createTestOrganizationModel(id uuid.UUID, externalID string, displayName string) *model.Organization {
	return &model.Organization{
		ID:          id,
		ExternalID:  externalID,
		DisplayName: displayName,
	}
}

func createExpectedAPIOrganization(id uuid.UUID, displayName string, externalID string) domain.Organization {
	name := id.String()
	return domain.Organization{
		ApiVersion: organizationApiVersion,
		Kind:       domain.OrganizationKind,
		Metadata:   domain.ObjectMeta{Name: &name},
		Spec: &domain.OrganizationSpec{
			ExternalId:  &externalID,
			DisplayName: &displayName,
		},
	}
}

func TestListAllOrganizations_EmptyResult(t *testing.T) {
	handler, _ := newTestHandler([]*model.Organization{})

	result, status := handler.ListAllOrganizations(context.Background(), domain.ListOrganizationsParams{})

	require.Equal(t, domain.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, domain.OrganizationListKind, result.Kind)
	require.Empty(t, result.Items)
	require.Equal(t, domain.ListMeta{}, result.Metadata)
}

func TestListAllOrganizations_SingleOrganization(t *testing.T) {
	orgID := uuid.New()
	defaultOrg := createTestOrganizationModel(orgID, "default-external-id", "Default")
	handler, _ := newTestHandler([]*model.Organization{defaultOrg})

	expectedOrg := createExpectedAPIOrganization(orgID, "Default", "default-external-id")

	result, status := handler.ListAllOrganizations(context.Background(), domain.ListOrganizationsParams{})

	require.Equal(t, domain.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, domain.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 1)
	require.Equal(t, expectedOrg, result.Items[0])
	require.Equal(t, domain.ListMeta{}, result.Metadata)
}

func TestListAllOrganizations_MultipleOrganizations(t *testing.T) {
	orgID1 := uuid.New()
	orgID2 := uuid.New()

	org1 := createTestOrganizationModel(orgID1, "external-id-1", "Organization One")
	org2 := createTestOrganizationModel(orgID2, "external-id-2", "Organization Two")

	handler, _ := newTestHandler([]*model.Organization{org1, org2})

	expectedOrg1 := createExpectedAPIOrganization(orgID1, "Organization One", "external-id-1")
	expectedOrg2 := createExpectedAPIOrganization(orgID2, "Organization Two", "external-id-2")

	result, status := handler.ListAllOrganizations(context.Background(), domain.ListOrganizationsParams{})

	require.Equal(t, domain.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, domain.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 2)

	require.Contains(t, result.Items, expectedOrg1)
	require.Contains(t, result.Items, expectedOrg2)
	require.Equal(t, domain.ListMeta{}, result.Metadata)
}

func TestListAllOrganizations_StoreError(t *testing.T) {
	handler, fakeStore := newTestHandler(nil)
	fakeStore.err = errors.New("database connection failed")

	result, status := handler.ListAllOrganizations(context.Background(), domain.ListOrganizationsParams{})

	require.Nil(t, result)
	require.NotEqual(t, domain.StatusOK(), status)
	require.Contains(t, status.Message, "database connection failed")
}

func TestListAllOrganizations_ResourceNotFoundError(t *testing.T) {
	handler, fakeStore := newTestHandler(nil)
	fakeStore.err = flterrors.ErrResourceNotFound

	result, status := handler.ListAllOrganizations(context.Background(), domain.ListOrganizationsParams{})

	require.Nil(t, result)
	require.Equal(t, int32(404), status.Code)
	require.Contains(t, status.Message, domain.OrganizationKind)
}

func TestListOrganizations_WithAuthFiltering(t *testing.T) {
	// Create three orgs ordered by ID: U1 (unauthorized), U2 and U3 (authorized)
	u1 := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	u2 := uuid.MustParse("00000000-0000-0000-0000-000000000022")
	u3 := uuid.MustParse("00000000-0000-0000-0000-000000000033")

	org1 := createTestOrganizationModel(u1, "ext-11", "Org-11")
	org2 := createTestOrganizationModel(u2, "ext-22", "Org-22")
	org3 := createTestOrganizationModel(u3, "ext-33", "Org-33")
	handler, _ := newTestHandler([]*model.Organization{org1, org2, org3})

	// Build external request context with mapped identity (non-internal)
	// Create MappedIdentity with only org2 and org3 (authorized orgs)
	mappedIdentity := identity.NewMappedIdentity(
		"tester",
		"uid-1",
		[]*model.Organization{org2, org3},
		map[string][]string{},
		false,
		nil,
	)
	ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)

	// Expect both authorized orgs (U2 and U3) to be returned
	result, status := handler.ListOrganizations(ctx, domain.ListOrganizationsParams{})
	require.Equal(t, domain.StatusOK(), status)
	require.NotNil(t, result)
	require.Len(t, result.Items, 2)
	require.Equal(t, domain.ListMeta{}, result.Metadata)

	expectedOrg2 := createExpectedAPIOrganization(u2, "Org-22", "ext-22")
	expectedOrg3 := createExpectedAPIOrganization(u3, "Org-33", "ext-33")
	require.Contains(t, result.Items, expectedOrg2)
	require.Contains(t, result.Items, expectedOrg3)
}

func TestListOrganizations_FieldSelectorFiltersAuthorizedOrgs(t *testing.T) {
	u2 := uuid.MustParse("00000000-0000-0000-0000-000000000022")
	u3 := uuid.MustParse("00000000-0000-0000-0000-000000000033")

	org2 := createTestOrganizationModel(u2, "ext-22", "Org-22")
	org3 := createTestOrganizationModel(u3, "ext-33", "Org-33")
	handler, _ := newTestHandler(nil)

	mappedIdentity := identity.NewMappedIdentity(
		"tester",
		"uid-1",
		[]*model.Organization{org2, org3},
		map[string][]string{},
		false,
		nil,
	)
	ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)

	selector := u2.String()
	result, status := handler.ListOrganizations(ctx, domain.ListOrganizationsParams{FieldSelector: &selector})
	require.Equal(t, domain.StatusOK(), status)
	require.Len(t, result.Items, 1)
	require.Equal(t, createExpectedAPIOrganization(u2, "Org-22", "ext-22"), result.Items[0])
}
