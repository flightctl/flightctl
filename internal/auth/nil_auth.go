package auth

import (
	"context"
	"net/http"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
)

type NilAuth struct{}

func (a NilAuth) ValidateToken(ctx context.Context, token string) error {
	return nil
}

func (a NilAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	identity := common.NewBaseIdentity("testuser", "testuser", []common.ReportedOrganization{
		{
			Name:         org.DefaultID.String(),
			IsInternalID: true,
			ID:           org.DefaultID.String(),
		},
	}, []string{v1alpha1.RoleAdmin})
	return identity, nil
}

func (a NilAuth) GetAuthConfig() *v1alpha1.AuthConfig {
	providerType := string(v1alpha1.AuthProviderInfoTypeOidc)
	return &v1alpha1.AuthConfig{
		DefaultProvider:      &providerType,
		OrganizationsEnabled: nil,
		Providers:            &[]v1alpha1.AuthProviderInfo{},
	}
}

func (NilAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	return true, nil
}

func (NilAuth) GetAuthToken(r *http.Request) (string, error) {
	return "", nil
}
