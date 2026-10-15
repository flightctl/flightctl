package lifecycle

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/opencontainers/go-digest"
)

func recordArtifactVolumeImage(ctx context.Context, logger *log.PrefixLogger, deltaStore *deltastatus.Store, action Action, volume Volume, podman *client.Podman, artifactDigest string) {
	if deltaStore == nil || action.Name == "" || action.Lifetime == "" {
		return
	}
	inspection, err := podman.InspectVolume(ctx, volume.ID)
	if err != nil || inspection == nil || inspection.Driver != podmanLocalVolumeDriver || inspection.CreatedAt == "" {
		logger.Warnf("Unable to bind artifact identity to volume %s: inspect failed or volume is not a created local volume: %v", volume.ID, err)
		return
	}
	image := deltastatus.ApplicationImage{
		Image:   volume.Reference,
		Owner:   string(action.User.WithDefault(v1beta1.RootUsername)),
		OCIType: volume.OCIType,
		Digest:  artifactDigest,
	}
	if err := deltaStore.RecordApplicationVolumeImage(action.Name, action.Lifetime, volume.ID, inspection.CreatedAt, image); err != nil {
		logger.Warnf("Unable to persist artifact identity for volume %s: %v", volume.ID, err)
	}
}

// artifactVolumePreparer owns the pin, invalidate, prepare, extract, and bind
// operation. Quadlet preserves verified volumes across lifecycle restarts.
type artifactVolumePreparer struct {
	log              *log.PrefixLogger
	deltaStore       *deltastatus.Store
	preserveExisting bool
}

func (q *artifactVolumePreparer) ensureArtifactVolume(
	ctx context.Context,
	action Action,
	volume Volume,
	podman *client.Podman,
	rw fileio.Writer,
	labels []string,
	canRecordIdentity bool,
) (bool, error) {
	immutableReference, artifactDigest, digestErr := q.artifactVolumeReference(ctx, volume, podman, canRecordIdentity)
	volumePath, created, preserved, err := q.prepareArtifactVolumePath(ctx, action, volume, podman, rw, labels, artifactDigest)
	if err != nil {
		return created, err
	}
	if preserved {
		return false, nil
	}
	if _, err := podman.ExtractArtifact(ctx, immutableReference, volumePath); err != nil {
		return created, fmt.Errorf("extracting artifact to volume %w: %w", errors.WithElement(volume.ID), err)
	}
	if canRecordIdentity && digestErr == nil && artifactDigest != "" {
		recordArtifactVolumeImage(ctx, q.log, q.deltaStore, action, volume, podman, artifactDigest)
	}
	q.log.Infof("Creating artifact volume %q from artifact %q", volume.ID, volume.Reference)
	return created, nil
}

func (q *artifactVolumePreparer) artifactVolumeReference(
	ctx context.Context,
	volume Volume,
	podman *client.Podman,
	canRecordIdentity bool,
) (string, string, error) {
	if !canRecordIdentity {
		return volume.Reference, "", nil
	}
	reference, digestValue, err := podman.ImmutableArtifactReference(ctx, volume.Reference)
	if err != nil {
		q.log.Warnf("Unable to pin artifact identity for volume %s; volume lastDelta evidence will be withheld: %v", volume.ID, err)
	}
	return reference, digestValue, err
}

func (q *artifactVolumePreparer) prepareArtifactVolumePath(
	ctx context.Context,
	action Action,
	volume Volume,
	podman *client.Podman,
	rw fileio.Writer,
	labels []string,
	artifactDigest string,
) (string, bool, bool, error) {
	if !podman.VolumeExists(ctx, volume.ID) {
		q.log.Tracef("Creating volume %q", volume.ID)
		if err := q.clearArtifactVolumeImage(action, volume.ID); err != nil {
			return "", false, false, err
		}
		volumePath, err := podman.CreateVolume(ctx, volume.ID, labels)
		return volumePath, err == nil, false, volumeCreateError(volume.ID, err)
	}

	q.log.Tracef("Volume %q already exists, updating contents", volume.ID)
	inspection, err := podman.InspectVolume(ctx, volume.ID)
	if err != nil {
		return "", false, false, fmt.Errorf("inspect volume identity %w: %w", errors.WithElement(volume.ID), err)
	}
	if inspection == nil {
		return "", false, false, fmt.Errorf("inspect volume identity %w: empty inspection", errors.WithElement(volume.ID))
	}
	if inspection.Driver != podmanLocalVolumeDriver {
		return "", false, false, fmt.Errorf("volume %w has driver %q, expected local artifact volume", errors.WithElement(volume.ID), inspection.Driver)
	}
	volumePath, err := podman.InspectVolumeMount(ctx, volume.ID)
	if err != nil {
		return "", false, false, fmt.Errorf("inspect volume %w: %w", errors.WithElement(volume.ID), err)
	}
	if q.preserveExisting && q.deltaStore != nil && action.Name != "" && action.Lifetime != "" {
		binding, found := q.deltaStore.ApplicationVolumeBinding(action.Name, action.Lifetime, volume.ID)
		_, validArtifactDigest := digest.Parse(artifactDigest)
		if found && binding.CreatedAt == inspection.CreatedAt && binding.CreatedAt != "" &&
			binding.Image.Image == volume.Reference && binding.Image.Owner == string(action.User.WithDefault(v1beta1.RootUsername)) &&
			binding.Image.OCIType == volume.OCIType && validArtifactDigest == nil &&
			binding.Image.Digest == artifactDigest {
			return volumePath, false, true, nil
		}
	}
	if err := q.clearArtifactVolumeImage(action, volume.ID); err != nil {
		return "", false, false, err
	}
	if err := rw.RemoveContents(volumePath); err != nil {
		return "", false, false, fmt.Errorf("removing volume content %w: %w", errors.WithElement(volumePath), err)
	}
	return volumePath, false, false, nil
}

func volumeCreateError(volumeID string, err error) error {
	if err != nil {
		return fmt.Errorf("creating volume %w: %w", errors.WithElement(volumeID), err)
	}
	return nil
}

func (q *artifactVolumePreparer) clearArtifactVolumeImage(action Action, volumeID string) error {
	if q.deltaStore == nil {
		return nil
	}
	if err := q.deltaStore.ClearApplicationVolumeImage(action.Name, action.Lifetime, volumeID); err != nil {
		return fmt.Errorf("clear old volume identity %w: %w", errors.WithElement(volumeID), err)
	}
	return nil
}
