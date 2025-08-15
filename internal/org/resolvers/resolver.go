package resolvers

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
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

const cacheExpirationTime = 10 * time.Minute

func BuildResolver(ctx context.Context, cfg *config.Config, store OrgStore, log logrus.FieldLogger) Resolver {
	if cfg != nil && cfg.Auth != nil && cfg.Organizations != nil && cfg.Organizations.Enabled {
		if cfg.Auth.OIDC != nil {
			return NewExternalResolver(ctx, store, cacheExpirationTime, &providers.ClaimsProvider{}, log)
		} else if cfg.Auth.AAP != nil {
			log.Warn("AAP organizations are not supported yet, falling back to default resolver")
		} else if cfg.Auth.K8s != nil {
			log.Warn("K8s organizations are not supported yet, falling back to default resolver")
		} else {
			log.Warn("No auth provider properly configured, falling back to default resolver")
		}
	}

	return NewDefaultResolver(store)
}
