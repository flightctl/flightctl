package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func createTestOrgProvisioner(catalogStore *fakeCatalogStore) *OrgProvisioner {
	return NewOrgProvisioner(catalogStore, logrus.New())
}

func TestEnsureDefaults_NewOrg_CreatesDefaultCatalog(t *testing.T) {
	catalogStore := &fakeCatalogStore{catalogs: make(map[catalogKey]*domain.Catalog)}
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
	catalogStore := &fakeCatalogStore{catalogs: make(map[catalogKey]*domain.Catalog)}
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
	catalogStore := &fakeCatalogStore{catalogs: make(map[catalogKey]*domain.Catalog)}
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
		catalogs: make(map[catalogKey]*domain.Catalog),
		getErr:   errors.New("database error"),
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

func TestEnsureDefaults_SeedsInitialMappingsForEachNewOrg(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "mappings.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(`
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: system-info
  spec:
    resourceType: Device
    expression: '{}'
`), 0600))

	org1 := &model.Organization{ID: uuid.New(), ExternalID: "org-1", DisplayName: "Organization 1"}
	org2 := &model.Organization{ID: uuid.New(), ExternalID: "org-2", DisplayName: "Organization 2"}
	ctrl := gomock.NewController(t)
	mappingService := labelsyncmappingservice.NewMockService(ctrl)
	mappingService.EXPECT().CreateLabelSyncMapping(gomock.Any(), org1.ID, gomock.Any()).DoAndReturn(
		func(_ context.Context, orgID uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
			require.Equal(t, "system-info", *mapping.Metadata.Name)
			return &mapping, domain.StatusOK()
		},
	)
	mappingService.EXPECT().CreateLabelSyncMapping(gomock.Any(), org2.ID, gomock.Any()).DoAndReturn(
		func(_ context.Context, orgID uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
			require.Equal(t, "system-info", *mapping.Metadata.Name)
			return &mapping, domain.StatusOK()
		},
	)

	seeder, err := labelsyncmappingservice.NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logrus.New())
	require.NoError(t, err)
	catalogStore := &fakeCatalogStore{catalogs: make(map[catalogKey]*domain.Catalog)}
	provisioner := NewOrgProvisioner(catalogStore, logrus.New(), seeder)

	provisioner.EnsureDefaults(context.Background(), []*model.Organization{org1, org2})
}
