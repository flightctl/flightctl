package identity

import (
	orgmodel "github.com/flightctl/flightctl/internal/org/model"
)

// MappedIdentity represents an identity with all its mapped database objects
// This is the internal identity object that contains local DB entities
type MappedIdentity struct {
	// Organizations the user belongs to (mapped from external identity)
	Organizations []*orgmodel.Organization `json:"organizations"`

	// Roles the user has (mapped from external identity)
	Roles []string `json:"roles"`

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

// GetRoles returns the roles
func (m *MappedIdentity) GetRoles() []string {
	return m.Roles
}

// GetIssuer returns the issuer that produced this identity
func (m *MappedIdentity) GetIssuer() *Issuer {
	return m.Issuer
}

// NewMappedIdentity creates a new MappedIdentity
func NewMappedIdentity(username, uid string, organizations []*orgmodel.Organization, roles []string, issuer *Issuer) *MappedIdentity {
	return &MappedIdentity{
		Username:      username,
		UID:           uid,
		Organizations: organizations,
		Roles:         roles,
		Issuer:        issuer,
	}
}
