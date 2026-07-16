package authprovider

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateAuthProvider(ctx context.Context, orgId uuid.UUID, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status)
	ListAuthProviders(ctx context.Context, orgId uuid.UUID, params domain.ListAuthProvidersParams) (*domain.AuthProviderList, domain.Status)
	ListAllAuthProviders(ctx context.Context, params domain.ListAuthProvidersParams) (*domain.AuthProviderList, domain.Status)
	GetAuthProvider(ctx context.Context, orgId uuid.UUID, name string) (*domain.AuthProvider, domain.Status)
	GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*domain.AuthProvider, domain.Status)
	GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*domain.AuthProvider, domain.Status)
	ReplaceAuthProvider(ctx context.Context, orgId uuid.UUID, name string, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status)
	PatchAuthProvider(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.AuthProvider, domain.Status)
	DeleteAuthProvider(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	GetAuthConfig(ctx context.Context, authConfig *domain.AuthConfig) (*domain.AuthConfig, domain.Status)
}
