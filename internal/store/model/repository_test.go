package model

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestRepositoryToApiResource(t *testing.T) {
	t.Run("When the stored repository has a generation it should report it", func(t *testing.T) {
		repository := &Repository{Resource: Resource{Name: "repo", Generation: lo.ToPtr(int64(3)), ResourceVersion: lo.ToPtr(int64(7))}}

		resource, err := repository.ToApiResource()

		require.NoError(t, err)
		require.Equal(t, int64(3), lo.FromPtr(resource.Metadata.Generation))
		require.Equal(t, "7", lo.FromPtr(resource.Metadata.ResourceVersion))
	})

	t.Run("When the stored repository has no generation it should report none", func(t *testing.T) {
		resource, err := (&Repository{Resource: Resource{Name: "repo"}}).ToApiResource()

		require.NoError(t, err)
		require.Nil(t, resource.Metadata.Generation)
	})
}
