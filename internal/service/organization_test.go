package service

import (
	"context"
	"errors"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func createServiceHandlerWithOrgMockStore(t *testing.T) (*ServiceHandler, *TestStore) {
	mockStore := &TestStore{}
	handler := &ServiceHandler{
		store: mockStore,
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

func createExpectedAPIOrganization(id uuid.UUID, displayName string, externalID string) api.Organization {
	name := id.String()
	return api.Organization{
		ApiVersion: organizationApiVersion,
		Kind:       api.OrganizationKind,
		Metadata:   api.ObjectMeta{Name: &name},
		Spec: &api.OrganizationSpec{
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
	ctx := context.Background()

	result, status := handler.ListOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Empty(t, result.Items)
	require.Equal(t, api.ListMeta{}, result.Metadata)
}

func TestListOrganizations_SingleOrganization(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	orgID := uuid.New()
	defaultOrg := createTestOrganizationModel(orgID, "default-external-id", "Default")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{defaultOrg})
	ctx := context.Background()

	expectedOrg := createExpectedAPIOrganization(orgID, "Default", "default-external-id")

	result, status := handler.ListOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 1)
	require.Equal(t, expectedOrg, result.Items[0])
	require.Equal(t, api.ListMeta{}, result.Metadata)
}

func TestListOrganizations_MultipleOrganizations(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)

	orgID1 := uuid.New()
	orgID2 := uuid.New()

	org1 := createTestOrganizationModel(orgID1, "external-id-1", "Organization One")
	org2 := createTestOrganizationModel(orgID2, "external-id-2", "Organization Two")

	orgs := []*model.Organization{org1, org2}
	setupMockStoreWithOrganizations(mockStore, orgs)
	ctx := context.Background()

	expectedOrg1 := createExpectedAPIOrganization(orgID1, "Organization One", "external-id-1")
	expectedOrg2 := createExpectedAPIOrganization(orgID2, "Organization Two", "external-id-2")

	result, status := handler.ListOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 2)

	require.Contains(t, result.Items, expectedOrg1)
	require.Contains(t, result.Items, expectedOrg2)
	require.Equal(t, api.ListMeta{}, result.Metadata)
}

func TestListOrganizations_StoreError(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	testError := errors.New("database connection failed")
	setupMockStoreWithError(mockStore, testError)
	ctx := context.Background()

	result, status := handler.ListOrganizations(ctx)

	require.Nil(t, result)
	require.NotEqual(t, api.StatusOK(), status)
	require.Contains(t, status.Message, "database connection failed")
}

func TestListOrganizations_ResourceNotFoundError(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	setupMockStoreWithError(mockStore, flterrors.ErrResourceNotFound)
	ctx := context.Background()

	result, status := handler.ListOrganizations(ctx)

	require.Nil(t, result)
	require.Equal(t, int32(404), status.Code)
	require.Contains(t, status.Message, api.OrganizationKind)
}
