package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/pkg/log"
	"sigs.k8s.io/yaml"
)

// ensurePodmanVolumes creates and populates each image-backed volume in Podman.
func ensurePodmanVolumes(
	ctx context.Context,
	log *log.PrefixLogger,
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
			if err := ensurePodmanVolume(ctx, log, writer, podman, volume.Name, &v, labels); err != nil {
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
	uniqueName := lifecycle.NewComposeID(name)

	if podman.VolumeExists(ctx, uniqueName) {
		log.Tracef("Volume %q already exists, updating contents", uniqueName)
		volumePath, err := podman.InspectVolumeMount(ctx, uniqueName)
		if err != nil {
			return fmt.Errorf("inspect volume %q: %w", uniqueName, err)
		}
		if err := writer.RemoveContents(volumePath); err != nil {
			return fmt.Errorf("removing volume content %q: %w", volumePath, err)
		}
		if _, err := podman.ExtractArtifact(ctx, imageRef, volumePath); err != nil {
			return fmt.Errorf("extract artifact: %w", err)
		}
		return nil
	}

	log.Infof("Creating volume %q from image %q", uniqueName, imageRef)

	volumePath, err := podman.CreateVolume(ctx, uniqueName, labels)
	if err != nil {
		return fmt.Errorf("creating volume %q: %w", uniqueName, err)
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

func patchRenamedVolumesInComposeSpec(
	log *log.PrefixLogger,
	original *common.ComposeSpec,
	volumes *[]v1alpha1.ApplicationVolume,
) (*common.ComposeSpec, map[string]string) {
	if volumes == nil {
		log.Debug("No volumes provided, skipping patch.")
		return original, nil
	}
	renameSet := make(map[string]struct{}, len(*volumes))
	for _, vol := range *volumes {
		renameSet[vol.Name] = struct{}{}
	}

	patchedServices := make(map[string]common.ComposeService, len(original.Services))
	for name, svc := range original.Services {
		newVolumes := append([]string(nil), svc.Volumes...)
		patchedServices[name] = common.ComposeService{
			Image:         svc.Image,
			ContainerName: svc.ContainerName,
			Volumes:       newVolumes,
		}
	}

	patchedVolumes := make(map[string]common.ComposeVolume, len(original.Volumes))
	for name, vol := range original.Volumes {
		patchedVolumes[name] = vol
	}

	// rename volumes
	renamed := make(map[string]string)
	newVolumes := make(map[string]common.ComposeVolume, len(patchedVolumes))
	for name, vol := range patchedVolumes {
		if _, ok := renameSet[name]; ok {
			newName := lifecycle.NewComposeID(name)
			renamed[name] = newName
			newVolumes[newName] = vol
			log.Infof("Renaming volume %q to %q", name, newName)
		} else {
			newVolumes[name] = vol
		}
	}

	// rewrite volume references in services
	for svcName, svc := range patchedServices {
		originalVolumes := svc.Volumes
		for i, vol := range svc.Volumes {
			parts := strings.SplitN(vol, ":", 2)
			if newName, ok := renamed[parts[0]]; ok {
				if len(parts) == 2 {
					svc.Volumes[i] = fmt.Sprintf("%s:%s", newName, parts[1])
				} else {
					svc.Volumes[i] = newName
				}
				log.Infof("Service %q volume reference %q updated to %q", svcName, vol, svc.Volumes[i])
			}
		}
		if !slices.Equal(originalVolumes, svc.Volumes) {
			log.Debugf("Service %q volume list changed: %v to %v", svcName, originalVolumes, svc.Volumes)
		}
		patchedServices[svcName] = svc
	}

	log.Debugf("Patch complete. Renamed volumes: %v", renamed)

	return &common.ComposeSpec{
		Services: patchedServices,
		Volumes:  newVolumes,
	}, renamed
}

func writeComposeOverrideDiff(
	log *log.PrefixLogger,
	dir string,
	original, patched *common.ComposeSpec,
	renamed map[string]string,
	writer fileio.Writer,
	overrideFilename string,
) error {
	override := &common.ComposeSpec{
		Services: map[string]common.ComposeService{},
		Volumes:  map[string]common.ComposeVolume{},
	}

	log.Debugf("Preparing to write override diff to %q", overrideFilename)

	// add renamed volumes
	for _, newName := range renamed {
		override.Volumes[newName] = patched.Volumes[newName]
		log.Infof("Override will include renamed volume: %q", newName)
	}

	for name, patchedSvc := range patched.Services {
		origSvc, found := original.Services[name]
		if !found || !slices.Equal(patchedSvc.Volumes, origSvc.Volumes) {
			override.Services[name] = patchedSvc
			log.Infof("Override will include service %q due to volume change or new service", name)
		}
	}

	if len(override.Services) == 0 && len(override.Volumes) == 0 {
		log.Debug("No changes detected; override file will not be written.")
		return nil
	}

	overrideBytes, err := yaml.Marshal(override)
	if err != nil {
		log.WithError(err).Error("Failed to marshal override spec")
		return fmt.Errorf("marshal override: %w", err)
	}

	path := filepath.Join(dir, overrideFilename)
	if err := writer.WriteFile(path, overrideBytes, fileio.DefaultFilePermissions); err != nil {
		log.WithError(err).Errorf("Failed to write override file to %s", path)
		return fmt.Errorf("write override file: %w", err)
	}

	log.Infof("Override file written to %q", path)
	return nil
}
