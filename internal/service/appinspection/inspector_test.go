package appinspection

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/appinspection"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"oras.land/oras-go/v2/registry"
)

func TestResolvePackageRepositoryAccess(t *testing.T) {
	const host = "quay.example.com"
	const path = "org/apps/package"
	for _, tc := range []struct {
		name      string
		first     []domain.OciRepoSpec
		second    []domain.OciRepoSpec
		selected  *domain.OciRepoSpec
		ambiguous bool
	}{
		{
			name: "multiple scopes match it should select the exact repository from the second page",
			first: []domain.OciRepoSpec{
				{Registry: host, Namespace: lo.ToPtr("org/apps")},
				{Registry: host, Namespace: lo.ToPtr("org/apps"), Scheme: lo.ToPtr(domain.OciRepoSchemeHttp)},
				{Registry: "another.example.com", Repository: lo.ToPtr(path)},
			},
			second:   []domain.OciRepoSpec{{Registry: host, Repository: lo.ToPtr(path)}},
			selected: &domain.OciRepoSpec{Registry: host, Repository: lo.ToPtr(path)},
		},
		{
			name:  "the namespace shares only a partial path component it should use anonymous access",
			first: []domain.OciRepoSpec{{Registry: host, Namespace: lo.ToPtr("org/app")}},
		},
		{
			name: "equally scoped results conflict it should refuse the lookup",
			first: []domain.OciRepoSpec{
				{Registry: host, Namespace: lo.ToPtr("org")},
				{Registry: host, Namespace: lo.ToPtr("org"), Scheme: lo.ToPtr(domain.OciRepoSchemeHttp)},
			},
			ambiguous: true,
		},
	} {
		t.Run("When "+tc.name, func(t *testing.T) {
			orgID := uuid.New()
			service := repository.NewMockService(gomock.NewController(t))
			page := repositoryAccessPage(t, tc.first)
			var token *string
			if tc.second != nil {
				token = lo.ToPtr("next-page")
				page.Metadata.Continue = token
			}
			first := service.EXPECT().ListRepositories(gomock.Any(), orgID, domain.ListRepositoriesParams{Limit: lo.ToPtr(int32(1000))}).
				Return(page, domain.Status{Code: http.StatusOK})
			if token != nil {
				service.EXPECT().ListRepositories(gomock.Any(), orgID, domain.ListRepositoriesParams{Limit: lo.ToPtr(int32(1000)), Continue: token}).
					After(first).Return(repositoryAccessPage(t, tc.second), domain.Status{Code: http.StatusOK})
			}
			inspector, err := NewInspector(service)
			require.NoError(t, err)
			spec, err := inspector.resolveAccess(t.Context(), orgID, registry.Reference{Registry: host, Repository: path, Reference: "latest"})
			if tc.ambiguous {
				require.Error(t, err)
				require.Nil(t, spec)
				return
			}
			require.NoError(t, err)
			if tc.selected == nil {
				require.Nil(t, spec)
			} else {
				require.Equal(t, tc.selected.Registry, spec.Registry)
				require.Equal(t, tc.selected.Repository, spec.Repository)
			}
		})
	}
}

func TestInspectPackageLookupFailureRetainsDeclaredReferences(t *testing.T) {
	var app domain.ApplicationProviderSpec
	const image = "quay.example.com/org/compose:v1"
	require.NoError(t, json.Unmarshal([]byte(`{"appType":"compose","image":"`+image+`"}`), &app))
	orgID := uuid.New()
	service := repository.NewMockService(gomock.NewController(t))
	service.EXPECT().ListRepositories(gomock.Any(), orgID, gomock.Any()).Return(nil, domain.Status{
		Code: http.StatusForbidden, Message: "sensitive access error",
	})
	inspector, err := NewInspector(service)
	require.NoError(t, err)
	result := inspector.InspectPackage(t.Context(), orgID, &app, nil)
	require.Equal(t, []appinspection.Diagnostic{appinspection.DiagnosticContentUnavailable}, result.Diagnostics)
	require.Equal(t, []appinspection.Reference{{Image: image, Role: appinspection.RolePackage, ConsumerKind: appinspection.ConsumerContainer}}, result.References)
	require.Empty(t, result.ParentDigest)
}

func repositoryAccessPage(t *testing.T, specs []domain.OciRepoSpec) *domain.RepositoryList {
	t.Helper()
	page := &domain.RepositoryList{}
	for _, spec := range specs {
		spec.Type = domain.OciRepoSpecTypeOci
		var item domain.Repository
		require.NoError(t, item.Spec.FromOciRepoSpec(spec))
		page.Items = append(page.Items, item)
	}
	return page
}
