package helm

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type repositoryPageService struct {
	repositoryservice.Service
	pages []*domain.RepositoryList
	calls int
}

func (s *repositoryPageService) ListRepositories(context.Context, uuid.UUID, domain.ListRepositoriesParams) (*domain.RepositoryList, domain.Status) {
	if s.calls >= len(s.pages) {
		return &domain.RepositoryList{}, domain.Status{Code: http.StatusOK}
	}
	page := s.pages[s.calls]
	s.calls++
	return page, domain.Status{Code: http.StatusOK}
}

func TestResolveOCIRepositorySpec(t *testing.T) {
	t.Run("When repository results are paginated it should choose the most specific match", func(t *testing.T) {
		orgID := uuid.New()
		continueToken := "next"
		broad := repositoryForSpec(t, domain.OciRepoSpec{Registry: "registry.example"})
		namespace := repositoryForSpec(t, domain.OciRepoSpec{Registry: "registry.example", Namespace: lo.ToPtr("team")})
		exact := repositoryForSpec(t, domain.OciRepoSpec{Registry: "registry.example", Repository: lo.ToPtr("team/charts/app")})
		service := &repositoryPageService{pages: []*domain.RepositoryList{
			{Items: []domain.Repository{broad, namespace}, Metadata: domain.ListMeta{Continue: &continueToken}},
			{Items: []domain.Repository{exact}},
		}}

		got, err := ResolveOCIRepositorySpec(context.Background(), service, orgID, "oci://registry.example/team/charts/app:1.2.3", nil)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "team/charts/app", lo.FromPtr(got.Repository))
		require.Equal(t, 2, service.calls)
	})

	t.Run("When no resource matches it should use a fallback only for the same registry", func(t *testing.T) {
		fallback := &domain.OciRepoSpec{Registry: "registry.example"}

		got, err := ResolveOCIRepositorySpec(context.Background(), nil, uuid.New(), "oci://registry.example/team/chart:1.0", fallback)

		require.NoError(t, err)
		require.Equal(t, fallback, got)
		got, err = ResolveOCIRepositorySpec(context.Background(), nil, uuid.New(), "oci://another.example/team/chart:1.0", fallback)
		require.NoError(t, err)
		require.Nil(t, got)
	})

	t.Run("When equally specific resources disagree it should return an error", func(t *testing.T) {
		insecure := true
		service := &repositoryPageService{pages: []*domain.RepositoryList{{
			Items: []domain.Repository{
				repositoryForSpec(t, domain.OciRepoSpec{Registry: "registry.example", Namespace: lo.ToPtr("team")}),
				repositoryForSpec(t, domain.OciRepoSpec{Registry: "registry.example", Namespace: lo.ToPtr("team"), SkipServerVerification: &insecure}),
			},
		}}}

		_, err := ResolveOCIRepositorySpec(context.Background(), service, uuid.New(), "oci://registry.example/team/chart:1.0", nil)

		require.ErrorContains(t, err, "equally specific")
	})
}

func repositoryForSpec(t *testing.T, spec domain.OciRepoSpec) domain.Repository {
	t.Helper()
	var repo domain.Repository
	require.NoError(t, repo.Spec.FromOciRepoSpec(spec))
	return repo
}
