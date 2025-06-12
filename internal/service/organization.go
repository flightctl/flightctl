package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
)

// GetAllOrganizations returns a list of ALL organizations.
//
// Usage is currently intended for internal purposes only such as fetching
// organizations for tasks that require unscoped knowledge of all organizations
func (h *ServiceHandler) ListAllOrganizationIDs(ctx context.Context) ([]uuid.UUID, api.Status) {
	// For now, we only have a single hardcoded default organization
	return []uuid.UUID{store.NullOrgId}, api.StatusOK()
}
