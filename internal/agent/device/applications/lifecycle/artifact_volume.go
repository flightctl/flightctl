package lifecycle

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
)

func recordArtifactVolumeImage(ctx context.Context, logger *log.PrefixLogger, deltaStore *deltastatus.Store, action Action, volume Volume, podman *client.Podman, artifactDigest string) error {
	if deltaStore == nil || action.Name == "" || action.Lifetime == "" {
		return nil
	}
	inspection, err := podman.InspectVolume(ctx, volume.ID)
	if err != nil || inspection == nil || inspection.Driver != podmanLocalVolumeDriver || inspection.CreatedAt == "" {
		logger.Warnf("Unable to bind artifact identity to volume %s: inspect failed or volume is not a created local volume: %v", volume.ID, err)
		return nil
	}
	image := deltastatus.ApplicationImage{
		Image:   volume.Reference,
		Owner:   string(action.User.WithDefault(v1beta1.RootUsername)),
		OCIType: volume.OCIType,
		Digest:  artifactDigest,
	}
	if err := deltaStore.RecordApplicationVolumeImage(action.Name, action.Lifetime, volume.ID, inspection.CreatedAt, image); err != nil {
		return fmt.Errorf("persist artifact identity for volume %w: %w", errors.WithElement(volume.ID), err)
	}
	return nil
}
