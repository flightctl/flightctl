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

func createTestOrganizationModel(id uuid.UUID, isDefault bool, externalID string) *model.Organization {
	return &model.Organization{
		ID:         id,
		IsDefault:  isDefault,
		ExternalID: externalID,
	}
}

func createExpectedAPIOrganization(id uuid.UUID, displayName string) api.Organization {
	return api.Organization{
		ApiVersion:  organizationApiVersion,
		Kind:        api.OrganizationKind,
		Id:          id,
		DisplayName: displayName,
	}
}

func setupMockStoreWithOrganizations(mockStore *TestStore, orgs []*model.Organization) {
	mockStore.Organization().(*DummyOrganization).organizations = &orgs
}

func setupMockStoreWithError(mockStore *TestStore, err error) {
	mockStore.Organization().(*DummyOrganization).err = err
}

func TestListUserOrganizations_EmptyResult(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{})
	ctx := context.Background()

	result, status := handler.ListUserOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Empty(t, result.Items)
	require.Equal(t, api.ListMeta{}, result.Metadata)
}

func TestListUserOrganizations_DefaultOrganization(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	orgID := uuid.New()
	defaultOrg := createTestOrganizationModel(orgID, true, "default-external-id")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{defaultOrg})
	ctx := context.Background()

	expectedOrg := createExpectedAPIOrganization(orgID, "Default")

	result, status := handler.ListUserOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 1)
	require.Equal(t, expectedOrg, result.Items[0])
	require.Equal(t, api.ListMeta{}, result.Metadata)
}

func TestListUserOrganizations_NonDefaultOrganization(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	orgID := uuid.New()
	nonDefaultOrg := createTestOrganizationModel(orgID, false, "external-id-123")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{nonDefaultOrg})
	ctx := context.Background()

	expectedOrg := createExpectedAPIOrganization(orgID, "Unknown")

	result, status := handler.ListUserOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 1)
	require.Equal(t, expectedOrg, result.Items[0])
	require.Equal(t, api.ListMeta{}, result.Metadata)
}

func TestListUserOrganizations_MultipleOrganizations(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)

	defaultOrgID := uuid.New()
	nonDefaultOrgID1 := uuid.New()
	nonDefaultOrgID2 := uuid.New()

	defaultOrg := createTestOrganizationModel(defaultOrgID, true, "default-external-id")
	nonDefaultOrg1 := createTestOrganizationModel(nonDefaultOrgID1, false, "external-id-1")
	nonDefaultOrg2 := createTestOrganizationModel(nonDefaultOrgID2, false, "external-id-2")

	orgs := []*model.Organization{defaultOrg, nonDefaultOrg1, nonDefaultOrg2}
	setupMockStoreWithOrganizations(mockStore, orgs)
	ctx := context.Background()

	expectedDefaultOrg := createExpectedAPIOrganization(defaultOrgID, "Default")
	expectedNonDefaultOrg1 := createExpectedAPIOrganization(nonDefaultOrgID1, "Unknown")
	expectedNonDefaultOrg2 := createExpectedAPIOrganization(nonDefaultOrgID2, "Unknown")

	result, status := handler.ListUserOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)
	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Len(t, result.Items, 3)

	require.Contains(t, result.Items, expectedDefaultOrg)
	require.Contains(t, result.Items, expectedNonDefaultOrg1)
	require.Contains(t, result.Items, expectedNonDefaultOrg2)
	require.Equal(t, api.ListMeta{}, result.Metadata)
}

func TestListUserOrganizations_StoreError(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	testError := errors.New("database connection failed")
	setupMockStoreWithError(mockStore, testError)
	ctx := context.Background()

	// Act
	result, status := handler.ListUserOrganizations(ctx)

	// Assert
	require.Nil(t, result)
	require.NotEqual(t, api.StatusOK(), status)
	require.Contains(t, status.Message, "database connection failed")
}

func TestListUserOrganizations_ResourceNotFoundError(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	setupMockStoreWithError(mockStore, flterrors.ErrResourceNotFound)
	ctx := context.Background()

	result, status := handler.ListUserOrganizations(ctx)

	require.Nil(t, result)
	require.Equal(t, int32(404), status.Code)
	require.Contains(t, status.Message, api.OrganizationKind)
}

func TestListUserOrganizations_APIResponseStructure(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	orgID := uuid.New()
	testOrg := createTestOrganizationModel(orgID, true, "test-external-id")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{testOrg})
	ctx := context.Background()

	result, status := handler.ListUserOrganizations(ctx)

	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, result)

	require.Equal(t, organizationApiVersion, result.ApiVersion)
	require.Equal(t, api.OrganizationListKind, result.Kind)
	require.Equal(t, api.ListMeta{}, result.Metadata)
	require.NotNil(t, result.Items)

	require.Len(t, result.Items, 1)
	org := result.Items[0]
	require.Equal(t, organizationApiVersion, org.ApiVersion)
	require.Equal(t, api.OrganizationKind, org.Kind)
	require.Equal(t, orgID, org.Id)
	require.NotEmpty(t, org.DisplayName)
}

func TestListUserOrganizations_DisplayNameLogic(t *testing.T) {
	tests := []struct {
		name            string
		isDefault       bool
		expectedDisplay string
	}{
		{
			name:            "Default organization shows 'Default'",
			isDefault:       true,
			expectedDisplay: "Default",
		},
		{
			name:            "Non-default organization shows 'Unknown'",
			isDefault:       false,
			expectedDisplay: "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, mockStore := createServiceHandlerWithOrgMockStore(t)
			orgID := uuid.New()
			testOrg := createTestOrganizationModel(orgID, tt.isDefault, "test-external-id")
			setupMockStoreWithOrganizations(mockStore, []*model.Organization{testOrg})
			ctx := context.Background()

			result, status := handler.ListUserOrganizations(ctx)

			require.Equal(t, api.StatusOK(), status)
			require.NotNil(t, result)
			require.Len(t, result.Items, 1)
			require.Equal(t, tt.expectedDisplay, result.Items[0].DisplayName)
		})
	}
}
