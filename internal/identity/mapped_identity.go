package identity

import (
	orgmodel "github.com/flightctl/flightctl/internal/org/model"
)

// OrganizationRoles pairs an organization with its roles
type OrganizationRoles struct {
	Organization *orgmodel.Organization
	Roles        []string
}

// MappedIdentity represents an identity with all its mapped database objects
// This is the internal identity object that contains local DB entities
type MappedIdentity struct {
	// Organizations the user belongs to (mapped from external identity)
	Organizations []*orgmodel.Organization `json:"organizations"`

	// OrgRoles maps organization ID to roles for that organization
	OrgRoles map[string][]string `json:"org_roles"`

	// SuperAdmin indicates if the user has the global flightctl-admin role
	SuperAdmin bool `json:"is_super_admin"`

	// Original username and UID for reference
	Username string `json:"username"`
	UID      string `json:"uid"`

	// Issuer that produced this identity (OIDC, AAP, K8s, etc.)
	Issuer *Issuer `json:"issuer"`
}

// GetUsername returns the username
func (m *MappedIdentity) GetUsername() string {
	return m.Username
}

// GetUID returns the user ID
func (m *MappedIdentity) GetUID() string {
	return m.UID
}

// GetOrganizations returns the full organization objects
func (m *MappedIdentity) GetOrganizations() []*orgmodel.Organization {
	return m.Organizations
}

// GetOrgRoles returns a list of organization-roles pairs
func (m *MappedIdentity) GetOrgRoles() []OrganizationRoles {
	result := make([]OrganizationRoles, 0, len(m.Organizations))
	for _, org := range m.Organizations {
		roles := m.OrgRoles[org.ID.String()]
		result = append(result, OrganizationRoles{
			Organization: org,
			Roles:        roles,
		})
	}
	return result
}

// GetRolesForOrg returns the roles for a specific organization by ID
func (m *MappedIdentity) GetRolesForOrg(orgID string) []string {
	if roles, ok := m.OrgRoles[orgID]; ok {
		return roles
	}
	return []string{}
}

// IsSuperAdmin returns whether the user has the global flightctl-admin role
func (m *MappedIdentity) IsSuperAdmin() bool {
	return m.SuperAdmin
}

// GetIssuer returns the issuer that produced this identity
func (m *MappedIdentity) GetIssuer() *Issuer {
	return m.Issuer
}

// NewMappedIdentity creates a new MappedIdentity
func NewMappedIdentity(username, uid string, organizations []*orgmodel.Organization, orgRoles map[string][]string, isSuperAdmin bool, issuer *Issuer) *MappedIdentity {
	return &MappedIdentity{
		Username:      username,
		UID:           uid,
		Organizations: organizations,
		OrgRoles:      orgRoles,
		SuperAdmin:    isSuperAdmin,
		Issuer:        issuer,
	}
}
