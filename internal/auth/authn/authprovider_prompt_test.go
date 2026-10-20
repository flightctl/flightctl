package authn

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	authproviderservice "github.com/flightctl/flightctl/internal/service/authprovider"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMultiAuth_PromptUpdates(t *testing.T) {
	for _, providerType := range []string{"oidc", "oauth2"} {
		t.Run(providerType, func(t *testing.T) {
			base := createBasicOAuth2Spec(t)
			provider := api.AuthProvider{
				ApiVersion: api.AuthProviderAPIVersion,
				Kind:       api.AuthProviderKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr("prompt-provider")},
			}
			setPrompt := func(prompt *string) {
				if providerType == "oidc" {
					require.NoError(t, provider.Spec.FromOIDCProviderSpec(api.OIDCProviderSpec{
						ProviderType:           api.Oidc,
						Issuer:                 *base.Issuer,
						ClientId:               base.ClientId,
						ClientSecret:           base.ClientSecret,
						Enabled:                base.Enabled,
						OrganizationAssignment: base.OrganizationAssignment,
						RoleAssignment:         base.RoleAssignment,
						Prompt:                 prompt,
					}))
				} else {
					base.Prompt = prompt
					require.NoError(t, provider.Spec.FromOAuth2ProviderSpec(base))
				}
			}
			service := authproviderservice.NewMockService(gomock.NewController(t))
			service.EXPECT().ListAllAuthProviders(gomock.Any(), gomock.Any()).DoAndReturn(
				func(context.Context, api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status) {
					return &api.AuthProviderList{Items: []api.AuthProvider{provider}}, api.StatusOK()
				}).AnyTimes()
			multiAuth := NewMultiAuth(service, nil, logrus.New())
			t.Cleanup(func() {
				for _, middleware := range multiAuth.dynamicProviders {
					if lifecycle, ok := middleware.(AuthProviderWithLifecycle); ok {
						lifecycle.Stop()
					}
				}
			})
			var previous common.AuthNMiddleware
			tests := []struct {
				name   string
				prompt *string
				want   string
			}{
				{"When a legacy provider omits prompt it should expose select_account", nil, "select_account"},
				{"When prompt is explicitly empty it should expose empty", lo.ToPtr(""), ""},
				{"When prompt changes to login it should expose login", lo.ToPtr("login"), "login"},
				{"When prompt contains extensions and spaces it should preserve them", lo.ToPtr(" custom  consent "), " custom  consent "},
				{"When prompt is removed it should expose select_account", nil, "select_account"},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					setPrompt(tt.prompt)
					require.NoError(t, multiAuth.LoadAllAuthProviders(t.Context()))
					middleware, status := multiAuth.GetProviderMiddleware("prompt-provider")
					require.Equal(t, int32(200), status.Code)
					if previous != nil {
						require.NotSame(t, previous, middleware, "prompt-only edits must refresh middleware")
					}
					config := mustGetAuthConfig(t, multiAuth)
					require.NotNil(t, config.Providers)
					require.Len(t, *config.Providers, 1)
					public := (*config.Providers)[0]
					if providerType == "oidc" {
						spec, err := public.Spec.AsOIDCProviderSpec()
						require.NoError(t, err)
						require.Equal(t, lo.ToPtr(tt.want), spec.Prompt)
						require.Equal(t, tt.prompt, middleware.(*OIDCAuth).GetOIDCSpec().Prompt)
					} else {
						spec, err := public.Spec.AsOAuth2ProviderSpec()
						require.NoError(t, err)
						require.Equal(t, lo.ToPtr(tt.want), spec.Prompt)
						require.Equal(t, tt.prompt, middleware.(*OAuth2Auth).GetOAuth2Spec().Prompt)
					}
					require.NoError(t, multiAuth.LoadAllAuthProviders(t.Context()))
					unchanged, status := multiAuth.GetProviderMiddleware("prompt-provider")
					require.Equal(t, int32(200), status.Code)
					require.Same(t, middleware, unchanged, "unchanged prompt must retain middleware")
					previous = middleware
				})
			}
		})
	}
}

func TestMultiAuth_GetAuthConfigPropagatesProviderError(t *testing.T) {
	oauth2Auth := createTestOAuth2Auth(t, createBasicOAuth2Spec(t))
	defer oauth2Auth.Stop()

	var invalidRoleAssignment api.AuthRoleAssignment
	require.NoError(t, invalidRoleAssignment.UnmarshalJSON([]byte("{")))
	oauth2Auth.spec.RoleAssignment = invalidRoleAssignment

	multiAuth := NewMultiAuth(nil, nil, logrus.New())
	multiAuth.AddStaticProvider("https://test-issuer.example.com", oauth2Auth)

	config, err := multiAuth.GetAuthConfig()
	require.Error(t, err)
	require.Nil(t, config)
}
