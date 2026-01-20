package service

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func createServiceHandlerWithOrgMockStore(t *testing.T) (*ServiceHandler, *TestStore) {
	mockStore := &TestStore{}
	handler := &ServiceHandler{
		eventHandler: NewEventHandler(mockStore, nil, log.InitLogs()),
		store:        mockStore,
	}
	return handler, mockStore
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

func setupMockStoreWithOrganizations(mockStore *TestStore, orgs []*model.Organization) {
	mockStore.Organization().(*DummyOrganization).organizations = &orgs
}

func setupMockStoreWithError(mockStore *TestStore, err error) {
	mockStore.Organization().(*DummyOrganization).err = err
}

func TestListOrganizations_EmptyResult(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{})
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	result, status := handler.ListOrganizations(ctx, domain.ListOrganizationsParams{})

	require.Equal(t, domain.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, domain.OrganizationListKind, result.Kind)
	require.Empty(t, result.Items)
	require.Equal(t, domain.ListMeta{}, result.Metadata)
}

func TestListOrganizations_SingleOrganization(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	orgID := uuid.New()
	defaultOrg := createTestOrganizationModel(orgID, "default-external-id", "Default")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{defaultOrg})
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	expectedOrg := createExpectedAPIOrganization(orgID, "Default", "default-external-id")

	result, status := handler.ListOrganizations(ctx, domain.ListOrganizationsParams{})

	require.Equal(t, domain.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, domain.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 1)
	require.Equal(t, expectedOrg, result.Items[0])
	require.Equal(t, domain.ListMeta{}, result.Metadata)
}

func TestListOrganizations_MultipleOrganizations(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)

	orgID1 := uuid.New()
	orgID2 := uuid.New()

	org1 := createTestOrganizationModel(orgID1, "external-id-1", "Organization One")
	org2 := createTestOrganizationModel(orgID2, "external-id-2", "Organization Two")

	orgs := []*model.Organization{org1, org2}
	setupMockStoreWithOrganizations(mockStore, orgs)
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	expectedOrg1 := createExpectedAPIOrganization(orgID1, "Organization One", "external-id-1")
	expectedOrg2 := createExpectedAPIOrganization(orgID2, "Organization Two", "external-id-2")

	result, status := handler.ListOrganizations(ctx, domain.ListOrganizationsParams{})

	require.Equal(t, domain.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, domain.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 2)

	require.Contains(t, result.Items, expectedOrg1)
	require.Contains(t, result.Items, expectedOrg2)
	require.Equal(t, domain.ListMeta{}, result.Metadata)
}

func TestListOrganizations_StoreError(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	testError := errors.New("database connection failed")
	setupMockStoreWithError(mockStore, testError)
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	result, status := handler.ListOrganizations(ctx, domain.ListOrganizationsParams{})

	require.Nil(t, result)
	require.NotEqual(t, domain.StatusOK(), status)
	require.Contains(t, status.Message, "database connection failed")
}

func TestListOrganizations_ResourceNotFoundError(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	setupMockStoreWithError(mockStore, flterrors.ErrResourceNotFound)
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	result, status := handler.ListOrganizations(ctx, domain.ListOrganizationsParams{})

	require.Nil(t, result)
	require.Equal(t, int32(404), status.Code)
	require.Contains(t, status.Message, domain.OrganizationKind)
}

func TestListOrganizations_WithAuthFiltering(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)

	// Create three orgs ordered by ID: U1 (unauthorized), U2 and U3 (authorized)
	u1 := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	u2 := uuid.MustParse("00000000-0000-0000-0000-000000000022")
	u3 := uuid.MustParse("00000000-0000-0000-0000-000000000033")

	org1 := createTestOrganizationModel(u1, "ext-11", "Org-11")
	org2 := createTestOrganizationModel(u2, "ext-22", "Org-22")
	org3 := createTestOrganizationModel(u3, "ext-33", "Org-33")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1, org2, org3})

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
