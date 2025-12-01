package middleware

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/api/v1beta1"
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

// roleNameMap maps external role names to internal role constants
var roleNameMap = map[string]string{
	v1beta1.ExternalRoleAdmin:     v1beta1.RoleAdmin,
	v1beta1.ExternalRoleOrgAdmin:  v1beta1.RoleOrgAdmin,
	v1beta1.ExternalRoleOperator:  v1beta1.RoleOperator,
	v1beta1.ExternalRoleViewer:    v1beta1.RoleViewer,
	v1beta1.ExternalRoleInstaller: v1beta1.RoleInstaller,
}

// internalRoles is a set of all internal role names for quick lookup
var internalRoles = map[string]bool{
	v1beta1.RoleAdmin:     true,
	v1beta1.RoleOrgAdmin:  true,
	v1beta1.RoleOperator:  true,
	v1beta1.RoleViewer:    true,
	v1beta1.RoleInstaller: true,
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
		m.log.Debugf("Identity mapping middleware: attempting to map identity %s to database objects", identity.GetUsername())
		organizations, err := m.identityMapper.MapIdentityToDB(ctx, identity)
		if err != nil {
			m.log.Errorf("Failed to map identity to database objects: %v", err)
			http.Error(w, fmt.Sprintf("Failed to map identity: %v", err), http.StatusInternalServerError)
			return
		}
		m.log.Debugf("Identity mapping middleware: successfully mapped identity %s to %d organizations", identity.GetUsername(), len(organizations))

		// Build org roles map: map organization ID to roles
		// Match ReportedOrganizations (from identity) with database organizations
		orgRoles := make(map[string][]string)
		reportedOrgs := identity.GetOrganizations()

		// Create a lookup map from external ID to database organization
		orgsByExternalID := make(map[string]*model.Organization)
		for _, org := range organizations {
			orgsByExternalID[org.ExternalID] = org
		}

		// Match reported organizations with database organizations and extract roles
		for _, reportedOrg := range reportedOrgs {
			var dbOrg *model.Organization
			if reportedOrg.IsInternalID {
				// Find by internal ID
				for _, org := range organizations {
					if org.ID.String() == reportedOrg.ID {
						dbOrg = org
						break
					}
				}
			} else {
				// Find by external ID
				dbOrg = orgsByExternalID[reportedOrg.ID]
			}

			if dbOrg != nil {
				// Store roles keyed by organization ID, transforming role names
				orgRoles[dbOrg.ID.String()] = transformRoleNames(reportedOrg.Roles)
			}
		}

		// Create mapped identity object with all mapped DB entities
		// Copy super admin flag directly from auth identity
		mappedIdentity := identitylib.NewMappedIdentity(
			identity.GetUsername(),
			identity.GetUID(),
			organizations,
			orgRoles,
			identity.IsSuperAdmin(),
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

// transformRoleNames transforms external role names to internal role constants
// Maps flightctl-* prefixed roles to their internal names, skips internal role names, keeps unknown roles as-is
func transformRoleNames(roles []string) []string {
	if len(roles) == 0 {
		return roles
	}

	transformed := make([]string, 0, len(roles))
	for _, role := range roles {
		if mappedRole, exists := roleNameMap[role]; exists {
			// Transform external role to internal role
			transformed = append(transformed, mappedRole)
		} else if internalRoles[role] {
			// Skip roles that are already internal role names
			continue
		} else {
			// Keep unmapped roles as-is
			transformed = append(transformed, role)
		}
	}
	return transformed
}
