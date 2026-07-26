package model

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestNewDeviceFromApiResourceNilAnnotations(t *testing.T) {
	t.Run("When Annotations is nil it should keep a nil map so the store can preserve existing annotations", func(t *testing.T) {
		device, err := NewDeviceFromApiResource(&domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec:     &domain.DeviceSpec{},
		})
		require.NoError(t, err)
		require.Nil(t, device.Annotations)
	})

	t.Run("When Annotations is set it should copy the provided map", func(t *testing.T) {
		device, err := NewDeviceFromApiResource(&domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr("dev1"),
				Annotations: &map[string]string{
					domain.DeviceAnnotationRenderedVersion: "3",
				},
			},
			Spec: &domain.DeviceSpec{},
		})
		require.NoError(t, err)
		require.Equal(t, "3", device.Annotations[domain.DeviceAnnotationRenderedVersion])
	})
}
