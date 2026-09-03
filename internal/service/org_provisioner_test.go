package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func createTestOrgProvisioner(catalogStore *fakeCatalogStore) *OrgProvisioner {
	return NewOrgProvisioner(catalogStore, logrus.New())
}

func TestEnsureDefaults_NewOrg_CreatesDefaultCatalog(t *testing.T) {
	catalogStore := &fakeCatalogStore{}
	provisioner := createTestOrgProvisioner(catalogStore)

	org := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}

	ctx := context.Background()
	provisioner.EnsureDefaults(ctx, []*model.Organization{org})

	catalog, status := catalogStore.GetCatalog(ctx, org.ID, domain.DefaultCatalogName)
	require.Equal(t, http.StatusOK, int(status.Code))
	require.NotNil(t, catalog)
	require.Equal(t, domain.DefaultCatalogName, *catalog.Metadata.Name)
	require.Equal(t, domain.DefaultCatalogDisplayName, *catalog.Spec.DisplayName)
}

func TestEnsureDefaults_ExistingCatalog_DoesNotDuplicate(t *testing.T) {
	catalogStore := &fakeCatalogStore{}
	provisioner := createTestOrgProvisioner(catalogStore)

	org := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}

	ctx := context.Background()

	// Provision once to create the catalog
	provisioner.EnsureDefaults(ctx, []*model.Organization{org})
	require.Len(t, catalogStore.catalogs, 1)

	// Provision again — should be a no-op
	provisioner.EnsureDefaults(ctx, []*model.Organization{org})
	require.Len(t, catalogStore.catalogs, 1, "Default catalog should not be duplicated")
}

func TestEnsureDefaults_MultipleOrgs_CreatesDefaultCatalogForEach(t *testing.T) {
	catalogStore := &fakeCatalogStore{}
	provisioner := createTestOrgProvisioner(catalogStore)

	org1 := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}
	org2 := &model.Organization{ID: uuid.New(), ExternalID: "org-2", DisplayName: "Organization 2"}

	ctx := context.Background()
	provisioner.EnsureDefaults(ctx, []*model.Organization{org1, org2})

	for _, org := range []*model.Organization{org1, org2} {
		catalog, status := catalogStore.GetCatalog(ctx, org.ID, domain.DefaultCatalogName)
		require.Equal(t, http.StatusOK, int(status.Code), "Default catalog should exist for org %s", org.ExternalID)
		require.NotNil(t, catalog)
		require.Equal(t, domain.DefaultCatalogName, *catalog.Metadata.Name)
	}
}

func TestEnsureDefaults_CatalogGetError_DoesNotPanic(t *testing.T) {
	catalogStore := &fakeCatalogStore{
		getErr: errors.New("database error"),
	}

	provisioner := createTestOrgProvisioner(catalogStore)
	org := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}

	// EnsureDefaults must not panic — errors are only logged, never returned
	require.NotPanics(t, func() {
		provisioner.EnsureDefaults(context.Background(), []*model.Organization{org})
	})

	// Catalog should not have been created since Get returned a non-NotFound error
	catalogStore.getErr = nil
	_, status := catalogStore.GetCatalog(context.Background(), org.ID, domain.DefaultCatalogName)
	require.Equal(t, http.StatusNotFound, int(status.Code), "No catalog should have been created when Get returns an unexpected error")
}
