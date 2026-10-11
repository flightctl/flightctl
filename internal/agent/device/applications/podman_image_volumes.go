package applications

import (
	"context"

	"github.com/flightctl/flightctl/api/core/v1beta1"
)

// currentImageVolumeObservations augments container observations with volumes
// attached to those running containers, including inline Quadlet and VM mounts.
func (m *PodmanMonitor) currentImageVolumeObservations(ctx context.Context, results []AppStatusResult) {
	if m.clientFactory == nil {
		return
	}
	for i := range results {
		result := &results[i]
		var observed []v1beta1.ApplicationVolumeStatus
		for _, workload := range result.workloads {
			if workload.Status != StatusRunning && workload.Status != StatusUnhealthy {
				continue
			}
			if workload.ID == "" {
				result.ImagesComplete = false
				continue
			}
			podman, err := m.clientFactory(result.Status.RunAs)
			if err != nil {
				result.ImagesComplete = false
				m.log.Warnf("Cannot inspect volumes for application %s: %v", result.Status.Name, err)
				continue
			}
			volumes, err := podman.ContainerImageVolumes(ctx, workload.ID)
			if err != nil {
				result.ImagesComplete = false
				m.log.Warnf("Cannot resolve all runtime volumes for application %s: %v", result.Status.Name, err)
			}
			for _, volume := range volumes {
				if name, ok := result.volumeNames[volume.Name]; ok {
					volume.Name = name
				}
				observed = append(observed, volume)
			}
		}
		result.Status.Volumes = mergeRuntimeVolumes(result.Status.Volumes, observed)
		var images []v1beta1.ApplicationImageDigest
		if result.Status.ImageDigests != nil {
			images = append(images, (*result.Status.ImageDigests)...)
		}
		for _, volume := range observed {
			images = append(images, v1beta1.ApplicationImageDigest{Image: volume.Reference, Digest: volume.Digest})
		}
		images = normalizedImageDigests(images)
		if len(images) > 0 {
			result.Status.ImageDigests = &images
		}
	}
}
