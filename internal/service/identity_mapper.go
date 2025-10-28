package service

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
)

// IdentityMapper handles mapping from identity information to database entities
type IdentityMapper struct {
	store store.Store
	log   logrus.FieldLogger
	cache *ttlcache.Cache[string, *model.Organization]
}

// NewIdentityMapper creates a new IdentityMapper instance
func NewIdentityMapper(store store.Store, log logrus.FieldLogger) *IdentityMapper {
	cache := ttlcache.New(
		ttlcache.WithTTL[string, *model.Organization](10 * time.Minute),
	)

	return &IdentityMapper{
		store: store,
		log:   log,
		cache: cache,
	}
}

// Start starts the cache background cleanup
func (m *IdentityMapper) Start() {
	m.cache.Start()
}

// Stop stops the cache background cleanup
func (m *IdentityMapper) Stop() {
	m.cache.Stop()
}

// MapIdentityToDB maps identity information to database entities
// It ensures that organizations exist in the database and returns the mapped entities
// Note: Users are not stored in the database - only organizations are persisted
func (m *IdentityMapper) MapIdentityToDB(ctx context.Context, identity common.Identity) ([]*model.Organization, error) {

	organizations, err := m.ensureOrganizationsExist(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure organizations exist: %w", err)
	}

	return organizations, nil
}

// ensureOrganizationsExist ensures that organizations exist in the database
func (m *IdentityMapper) ensureOrganizationsExist(ctx context.Context, identity common.Identity) ([]*model.Organization, error) {
	reportedOrgs := identity.GetOrganizations()
	if len(reportedOrgs) == 0 {
		return []*model.Organization{}, nil
	}

	var organizations []*model.Organization
	var newOrgs []*model.Organization
	var uncachedReportedOrgs []common.ReportedOrganization

	// Check cache first for existing organizations
	for _, reportedOrg := range reportedOrgs {
		if cachedOrg := m.cache.Get(reportedOrg.ID); cachedOrg != nil {
			organizations = append(organizations, cachedOrg.Value())
		} else {
			uncachedReportedOrgs = append(uncachedReportedOrgs, reportedOrg)
		}
	}

	// If all organizations were cached, return them
	if len(uncachedReportedOrgs) == 0 {
		return organizations, nil
	}

	// Separate internal and external IDs
	var uncachedInternalIds []string
	var uncachedExternalIds []string
	for _, reportedOrg := range uncachedReportedOrgs {
		if reportedOrg.IsInternalID {
			uncachedInternalIds = append(uncachedInternalIds, reportedOrg.ID)
		} else {
			uncachedExternalIds = append(uncachedExternalIds, reportedOrg.ID)
		}
	}

	// Fetch organizations by internal IDs
	var existingOrgs []*model.Organization
	if len(uncachedInternalIds) > 0 {
		internalOrgs, err := m.store.Organization().ListByIDs(ctx, uncachedInternalIds)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch organizations by internal IDs: %w", err)
		}
		existingOrgs = append(existingOrgs, internalOrgs...)
	}

	// Fetch organizations by external IDs
	if len(uncachedExternalIds) > 0 {
		externalOrgs, err := m.store.Organization().ListByExternalIDs(ctx, uncachedExternalIds)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch organizations by external IDs: %w", err)
		}
		existingOrgs = append(existingOrgs, externalOrgs...)
	}

	// Create maps for quick lookup - by ID for internal and by ExternalID for external
	existingOrgByInternalID := make(map[string]*model.Organization)
	existingOrgByExternalID := make(map[string]*model.Organization)
	for _, org := range existingOrgs {
		existingOrgByInternalID[org.ID.String()] = org
		if org.ExternalID != "" {
			existingOrgByExternalID[org.ExternalID] = org
		}
		// Cache the found organization
		if org.ExternalID != "" {
			m.cache.Set(org.ExternalID, org, ttlcache.DefaultTTL)
		}
		m.cache.Set(org.ID.String(), org, ttlcache.DefaultTTL)
	}

	// Check which organizations need to be created and which already exist
	for _, reportedOrg := range uncachedReportedOrgs {
		var existingOrg *model.Organization
		if reportedOrg.IsInternalID {
			existingOrg = existingOrgByInternalID[reportedOrg.ID]
		} else {
			existingOrg = existingOrgByExternalID[reportedOrg.ID]
		}

		if existingOrg != nil {
			organizations = append(organizations, existingOrg)
		} else {
			// Organization doesn't exist, create new one (only for external IDs)
			if !reportedOrg.IsInternalID {
				newOrg := &model.Organization{
					ID:          uuid.New(),
					ExternalID:  reportedOrg.ID,
					DisplayName: reportedOrg.Name, // Use reported name as display name
				}
				newOrgs = append(newOrgs, newOrg)
			}

		}
	}

	// Create new organizations if any
	if len(newOrgs) > 0 {
		createdOrgs, err := m.store.Organization().UpsertMany(ctx, newOrgs)
		if err != nil {
			return nil, fmt.Errorf("failed to create organizations: %w", err)
		}
		// Cache the newly created organizations
		for _, org := range createdOrgs {
			m.cache.Set(org.ExternalID, org, ttlcache.DefaultTTL)
			m.cache.Set(org.ID.String(), org, ttlcache.DefaultTTL)
		}
		organizations = append(organizations, createdOrgs...)
		m.log.Infof("Created %d new organizations", len(createdOrgs))
	}

	return organizations, nil
}

// GetUserOrganizations returns the organizations for a user based on their identity
func (m *IdentityMapper) GetUserOrganizations(ctx context.Context, identity common.Identity) ([]*model.Organization, error) {
	organizations, err := m.MapIdentityToDB(ctx, identity)
	return organizations, err
}

// IsMemberOf checks if a user is a member of a specific organization
func (m *IdentityMapper) IsMemberOf(ctx context.Context, identity common.Identity, orgID uuid.UUID) (bool, error) {
	organizations, err := m.MapIdentityToDB(ctx, identity)
	if err != nil {
		return false, err
	}

	for _, org := range organizations {
		if org.ID == orgID {
			return true, nil
		}
	}

	return false, nil
}
