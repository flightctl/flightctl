package resolvers

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

// DefaultResolver is a shim over the default organization, and treats
// it as the only organization a user can be a member of.
type DefaultResolver struct {
	store OrgStore
	cache cache.OrganizationCache
}

func NewDefaultResolver(store OrgStore, cache cache.OrganizationCache) *DefaultResolver {
	return &DefaultResolver{store: store, cache: cache}
}

func (r *DefaultResolver) getDefaultOrg(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	if id != org.DefaultID {
		return nil, flterrors.ErrResourceNotFound
	}

	if item := r.cache.Get(id); item != nil {
		return item, nil
	}

	item, err := r.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	r.cache.Set(id, item)
	return item, nil
}

func (r *DefaultResolver) EnsureExists(ctx context.Context, id uuid.UUID) error {
	_, err := r.getDefaultOrg(ctx, id)
	return err
}

func (r *DefaultResolver) IsMemberOf(ctx context.Context, identity common.Identity, orgID uuid.UUID) (bool, error) {
	if identity == nil {
		return false, fmt.Errorf("identity is nil")
	}

	err := r.EnsureExists(ctx, orgID)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (r *DefaultResolver) GetUserOrganizations(ctx context.Context, identity common.Identity) ([]*model.Organization, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity is nil")
	}

	org, err := r.getDefaultOrg(ctx, org.DefaultID)
	if err != nil {
		return nil, err
	}

	return []*model.Organization{org}, nil
}
