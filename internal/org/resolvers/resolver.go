package resolvers

import (
	"context"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/org/providers"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type OrgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error)
	ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error)
	UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error)
}

type Resolver interface {
	EnsureExists(ctx context.Context, id uuid.UUID) error
	IsMemberOf(ctx context.Context, identity common.Identity, id uuid.UUID) (bool, error)
	GetUserOrganizations(ctx context.Context, identity common.Identity) ([]*model.Organization, error)
}

type BuildResolverOptions struct {
	Config *config.Config
	Store  OrgStore
	Log    logrus.FieldLogger
	Cache  cache.OrganizationCache
}

func BuildResolver(opts BuildResolverOptions) Resolver {
	if opts.Config != nil && opts.Config.Auth != nil && opts.Config.Organizations != nil && opts.Config.Organizations.Enabled {
		if opts.Config.Auth.OIDC != nil {
			return NewExternalResolver(opts.Store, opts.Cache, &providers.ClaimsProvider{}, opts.Log)
		} else if opts.Config.Auth.AAP != nil {
			opts.Log.Warn("AAP organizations are not supported yet, falling back to default resolver")
		} else if opts.Config.Auth.K8s != nil {
			opts.Log.Warn("K8s organizations are not supported yet, falling back to default resolver")
		} else {
			opts.Log.Warn("No auth provider properly configured, falling back to default resolver")
		}
	}

	return NewDefaultResolver(opts.Store, opts.Cache)
}
