package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRegistryRenderRealComposition(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authenticated bool
		authored      bool
		ambiguous     bool
		conflict      bool
		disabled      bool
		wantError     string
	}{
		{name: "When credentials and trust match it should render the real composition", authenticated: true},
		{name: "When an anonymous repository supplies trust it should render its certificates"},
		{name: "When authentication is authored it should retain trust without selecting ambiguous credentials", authenticated: true, authored: true, ambiguous: true},
		{name: "When credentials are ambiguous it should reject rendering", authenticated: true, ambiguous: true, wantError: "ambiguous credentials"},
		{name: "When endpoint policies conflict it should reject rendering", conflict: true, wantError: "conflicting transport settings"},
		{name: "When authentication is authored it should still reject endpoint policy conflicts", authenticated: true, authored: true, conflict: true, wantError: "conflicting transport settings"},
		{name: "When device delivery is disabled it should contribute nothing", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := uuid.New()
			ca := registryTestCA(t)
			repoSpec := domain.OciRepoSpec{
				Type:             domain.OciRepoSpecTypeOci,
				Registry:         "quay.io",
				Namespace:        lo.ToPtr("team"),
				DevicePullAccess: lo.ToPtr(!tc.disabled),
				CaCrt:            lo.ToPtr(base64.StdEncoding.EncodeToString([]byte(ca))),
			}
			if tc.authenticated {
				auth := domain.OciAuth{}
				require.NoError(t, auth.FromDockerAuth(domain.DockerAuth{AuthType: domain.Docker, Username: "account", Password: "test-password"}))
				repoSpec.OciAuth = &auth
			}
			repo := domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("first")}}
			require.NoError(t, repo.Spec.FromOciRepoSpec(repoSpec))
			repositories := []domain.Repository{repo}
			if tc.ambiguous || tc.conflict {
				if tc.conflict {
					repoSpec.Scheme = lo.ToPtr(domain.OciRepoSchemeHttp)
				}
				other := domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("second")}}
				require.NoError(t, other.Spec.FromOciRepoSpec(repoSpec))
				repositories = append(repositories, other)
			}
			repos := repositoryservice.NewMockService(gomock.NewController(t))
			repos.EXPECT().ListRepositories(gomock.Any(), org, domain.ListRepositoriesParams{}).Return(&domain.RepositoryList{Items: repositories}, domain.StatusOK())
			logic := NewDeviceRenderLogic(logrus.New(), nil, repos, nil, nil, newTestKVStore(), &config.Config{})
			spec := registrySpec(t, `{"applications":[{"name":"app","appType":"container","runAs":"root","image":"quay.io/team/app:v1"}]}`)
			if tc.authored {
				var provider domain.ConfigProviderSpec
				require.NoError(t, provider.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{Inline: []api.FileSpec{{Path: "/root/.config/containers/auth.json", Content: "{}"}}}))
				spec.Config = &[]domain.ConfigProviderSpec{provider}
			}
			rendered, err := logic.RenderSpec(context.Background(), org, spec)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			if tc.disabled {
				require.Empty(t, rendered.ConfigResources)
				return
			}
			var resources []api.ConfigResource
			require.NoError(t, json.Unmarshal(rendered.ConfigResources, &resources))
			require.Len(t, resources, 1)
			resource, err := resources[0].AsRegistryConfigResource()
			require.NoError(t, err)
			require.Equal(t, "registry:quay.io", resource.Name)
			require.Equal(t, api.RegistrySchemeHTTPS, resource.Scheme)
			require.False(t, lo.FromPtr(resource.SkipServerVerification))
			require.Equal(t, repoSpec.CaCrt, resource.CaCrt)
			require.Len(t, resource.Access, 1)
			require.Equal(t, "team", lo.FromPtr(resource.Access[0].Scope))
			if tc.authenticated && !tc.authored {
				require.NotNil(t, resource.Access[0].Authentication)
				auth, err := resource.Access[0].Authentication.AsDockerAuth()
				require.NoError(t, err)
				require.Equal(t, "account", auth.Username)
			} else {
				require.Nil(t, resource.Access[0].Authentication)
			}
		})
	}
}
