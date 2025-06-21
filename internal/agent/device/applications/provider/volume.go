package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"sigs.k8s.io/yaml"
)

type Volume struct {
	// Name is the user defined name for the volume
	Name string
	// ID is a unique idenfier used to create the actual volume
	ID string
	// Refence is a the reference used to populate the volume
	Reference string
	// Available is true if the volume has been created
	Available bool
}

type VolumeManager interface {
	// Get returns the Volume by name, if it exists.
	Get(name string) (*Volume, bool)
	// Add adds a new Volume to the manager.
	Add(volume *Volume)
	// Update updates an existing Volume. Returns true if the volume existed and was updated.
	Update(volume *Volume) bool
	// Remove deletes the Volume by name. Returns true if the volume existed and was removed.
	Remove(name string) bool
	// List returns all managed Volumes.
	List() []*Volume
	// Status populates the given DeviceApplicationStatus with volume status information.
	Status(status *v1alpha1.DeviceApplicationStatus)
	// UpdateStatus processes a Podman event and updates internal volume status as needed.
	UpdateStatus(event *client.PodmanEvent)
}

// NewVolumeManager returns a new VolumeManager.
func NewVolumeManager(log *log.PrefixLogger, appName string, volumes *[]v1alpha1.ApplicationVolume) (VolumeManager, error) {
	m := &volumeManager{
		volumes: make(map[string]*Volume),
	}

	if volumes == nil {
		return m, nil
	}

	for _, v := range *volumes {
		// TODO: image provider assumed
		provider, err := v.AsImageVolumeProviderSpec()
		if err != nil {
			return nil, err
		}
		m.volumes[v.Name] = &Volume{
			Name:      v.Name,
			Reference: provider.Image.Reference,
			ID:        client.ComposeVolumeName(appName, v.Name),
			Available: true, // event support is broken.  https://github.com/containers/podman/issues/26480
		}
	}
	return m, nil
}

type volumeManager struct {
	volumes map[string]*Volume
	log     *log.PrefixLogger
}

func (m *volumeManager) Get(name string) (*Volume, bool) {
	vol, ok := m.volumes[name]
	return vol, ok
}

func (m *volumeManager) Add(volume *Volume) {
	_, ok := m.volumes[volume.Name]
	if !ok {
		m.volumes[volume.Name] = volume
	}
}

func (m *volumeManager) Update(volume *Volume) bool {
	_, ok := m.volumes[volume.Name]
	if !ok {
		return false
	}
	m.volumes[volume.Name] = volume
	return true
}

func (m *volumeManager) Remove(name string) bool {
	_, ok := m.volumes[name]
	if !ok {
		return false
	}
	delete(m.volumes, name)
	return true
}

func (m *volumeManager) List() []*Volume {
	result := make([]*Volume, 0, len(m.volumes))
	for _, vol := range m.volumes {
		result = append(result, vol)
	}

	// ensure ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

func (m *volumeManager) Status(status *v1alpha1.DeviceApplicationStatus) {
	volumes := make([]v1alpha1.ApplicationVolumeStatus, 0, len(m.volumes))
	for _, vol := range m.List() { // using sorted list
		if !vol.Available {
			// only report obsereved status
			continue
		}
		volumes = append(volumes, v1alpha1.ApplicationVolumeStatus{
			Name:      vol.Name,
			Reference: vol.Reference,
		})
	}

	if len(volumes) == 0 {
		return
	}

	status.Volumes = &volumes
}

func (m *volumeManager) UpdateStatus(event *client.PodmanEvent) {
	if event.Type != "volume" {
		return
	}

	volName := event.Name
	vol, ok := m.volumes[volName]
	if !ok {
		m.log.Warnf("Observed untracked volume: %s", volName)
		return
	}

	// volumes are not completly observe only so just update that we have observed them
	switch event.Status {
	case "create":
		vol.Available = true
	case "remove":
		vol.Available = false
	default:
		m.log.Tracef("Unhandeled volume event status %v", event.Status)
	}
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
	volumeManager VolumeManager,
	writer fileio.Writer,
	overrideFilename string,
) error {
	volumes := volumeManager.List()
	if len(volumes) == 0 {
		return nil
	}

	override := map[string]any{
		"volumes": map[string]any{},
	}

	volMap := override["volumes"].(map[string]any)
	for _, volume := range volumes {
		volMap[volume.Name] = map[string]any{
			"external": true,
			"name":     volume.ID,
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

// ToLifecycleVolumes converts provider volumes to lifecycle volumes
func ToLifecycleVolumes(volumes []*Volume) []lifecycle.Volume {
	out := make([]lifecycle.Volume, len(volumes))
	for i, vol := range volumes {
		out[i] = lifecycle.Volume{
			ID:        vol.ID,
			Reference: vol.Reference,
		}
	}
	return out
}
