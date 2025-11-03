package middleware

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	identitylib "github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/sirupsen/logrus"
)

// IdentityMappingMiddleware maps identity information to local database objects
// This middleware sits between authentication and organization middleware
type IdentityMappingMiddleware struct {
	identityMapper *service.IdentityMapper
	log            logrus.FieldLogger
}

// NewIdentityMappingMiddleware creates a new identity mapping middleware
func NewIdentityMappingMiddleware(identityMapper *service.IdentityMapper, log logrus.FieldLogger) *IdentityMappingMiddleware {
	return &IdentityMappingMiddleware{
		identityMapper: identityMapper,
		log:            log,
	}
}

// MapIdentityToDB is the middleware function that maps identity to database objects
func (m *IdentityMappingMiddleware) MapIdentityToDB(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Get identity from context (set by authentication middleware)
		identity, err := common.GetIdentity(ctx)
		if err != nil {
			// If no identity, continue without mapping (might be a public endpoint)
			m.log.Debugf("No identity found in context, skipping identity mapping: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		m.log.Debugf("Identity mapping middleware: processing identity %s with %d organizations",
			identity.GetUsername(), len(identity.GetOrganizations()))

		// Map identity to database objects (organizations)
		m.log.Infof("Identity mapping middleware: attempting to map identity %s to database objects", identity.GetUsername())
		organizations, err := m.identityMapper.MapIdentityToDB(ctx, identity)
		if err != nil {
			m.log.Errorf("Failed to map identity to database objects: %v", err)
			http.Error(w, fmt.Sprintf("Failed to map identity: %v", err), http.StatusInternalServerError)
			return
		}
		m.log.Infof("Identity mapping middleware: successfully mapped identity %s to %d organizations", identity.GetUsername(), len(organizations))

		// Create mapped identity object with all mapped DB entities
		mappedIdentity := identitylib.NewMappedIdentity(
			identity.GetUsername(),
			identity.GetUID(),
			organizations,
			identity.GetRoles(),
			identity.GetIssuer(),
		)

		// Set mapped identity in context for downstream use
		ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)

		// Log the mapping for debugging
		m.log.Debugf("Mapped identity %s to %d organizations: %v",
			identity.GetUsername(), len(organizations), getOrganizationNames(organizations))

		// Additional debug logging for organization IDs
		orgIDs := make([]string, len(organizations))
		for i, org := range organizations {
			orgIDs[i] = org.ID.String()
		}
		m.log.Debugf("Mapped identity organizations IDs: %v", orgIDs)

		// Continue to next middleware/handler
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// getOrganizationNames extracts organization names for logging
func getOrganizationNames(organizations []*model.Organization) []string {
	names := make([]string, len(organizations))
	for i, org := range organizations {
		names[i] = org.DisplayName
	}
	return names
}
