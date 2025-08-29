package resolvers

import (
	"context"
	"fmt"
	"sync"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

// DefaultResolver is a shim over the default organization, and treats
// it as the only organization a user can be a member of.
type DefaultResolver struct {
	store      OrgStore
	defaultOrg *model.Organization
	mu         sync.Mutex
}

func NewDefaultResolver(store OrgStore) *DefaultResolver {
	return &DefaultResolver{store: store}
}

func (r *DefaultResolver) getDefaultOrg(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	if id != org.DefaultID {
		return nil, fmt.Errorf("organization not found")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.defaultOrg != nil {
		return r.defaultOrg, nil
	}

	org, err := r.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	r.defaultOrg = org
	return r.defaultOrg, nil
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
