package auth

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
)

// NilAuth is a special auth type that does nothing
type NilAuth struct{}

func (NilAuth) CheckPermission(_ context.Context, _ string, _ string) (bool, error) {
	return true, nil
}

func (NilAuth) GetUserPermissions(_ context.Context) (*api.PermissionList, error) {
	// When auth is disabled, return all permissions
	return &api.PermissionList{
		Permissions: []api.Permission{
			{
				Resource:   "*",
				Operations: []string{"*"},
			},
		},
	}, nil
}

func (NilAuth) IsEnabled() bool {
	return true
}

func (NilAuth) ValidateToken(_ context.Context, _ string) error {
	return nil
}

func (NilAuth) GetIdentity(_ context.Context, _ string) (Identity, error) {
	// When auth is disabled, create a default identity with access to the default organization
	organizations := []common.ReportedOrganization{
		{
			Name:         org.DefaultExternalID,
			IsInternalID: true,
			ID:           org.DefaultID.String(),
			Roles:        []string{api.ExternalRoleAdmin}, // Admin role for nil auth
		},
	}

	identity := common.NewBaseIdentity("nil-auth-user", "nil-auth-uid", organizations)
	identity.SetSuperAdmin(true) // Nil auth users are super admins
	return identity, nil
}

func (NilAuth) GetAuthConfig() *api.AuthConfig {
	return &api.AuthConfig{
		ApiVersion: api.AuthConfigAPIVersion,
	}
}

func (NilAuth) GetAuthToken(_ *http.Request) (string, error) {
	return "nil-auth-token", nil
}
