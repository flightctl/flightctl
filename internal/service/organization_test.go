package service

import (
	"context"
	"errors"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
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

// mock resolver that returns a fixed set of user organizations
type mockResolver struct {
	orgs []*model.Organization
}

func (m *mockResolver) EnsureExists(ctx context.Context, id uuid.UUID) error { return nil }
func (m *mockResolver) IsMemberOf(ctx context.Context, identity common.Identity, id uuid.UUID) (bool, error) {
	for _, o := range m.orgs {
		if o.ID == id {
			return true, nil
		}
	}
	return false, nil
}
func (m *mockResolver) GetUserOrganizations(ctx context.Context, identity common.Identity) ([]*model.Organization, error) {
	return m.orgs, nil
}

func TestListOrganizations_EmptyResult(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{})
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	result, status := handler.ListOrganizations(ctx, api.ListOrganizationsParams{})

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
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	expectedOrg := createExpectedAPIOrganization(orgID, "Default", "default-external-id")

	result, status := handler.ListOrganizations(ctx, api.ListOrganizationsParams{})

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
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	expectedOrg1 := createExpectedAPIOrganization(orgID1, "Organization One", "external-id-1")
	expectedOrg2 := createExpectedAPIOrganization(orgID2, "Organization Two", "external-id-2")

	result, status := handler.ListOrganizations(ctx, api.ListOrganizationsParams{})

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
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	result, status := handler.ListOrganizations(ctx, api.ListOrganizationsParams{})

	require.Nil(t, result)
	require.NotEqual(t, api.StatusOK(), status)
	require.Contains(t, status.Message, "database connection failed")
}

func TestListOrganizations_ResourceNotFoundError(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)
	setupMockStoreWithError(mockStore, flterrors.ErrResourceNotFound)
	ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

	result, status := handler.ListOrganizations(ctx, api.ListOrganizationsParams{})

	require.Nil(t, result)
	require.Equal(t, int32(404), status.Code)
	require.Contains(t, status.Message, api.OrganizationKind)
}

func TestListOrganizations_PaginationWithAuthFilteringProducesContinue(t *testing.T) {
	handler, mockStore := createServiceHandlerWithOrgMockStore(t)

	// Create three orgs ordered by ID: U1 (unauthorized), U2 and U3 (authorized)
	u1 := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	u2 := uuid.MustParse("00000000-0000-0000-0000-000000000022")
	u3 := uuid.MustParse("00000000-0000-0000-0000-000000000033")

	org1 := createTestOrganizationModel(u1, "ext-11", "Org-11")
	org2 := createTestOrganizationModel(u2, "ext-22", "Org-22")
	org3 := createTestOrganizationModel(u3, "ext-33", "Org-33")
	setupMockStoreWithOrganizations(mockStore, []*model.Organization{org1, org2, org3})

	// Inject resolver that authorizes only U2 and U3
	handler.orgResolver = &mockResolver{orgs: []*model.Organization{org2, org3}}

	// Build external request context with identity (non-internal)
	identity := common.NewBaseIdentity("tester", "uid-1", []string{})
	ctx := context.WithValue(context.Background(), consts.IdentityCtxKey, identity)

	// First page: limit=1, expect item U2 and a continue token present
	var limit int32 = 1
	list1, status := handler.ListOrganizations(ctx, api.ListOrganizationsParams{Limit: &limit})
	require.Equal(t, api.StatusOK(), status)
	require.NotNil(t, list1)
	require.Len(t, list1.Items, 1)
	require.NotNil(t, list1.Metadata.Continue)
	require.Equal(t, u2.String(), *list1.Items[0].Metadata.Name)

	// Second page: use continue from first response, expect U3 and no continue token
	cont := list1.Metadata.Continue
	list2, status2 := handler.ListOrganizations(ctx, api.ListOrganizationsParams{Limit: &limit, Continue: cont})
	require.Equal(t, api.StatusOK(), status2)
	require.NotNil(t, list2)
	require.Len(t, list2.Items, 1)
	require.Nil(t, list2.Metadata.Continue)
	require.Equal(t, u3.String(), *list2.Items[0].Metadata.Name)
}
