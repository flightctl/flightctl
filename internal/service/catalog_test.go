package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func createTestCatalog(name string, owner *string) domain.Catalog {
	return domain.Catalog{
		ApiVersion: "v1alpha1",
		Kind:       "Catalog",
		Metadata: domain.ObjectMeta{
			Name:  lo.ToPtr(name),
			Owner: owner,
		},
		Spec: domain.CatalogSpec{},
	}
}

func createTestCatalogItem(catalogName, itemName string, owner *string) domain.CatalogItem {
	return domain.CatalogItem{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogItem",
		Metadata: domain.CatalogItemMeta{
			Name:    lo.ToPtr(itemName),
			Catalog: catalogName,
			Owner:   owner,
		},
		Spec: domain.CatalogItemSpec{
			Artifacts: []domain.CatalogItemArtifact{
				{
					Type: domain.CatalogItemArtifactTypeContainer,
					Uri:  "quay.io/example/app",
				},
			},
			Type: domain.CatalogItemTypeContainer,
			Versions: []domain.CatalogItemVersion{
				{
					Version:    "1.0.0",
					Channels:   []string{"stable"},
					References: map[string]string{"container": "v1.0.0"},
				},
			},
		},
	}
}

func createCatalogTestServiceHandler() *ServiceHandler {
	testStore := &TestStore{}
	wc := &DummyWorkerClient{}
	return &ServiceHandler{
		eventHandler: NewEventHandler(testStore, wc, log.InitLogs()),
		store:        testStore,
		workerClient: wc,
	}
}

func TestDeleteCatalog(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	tests := []struct {
		name                  string
		catalogName           string
		catalogOwner          *string
		createCatalog         bool
		isResourceSyncRequest bool
		expectedStatusCode    int32
		expectedError         error
		expectCatalogDeleted  bool
	}{
		{
			name:                 "delete catalog without owner succeeds",
			catalogName:          "test-catalog",
			catalogOwner:         nil,
			createCatalog:        true,
			expectedStatusCode:   statusSuccessCode,
			expectCatalogDeleted: true,
		},
		{
			name:                 "delete non-existent catalog returns OK (idempotent)",
			catalogName:          "nonexistent-catalog",
			createCatalog:        false,
			expectedStatusCode:   statusSuccessCode,
			expectCatalogDeleted: true,
		},
		{
			name:                 "delete catalog with owner fails with conflict",
			catalogName:          "owned-catalog",
			catalogOwner:         &owner,
			createCatalog:        true,
			expectedStatusCode:   int32(http.StatusConflict),
			expectedError:        flterrors.ErrDeletingResourceWithOwnerNotAllowed,
			expectCatalogDeleted: false,
		},
		{
			name:                  "resourceSync can delete catalogs it owns",
			catalogName:           "resourcesync-owned-catalog",
			catalogOwner:          &owner,
			createCatalog:         true,
			isResourceSyncRequest: true,
			expectedStatusCode:    statusSuccessCode,
			expectCatalogDeleted:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			serviceHandler := createCatalogTestServiceHandler()
			ctx := context.Background()
			testOrgId := uuid.New()

			if tt.createCatalog {
				catalog := createTestCatalog(tt.catalogName, tt.catalogOwner)
				_, err := serviceHandler.store.Catalog().Create(ctx, testOrgId, &catalog, serviceHandler.callbackCatalogUpdated)
				require.NoError(err)
			}

			deleteCtx := ctx
			if tt.isResourceSyncRequest {
				deleteCtx = context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
			}

			status := serviceHandler.DeleteCatalog(deleteCtx, testOrgId, tt.catalogName)
			require.Equal(tt.expectedStatusCode, status.Code)

			if tt.expectedError != nil {
				require.Equal(tt.expectedError.Error(), status.Message)
			}

			_, getStatus := serviceHandler.GetCatalog(ctx, testOrgId, tt.catalogName)
			if tt.expectCatalogDeleted {
				require.Equal(statusNotFoundCode, getStatus.Code)
			} else {
				require.Equal(statusSuccessCode, getStatus.Code)
			}
		})
	}
}

func TestDeleteCatalogItem(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	tests := []struct {
		name                  string
		catalogName           string
		itemName              string
		itemOwner             *string
		createItem            bool
		isResourceSyncRequest bool
		expectedStatusCode    int32
		expectedError         error
		expectItemDeleted     bool
	}{
		{
			name:               "delete item without owner succeeds",
			catalogName:        "test-catalog",
			itemName:           "test-item",
			itemOwner:          nil,
			createItem:         true,
			expectedStatusCode: statusSuccessCode,
			expectItemDeleted:  true,
		},
		{
			name:               "delete non-existent item returns OK (idempotent)",
			catalogName:        "test-catalog",
			itemName:           "nonexistent-item",
			createItem:         false,
			expectedStatusCode: statusSuccessCode,
			expectItemDeleted:  true,
		},
		{
			name:               "delete item with owner fails with conflict",
			catalogName:        "test-catalog",
			itemName:           "owned-item",
			itemOwner:          &owner,
			createItem:         true,
			expectedStatusCode: int32(http.StatusConflict),
			expectedError:      flterrors.ErrDeletingResourceWithOwnerNotAllowed,
			expectItemDeleted:  false,
		},
		{
			name:                  "resourceSync can delete items it owns",
			catalogName:           "test-catalog",
			itemName:              "rs-owned-item",
			itemOwner:             &owner,
			createItem:            true,
			isResourceSyncRequest: true,
			expectedStatusCode:    statusSuccessCode,
			expectItemDeleted:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			serviceHandler := createCatalogTestServiceHandler()
			ctx := context.Background()
			testOrgId := uuid.New()

			catalog := createTestCatalog(tt.catalogName, nil)
			_, err := serviceHandler.store.Catalog().Create(ctx, testOrgId, &catalog, nil)
			require.NoError(err)

			if tt.createItem {
				item := createTestCatalogItem(tt.catalogName, tt.itemName, tt.itemOwner)
				_, err := serviceHandler.store.Catalog().CreateItem(ctx, testOrgId, tt.catalogName, &item)
				require.NoError(err)
			}

			deleteCtx := ctx
			if tt.isResourceSyncRequest {
				deleteCtx = context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
			}

			status := serviceHandler.DeleteCatalogItem(deleteCtx, testOrgId, tt.catalogName, tt.itemName)
			require.Equal(tt.expectedStatusCode, status.Code)

			if tt.expectedError != nil {
				require.Equal(tt.expectedError.Error(), status.Message)
			}

			_, getErr := serviceHandler.store.Catalog().GetItem(ctx, testOrgId, tt.catalogName, tt.itemName)
			if tt.expectItemDeleted {
				require.ErrorIs(getErr, flterrors.ErrResourceNotFound)
			} else {
				require.NoError(getErr)
			}
		})
	}
}

func TestReplaceCatalogItemOwnerCheck(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	tests := []struct {
		name                  string
		itemOwner             *string
		isResourceSyncRequest bool
		expectedStatusCode    int32
		expectedError         error
	}{
		{
			name:               "replace item without owner succeeds",
			itemOwner:          nil,
			expectedStatusCode: statusSuccessCode,
		},
		{
			name:               "replace item with owner fails with conflict",
			itemOwner:          &owner,
			expectedStatusCode: int32(http.StatusConflict),
			expectedError:      flterrors.ErrUpdatingResourceWithOwnerNotAllowed,
		},
		{
			name:                  "resourceSync can replace items it owns",
			itemOwner:             &owner,
			isResourceSyncRequest: true,
			expectedStatusCode:    statusSuccessCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			serviceHandler := createCatalogTestServiceHandler()
			ctx := context.Background()
			testOrgId := uuid.New()

			catalogName := "test-catalog"
			itemName := "test-item"

			catalog := createTestCatalog(catalogName, nil)
			_, err := serviceHandler.store.Catalog().Create(ctx, testOrgId, &catalog, nil)
			require.NoError(err)

			item := createTestCatalogItem(catalogName, itemName, tt.itemOwner)
			_, err = serviceHandler.store.Catalog().CreateItem(ctx, testOrgId, catalogName, &item)
			require.NoError(err)

			replaceCtx := ctx
			if tt.isResourceSyncRequest {
				replaceCtx = context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
			}

			updatedItem := createTestCatalogItem(catalogName, itemName, nil)
			_, status := serviceHandler.ReplaceCatalogItem(replaceCtx, testOrgId, catalogName, itemName, updatedItem)
			require.Equal(tt.expectedStatusCode, status.Code)

			if tt.expectedError != nil {
				require.Equal(tt.expectedError.Error(), status.Message)
			}
		})
	}
}

func TestReplaceCatalogItemCreatePath(t *testing.T) {
	// Creating a new item via Replace should always succeed (no existing owner to check)
	require := require.New(t)
	serviceHandler := createCatalogTestServiceHandler()
	ctx := context.Background()
	testOrgId := uuid.New()

	catalogName := "test-catalog"
	itemName := "new-item"

	catalog := createTestCatalog(catalogName, nil)
	_, err := serviceHandler.store.Catalog().Create(ctx, testOrgId, &catalog, nil)
	require.NoError(err)

	item := createTestCatalogItem(catalogName, itemName, nil)
	result, status := serviceHandler.ReplaceCatalogItem(ctx, testOrgId, catalogName, itemName, item)
	require.Equal(statusCreatedCode, status.Code)
	require.NotNil(result)
}
