package auth

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
)

// NilAuth is a special auth type that does nothing
type NilAuth struct{}

func (NilAuth) CheckPermission(_ context.Context, _ string, _ string) (bool, error) {
	return true, nil
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
		},
	}

	identity := common.NewBaseIdentity("nil-auth-user", "nil-auth-uid", organizations, []string{api.RoleAdmin})
	return identity, nil
}

func (NilAuth) GetAuthConfig() *api.AuthConfig {
	return &api.AuthConfig{}
}

func (NilAuth) GetAuthToken(_ *http.Request) (string, error) {
	return "nil-auth-token", nil
}
