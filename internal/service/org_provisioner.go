package service

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
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
	store store.Store
	log   logrus.FieldLogger
}

// Ensure OrgProvisioner satisfies the interface.
var _ OrgProvisionerInterface = (*OrgProvisioner)(nil)

// NewOrgProvisioner creates a new OrgProvisioner.
func NewOrgProvisioner(store store.Store, log logrus.FieldLogger) *OrgProvisioner {
	return &OrgProvisioner{store: store, log: log}
}

// EnsureDefaults creates default resources for the given newly-created organizations.
// It is only called for orgs that IdentityMapper just created via UpsertMany.
// Errors are logged and not returned; missing defaults are non-fatal to request handling.
func (p *OrgProvisioner) EnsureDefaults(ctx context.Context, orgs []*model.Organization) {
	for _, o := range orgs {
		p.ensureDefaultCatalog(ctx, o.ID)
	}
}

// ensureDefaultCatalog creates the default catalog for the org if it does not yet exist.
func (p *OrgProvisioner) ensureDefaultCatalog(ctx context.Context, orgID uuid.UUID) {
	_, err := p.store.Catalog().Get(ctx, orgID, domain.DefaultCatalogName)
	if err == nil {
		return
	}
	if !errors.Is(err, flterrors.ErrResourceNotFound) {
		p.log.WithError(err).Errorf("Failed to check default catalog for org %s", orgID)
		return
	}

	displayName := domain.DefaultCatalogDisplayName
	name := domain.DefaultCatalogName
	_, err = p.store.Catalog().Create(ctx, orgID, &domain.Catalog{
		Metadata: domain.ObjectMeta{
			Name: &name,
		},
		Spec: domain.CatalogSpec{
			DisplayName: &displayName,
		},
	}, nil)
	if err != nil {
		p.log.WithError(err).Errorf("Failed to create default catalog for org %s", orgID)
	}
}
