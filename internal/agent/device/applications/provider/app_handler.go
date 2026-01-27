package provider

import (
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/userutil"
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

func targetPathForQuadlet(user v1beta1.Username, id string) string {
	namespacedName := quadlet.NamespaceResource(id, lifecycle.QuadletTargetName)
	if user.IsRootUser() {
		return filepath.Join(lifecycle.RootfulQuadletTargetPath, namespacedName)
	} else {
		// This lookup should be prevalidated.
		_, _, homeDir, _ := userutil.LookupUser(user)
		return filepath.Join(homeDir, ".config/systemd/user", namespacedName)
	}
}
