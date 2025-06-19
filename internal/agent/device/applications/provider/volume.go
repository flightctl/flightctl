package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"sigs.k8s.io/yaml"
)

// ensurePodmanVolumes creates and populates each image-backed volume in Podman.
func ensurePodmanVolumes(
	ctx context.Context,
	log *log.PrefixLogger,
	appName string,
	writer fileio.Writer,
	podman *client.Podman,
	volumes *[]v1alpha1.ApplicationVolume,
	labels []string,
) error {
	if volumes == nil {
		return nil
	}

	// ensure the volume content is pulled and available
	for _, volume := range *volumes {
		vType, err := volume.Type()
		if err != nil {
			return fmt.Errorf("getting volume type: %w", err)
		}
		switch vType {
		case v1alpha1.ImageApplicationVolumeProviderType:
			v, err := volume.AsImageVolumeProviderSpec()
			if err != nil {
				return fmt.Errorf("getting image volume provider spec: %w", err)
			}
			uniqueVolumeName := lifecycle.ComposeVolumeName(appName, volume.Name)
			if err := ensurePodmanVolume(ctx, log, writer, podman, uniqueVolumeName, &v, labels); err != nil {
				return fmt.Errorf("pulling image volume: %w", err)
			}
		default:
			return fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, vType)
		}
	}
	return nil
}

// ensurePodmanVolume creates and populates a image-backed podman volume.
func ensurePodmanVolume(
	ctx context.Context,
	log *log.PrefixLogger,
	writer fileio.Writer,
	podman *client.Podman,
	name string,
	provider *v1alpha1.ImageVolumeProviderSpec,
	labels []string,
) error {
	imageRef := provider.Image.Reference

	if podman.VolumeExists(ctx, name) {
		log.Tracef("Volume %q already exists, updating contents", name)
		volumePath, err := podman.InspectVolumeMount(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect volume %q: %w", name, err)
		}
		if err := writer.RemoveContents(volumePath); err != nil {
			return fmt.Errorf("removing volume content %q: %w", volumePath, err)
		}
		if _, err := podman.ExtractArtifact(ctx, imageRef, volumePath); err != nil {
			return fmt.Errorf("extract artifact: %w", err)
		}
		return nil
	}

	log.Infof("Creating volume %q from image %q", name, imageRef)

	volumePath, err := podman.CreateVolume(ctx, name, labels)
	if err != nil {
		return fmt.Errorf("creating volume %q: %w", name, err)
	}
	if _, err := podman.ExtractArtifact(ctx, imageRef, volumePath); err != nil {
		return fmt.Errorf("copy image contents: %w", err)
	}

	return nil
}

// ensureVolumesContent ensures image content for each volume is present,
// pulling it based on the volume's pull policy.
func ensureVolumesContent(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, volumes *[]v1alpha1.ApplicationVolume) error {
	if volumes == nil {
		return nil
	}

	// ensure the volume content is pulled and available on disk
	for _, volume := range *volumes {
		vType, err := volume.Type()
		if err != nil {
			return fmt.Errorf("getting volume type: %w", err)
		}
		switch vType {
		case v1alpha1.ImageApplicationVolumeProviderType:
			v, err := volume.AsImageVolumeProviderSpec()
			if err != nil {
				return fmt.Errorf("getting image volume provider spec: %w", err)
			}
			pullPolicy := v1alpha1.PullIfNotPresent
			if v.Image.PullPolicy != nil {
				pullPolicy = *v.Image.PullPolicy
			}
			if err := ensureArtifactExists(ctx, log, podman, v.Image.Reference, pullPolicy); err != nil {
				return fmt.Errorf("pulling image volume: %w", err)
			}
		default:
			return fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, vType)
		}
	}
	return nil
}

// ensureArtifactExists checks if an artifact exists locally and pulls it if needed.
func ensureArtifactExists(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, artifact string, pullPolicy v1alpha1.ImagePullPolicy) error {
	if pullPolicy == v1alpha1.PullNever {
		log.Tracef("Pull policy is set to never, skipping artifact pull: %s", artifact)
		return nil
	}
	if podman.ImageExists(ctx, artifact) && pullPolicy != v1alpha1.PullAlways {
		log.Tracef("Artifact already exists in container storage: %s", artifact)
		return nil
	}

	_, err := podman.PullArtifact(ctx, artifact)
	if err != nil {
		log.Warnf("Failed to pull artifact %q: %v", artifact, err)
		return err
	}

	return nil
}

// ensureDependenciesFromVolumes verifies all volume types are supported
// and checks that Podman ≥ 5.5 is used for image backed volumes.
func ensureDependenciesFromVolumes(ctx context.Context, podman *client.Podman, volumes *[]v1alpha1.ApplicationVolume) error {
	if volumes == nil {
		return nil
	}

	for _, volume := range *volumes {
		vType, err := volume.Type()
		if err != nil {
			return err
		}

		switch vType {
		case v1alpha1.ImageApplicationVolumeProviderType:
			version, err := podman.Version(ctx)
			if err != nil {
				return fmt.Errorf("checking podman version: %w", err)
			}
			if !version.GreaterOrEqual(5, 5) {
				return fmt.Errorf("image volume support requires podman >= 5.5, found %d.%d", version.Major, version.Minor)
			}
		default:
			return fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, vType)
		}
	}
	return nil
}

// writeComposeOverride creates an override file that maps volumes to external names and perists to disk.
func writeComposeOverride(
	log *log.PrefixLogger,
	dir string,
	appName string,
	volumes *[]v1alpha1.ApplicationVolume,
	writer fileio.Writer,
	overrideFilename string,
) error {
	if volumes == nil || len(*volumes) == 0 {
		return nil
	}

	override := map[string]any{
		"volumes": map[string]any{},
	}

	volMap := override["volumes"].(map[string]any)
	for _, volume := range *volumes {
		uniqueName := lifecycle.ComposeVolumeName(appName, volume.Name)
		volMap[volume.Name] = map[string]any{
			"external": true,
			"name":     uniqueName,
		}
	}

	overrideBytes, err := yaml.Marshal(override)
	if err != nil {
		return fmt.Errorf("marshal override yaml: %w", err)
	}

	path := filepath.Join(dir, overrideFilename)
	if err := writer.WriteFile(path, overrideBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("write override file: %w", err)
	}

	log.Infof("Compose override file written: %s", path)
	return nil
}
