package service

import (
	"context"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/catalog"
	"github.com/flightctl/flightctl/internal/service/common"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// OrgProvisionerInterface is implemented by anything that can provision default resources for new orgs.
type OrgProvisionerInterface interface {
	EnsureDefaults(ctx context.Context, orgs []*model.Organization)
}

// OrgProvisioner ensures that default resources exist for every organization.
// It is called by IdentityMapper only when new organizations are created,
// so each org is provisioned at most once per identity-mapper cache TTL.
type OrgProvisioner struct {
	catalogs        catalog.Service
	initialMappings *labelsyncmappingservice.InitialLabelSyncMappingProvisioner
	log             logrus.FieldLogger
}

// Ensure OrgProvisioner satisfies the interface.
var _ OrgProvisionerInterface = (*OrgProvisioner)(nil)

// NewOrgProvisioner creates a new OrgProvisioner.
func NewOrgProvisioner(catalogs catalog.Service, log logrus.FieldLogger, initialMappings ...*labelsyncmappingservice.InitialLabelSyncMappingProvisioner) *OrgProvisioner {
	provisioner := &OrgProvisioner{catalogs: catalogs, log: log}
	if len(initialMappings) > 0 {
		provisioner.initialMappings = initialMappings[0]
	}
	return provisioner
}

// NewOrgProvisionerWithInitialMappings creates an organization provisioner that
// seeds deployment-provided label-sync mappings for each newly-created organization.
func NewOrgProvisionerWithInitialMappings(
	catalogs catalog.Service,
	mappings labelsyncmappingservice.Service,
	filePath string,
	log logrus.FieldLogger,
) (*OrgProvisioner, error) {
	initialMappings, err := labelsyncmappingservice.NewInitialLabelSyncMappingProvisioner(filePath, mappings, log)
	if err != nil {
		return nil, err
	}
	return NewOrgProvisioner(catalogs, log, initialMappings), nil
}

// EnsureDefaults creates default resources for the given newly-created organizations.
// It is only called for orgs that IdentityMapper just created via UpsertMany.
// Errors are logged and not returned; missing defaults are non-fatal to request handling.
func (p *OrgProvisioner) EnsureDefaults(ctx context.Context, orgs []*model.Organization) {
	for _, o := range orgs {
		p.ensureDefaultCatalog(ctx, o.ID)
		if p.initialMappings != nil {
			p.initialMappings.Provision(ctx, o.ID)
		}
	}
}

// ensureDefaultCatalog creates the default catalog for the org if it does not yet exist.
func (p *OrgProvisioner) ensureDefaultCatalog(ctx context.Context, orgID uuid.UUID) {
	_, status := p.catalogs.GetCatalog(ctx, orgID, domain.DefaultCatalogName)
	if status.Code == http.StatusOK {
		return
	}
	if status.Code != http.StatusNotFound {
		p.log.WithError(common.ApiStatusToErr(status)).Errorf("Failed to check default catalog for org %s", orgID)
		return
	}

	displayName := domain.DefaultCatalogDisplayName
	name := domain.DefaultCatalogName
	_, status = p.catalogs.CreateCatalog(ctx, orgID, domain.Catalog{
		Metadata: domain.ObjectMeta{
			Name: &name,
		},
		Spec: domain.CatalogSpec{
			DisplayName: &displayName,
		},
	})
	if err := common.ApiStatusToErr(status); err != nil {
		p.log.WithError(err).Errorf("Failed to create default catalog for org %s", orgID)
	}
}
