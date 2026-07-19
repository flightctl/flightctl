package device

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
		existing := &domain.Device{
			Metadata: domain.ObjectMeta{Generation: lo.ToPtr(int64(3))},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img:1"}},
		}
		next := &domain.Device{
			Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img:1"}},
		}
		setGenerationOnUpdate(existing, next)
		require.Equal(t, int64(3), lo.FromPtr(next.Metadata.Generation))
	})

	t.Run("When the spec changes it should bump the generation", func(t *testing.T) {
		existing := &domain.Device{
			Metadata: domain.ObjectMeta{Generation: lo.ToPtr(int64(3))},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img:1"}},
		}
		next := &domain.Device{
			Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img:2"}},
		}
		setGenerationOnUpdate(existing, next)
		require.Equal(t, int64(4), lo.FromPtr(next.Metadata.Generation))
	})

	t.Run("When existing generation is nil it should treat it as zero", func(t *testing.T) {
		existing := &domain.Device{Spec: &domain.DeviceSpec{}}
		next := &domain.Device{Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img:1"}}}
		setGenerationOnUpdate(existing, next)
		require.Equal(t, int64(1), lo.FromPtr(next.Metadata.Generation))
	})
}
