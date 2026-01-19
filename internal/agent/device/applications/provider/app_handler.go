package provider

import (
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
)

func ensureImageVolumes(vols []v1beta1.ApplicationVolume) error {
	for _, vol := range vols {
		volType, err := vol.Type()
		if err != nil {
			return fmt.Errorf("volume type: %w", err)
		}
		if volType != v1beta1.ImageApplicationVolumeProviderType {
			return fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, volType)
		}
	}
	return nil
}
