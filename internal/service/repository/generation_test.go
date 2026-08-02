package repository

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestSetGenerationOnCreate(t *testing.T) {
	meta := domain.ObjectMeta{Generation: lo.ToPtr(int64(99))}
	setGenerationOnCreate(&meta)
	require.Equal(t, int64(1), lo.FromPtr(meta.Generation))
}

func TestSetGenerationOnUpdate(t *testing.T) {
	t.Run("When the spec is unchanged it should keep the previous generation", func(t *testing.T) {
		existing := &domain.Repository{
			Metadata: domain.ObjectMeta{Generation: lo.ToPtr(int64(3))},
		}
		require.NoError(t, existing.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/1.git", Type: domain.GitRepoSpecTypeGit}))
		next := &domain.Repository{}
		require.NoError(t, next.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/1.git", Type: domain.GitRepoSpecTypeGit}))
		setGenerationOnUpdate(existing, next)
		require.Equal(t, int64(3), lo.FromPtr(next.Metadata.Generation))
	})

	t.Run("When the spec changes it should bump the generation", func(t *testing.T) {
		existing := &domain.Repository{
			Metadata: domain.ObjectMeta{Generation: lo.ToPtr(int64(3))},
		}
		require.NoError(t, existing.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/1.git", Type: domain.GitRepoSpecTypeGit}))
		next := &domain.Repository{}
		require.NoError(t, next.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/2.git", Type: domain.GitRepoSpecTypeGit}))
		setGenerationOnUpdate(existing, next)
		require.Equal(t, int64(4), lo.FromPtr(next.Metadata.Generation))
	})

	t.Run("When existing generation is nil and the spec changes it should set generation to 1", func(t *testing.T) {
		existing := &domain.Repository{}
		require.NoError(t, existing.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/1.git", Type: domain.GitRepoSpecTypeGit}))
		next := &domain.Repository{}
		require.NoError(t, next.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/2.git", Type: domain.GitRepoSpecTypeGit}))
		setGenerationOnUpdate(existing, next)
		require.Equal(t, int64(1), lo.FromPtr(next.Metadata.Generation))
	})

	t.Run("When existing generation is nil and the spec is unchanged it should leave generation nil", func(t *testing.T) {
		existing := &domain.Repository{}
		require.NoError(t, existing.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/1.git", Type: domain.GitRepoSpecTypeGit}))
		next := &domain.Repository{}
		require.NoError(t, next.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "https://example.com/1.git", Type: domain.GitRepoSpecTypeGit}))
		setGenerationOnUpdate(existing, next)
		require.Nil(t, next.Metadata.Generation)
	})
}
