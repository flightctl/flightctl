package service

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func createTestOrgProvisioner(mockStore *TestStore) *OrgProvisioner {
	return NewOrgProvisioner(mockStore, logrus.New())
}

func TestEnsureDefaults_NewOrg_CreatesDefaultCatalog(t *testing.T) {
	mockStore := &TestStore{}
	provisioner := createTestOrgProvisioner(mockStore)

	org := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}

	ctx := context.Background()
	provisioner.EnsureDefaults(ctx, []*model.Organization{org})

	catalog, err := mockStore.Catalog().Get(ctx, org.ID, domain.DefaultCatalogName)
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Equal(t, domain.DefaultCatalogName, *catalog.Metadata.Name)
	require.Equal(t, domain.DefaultCatalogDisplayName, *catalog.Spec.DisplayName)
}

func TestEnsureDefaults_ExistingCatalog_DoesNotDuplicate(t *testing.T) {
	mockStore := &TestStore{}
	provisioner := createTestOrgProvisioner(mockStore)

	org := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}

	ctx := context.Background()

	// Provision once to create the catalog
	provisioner.EnsureDefaults(ctx, []*model.Organization{org})
	require.Len(t, *mockStore.catalogs.catalogs, 1)

	// Provision again — should be a no-op
	provisioner.EnsureDefaults(ctx, []*model.Organization{org})
	require.Len(t, *mockStore.catalogs.catalogs, 1, "Default catalog should not be duplicated")
}

func TestEnsureDefaults_MultipleOrgs_CreatesDefaultCatalogForEach(t *testing.T) {
	mockStore := &TestStore{}
	provisioner := createTestOrgProvisioner(mockStore)

	org1 := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}
	org2 := &model.Organization{ID: uuid.New(), ExternalID: "org-2", DisplayName: "Organization 2"}

	ctx := context.Background()
	provisioner.EnsureDefaults(ctx, []*model.Organization{org1, org2})

	for _, org := range []*model.Organization{org1, org2} {
		catalog, err := mockStore.Catalog().Get(ctx, org.ID, domain.DefaultCatalogName)
		require.NoError(t, err, "Default catalog should exist for org %s", org.ExternalID)
		require.NotNil(t, catalog)
		require.Equal(t, domain.DefaultCatalogName, *catalog.Metadata.Name)
	}
}

func TestEnsureDefaults_CatalogGetError_DoesNotPanic(t *testing.T) {
	mockStore := &TestStore{}
	mockStore.init()
	mockStore.catalogs = &DummyCatalog{
		catalogs: &[]domain.Catalog{},
		items:    &[]domain.CatalogItem{},
		getErr:   errors.New("database error"),
	}

	provisioner := createTestOrgProvisioner(mockStore)
	org := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}

	// EnsureDefaults must not panic — errors are only logged, never returned
	require.NotPanics(t, func() {
		provisioner.EnsureDefaults(context.Background(), []*model.Organization{org})
	})

	// Catalog should not have been created since Get returned a non-NotFound error
	mockStore.catalogs.getErr = nil
	_, err := mockStore.Catalog().Get(context.Background(), org.ID, domain.DefaultCatalogName)
	require.Error(t, err, "No catalog should have been created when Get returns an unexpected error")
}
