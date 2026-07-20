package service

import (
	"testing"

	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestSetGenerationOnCreate(t *testing.T) {
	t.Run("When called it should set generation to 1", func(t *testing.T) {
		meta := &domain.ObjectMeta{}
		setGenerationOnCreate(meta)
		require.NotNil(t, meta.Generation)
		require.Equal(t, int64(1), *meta.Generation)
	})

	t.Run("When a caller-supplied generation is already set it should overwrite it", func(t *testing.T) {
		meta := &domain.ObjectMeta{Generation: lo.ToPtr(int64(42))}
		setGenerationOnCreate(meta)
		require.Equal(t, int64(1), *meta.Generation)
	})
}

func TestIncrementGenerationOnSpecChange(t *testing.T) {
	t.Run("When spec changed it should increment by 1", func(t *testing.T) {
		result := incrementGenerationOnSpecChange(lo.ToPtr(int64(1)), true)
		require.Equal(t, int64(2), *result)
	})

	t.Run("When spec is unchanged it should leave generation as-is", func(t *testing.T) {
		result := incrementGenerationOnSpecChange(lo.ToPtr(int64(3)), false)
		require.Equal(t, int64(3), *result)
	})

	t.Run("When spec changed and existing generation is nil it should treat it as 0 and increment to 1", func(t *testing.T) {
		result := incrementGenerationOnSpecChange(nil, true)
		require.Equal(t, int64(1), *result)
	})

	t.Run("When spec is unchanged and existing generation is nil it should leave it as 0", func(t *testing.T) {
		result := incrementGenerationOnSpecChange(nil, false)
		require.Equal(t, int64(0), *result)
	})
}
