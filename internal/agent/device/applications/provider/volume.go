package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"sigs.k8s.io/yaml"
)

type Volume struct {
	// Name is the user defined name for the volume
	Name string
	// ID is a unique internal idenfier used to create the actual volume
	ID string
	// Reference is the reference used to populate the volume
	Reference string
	// Available is true if the volume has been created
	Available bool
	// ReclaimPolicy controls how the volume is handled when the application is removed
	ReclaimPolicy v1beta1.ApplicationVolumeReclaimPolicy
}

type volumeProvider func() ([]*Volume, error)

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
	Status(status *v1beta1.DeviceApplicationStatus)
	// UpdateStatus processes a Podman event and updates internal volume status as needed.
	UpdateStatus(event *client.PodmanEvent)
	// AddVolumes adds all specified volumes to the manager. An ID will be added if one does not exist
	AddVolumes([]*Volume)
}

// NewVolumeManager returns a new VolumeManager.
func NewVolumeManager(log *log.PrefixLogger, appName string, appType v1beta1.AppType, volumes *[]v1beta1.ApplicationVolume) (VolumeManager, error) {
	m := &volumeManager{
		volumes: make(map[string]*Volume),
		log:     log,
		appName: appName,
		appType: appType,
	}

	if volumes == nil {
		return m, nil
	}

	for _, v := range *volumes {
		volType, err := v.Type()
		if err != nil {
			return nil, fmt.Errorf("volume type: %w", err)
		}
		var image *v1beta1.ImageVolumeSource
		switch volType {
		case v1beta1.ImageApplicationVolumeProviderType:
			provider, err := v.AsImageVolumeProviderSpec()
			if err != nil {
				return nil, err
			}
			image = &provider.Image
		case v1beta1.ImageMountApplicationVolumeProviderType:
			provider, err := v.AsImageMountVolumeProviderSpec()
			if err != nil {
				return nil, err
			}
			image = &provider.Image
		case v1beta1.MountApplicationVolumeProviderType:
			// nothing to manage
			continue
		default:
			return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, volType)
		}
		volID := m.volumeID(v.Name)
		policy := v.GetReclaimPolicy()
		m.volumes[volID] = &Volume{
			Name:          v.Name,
			Reference:     image.Reference,
			ID:            volID,
			Available:     true, // TODO: event support is broken for volumes.  https://github.com/containers/podman/issues/26480
			ReclaimPolicy: policy,
		}
	}
	return m, nil
}

type volumeManager struct {
	// volumes map is keyed from volume ID
	volumes map[string]*Volume
	log     *log.PrefixLogger
	appName string
	appType v1beta1.AppType
}

func (m *volumeManager) volumeID(volName string) string {
	switch m.appType {
	case v1beta1.AppTypeQuadlet, v1beta1.AppTypeContainer:
		return quadlet.NamespaceResource(client.NewComposeID(m.appName), volName)
	default:
		return client.ComposeVolumeName(m.appName, volName)
	}
}

func (m *volumeManager) Get(id string) (*Volume, bool) {
	vol, ok := m.volumes[id]
	return vol, ok
}

func (m *volumeManager) Add(volume *Volume) {
	_, ok := m.volumes[volume.ID]
	if !ok {
		if volume.ReclaimPolicy == "" {
			volume.ReclaimPolicy = v1beta1.Retain
		}
		m.volumes[volume.ID] = volume
	}
}

func (m *volumeManager) Update(volume *Volume) bool {
	_, ok := m.volumes[volume.ID]
	if !ok {
		return false
	}
	if volume.ReclaimPolicy == "" {
		volume.ReclaimPolicy = v1beta1.Retain
	}
	m.volumes[volume.ID] = volume
	return true
}

func (m *volumeManager) Remove(id string) bool {
	_, ok := m.volumes[id]
	if !ok {
		return false
	}
	delete(m.volumes, id)
	return true
}

func (m *volumeManager) List() []*Volume {
	result := make([]*Volume, 0, len(m.volumes))
	for _, vol := range m.volumes {
		result = append(result, vol)
	}

	// ensure ordering
	sort.Slice(result, func(i, j int) bool {
		// sort by user defined name
		return result[i].Name < result[j].Name
	})

	return result
}

func (m *volumeManager) Status(status *v1beta1.DeviceApplicationStatus) {
	volumes := make([]v1beta1.ApplicationVolumeStatus, 0, len(m.volumes))
	for _, vol := range m.List() {
		if !vol.Available {
			// only report obsereved status
			continue
		}
		volumes = append(volumes, v1beta1.ApplicationVolumeStatus{
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

	// the id of the volume is the event name
	volID := event.Name
	vol, ok := m.volumes[volID]
	if !ok {
		m.log.Warnf("Observed untracked volume: %s", volID)
		return
	}

	// volumes are not purely observe-only, update state to reflect observation
	switch event.Status {
	case "create":
		vol.Available = true
	case "remove":
		vol.Available = false
	default:
		m.log.Tracef("Unhandeled volume event status %v", event.Status)
	}
}

func (m *volumeManager) AddVolumes(volumes []*Volume) {
	for _, volume := range volumes {
		vol := volume
		if vol.ID == "" {
			vol.ID = m.volumeID(volume.Name)
		}
		if vol.ReclaimPolicy == "" {
			vol.ReclaimPolicy = v1beta1.Retain
		}
		vol.Available = true // TODO: event support is broken for volumes.  https://github.com/containers/podman/issues/26480
		m.volumes[vol.ID] = vol
	}
}

// ensureDependenciesFromVolumes verifies all volume types are supported
// and checks that Podman ≥ 5.5 is used for image backed volumes.
func ensureDependenciesFromVolumes(ctx context.Context, podman *client.Podman, volumes *[]v1beta1.ApplicationVolume) error {
	if volumes == nil {
		return nil
	}

	for _, volume := range *volumes {
		vType, err := volume.Type()
		if err != nil {
			return err
		}

		switch vType {
		case v1beta1.ImageApplicationVolumeProviderType, v1beta1.ImageMountApplicationVolumeProviderType:
			version, err := podman.Version(ctx)
			if err != nil {
				return fmt.Errorf("checking podman version: %w", err)
			}
			if !version.GreaterOrEqual(5, 5) {
				return fmt.Errorf("image volume support requires podman >= 5.5, found %d.%d", version.Major, version.Minor)
			}
		case v1beta1.MountApplicationVolumeProviderType:
			// No dependencies for simple mounts
			continue
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
			ID:            vol.ID,
			Reference:     vol.Reference,
			ReclaimPolicy: vol.ReclaimPolicy,
		}
	}
	return out
}

func extractQuadletVolumes(appID string, quadlets map[string]*common.QuadletReferences) []*Volume {
	var volumes []*Volume
	for name, quad := range quadlets {
		// Only track volume's with images
		if quad.Type != common.QuadletTypeVolume || quad.Image == nil {
			continue
		}
		name = strings.TrimPrefix(name, fmt.Sprintf("%s-", appID))
		volumes = append(volumes, &Volume{
			Name:          name,
			ID:            quadlet.VolumeName(quad.Name, quadlet.NamespaceResource(appID, name)),
			Reference:     *quad.Image,
			ReclaimPolicy: v1beta1.Retain,
		})
	}
	return volumes
}

func extractQuadletVolumesFromSpec(appID string, contents []v1beta1.ApplicationContent) ([]*Volume, error) {
	quadlets, err := client.ParseQuadletReferencesFromSpec(contents)
	if err != nil {
		return nil, fmt.Errorf("parsing quadlet spec: %w", err)
	}
	return extractQuadletVolumes(appID, quadlets), nil
}

func extractQuadletVolumesFromDir(appID string, r fileio.Reader, path string) ([]*Volume, error) {
	quadlets, err := client.ParseQuadletReferencesFromDir(r, path)
	if err != nil {
		return nil, fmt.Errorf("parsing quadlet spec: %w", err)
	}
	return extractQuadletVolumes(appID, quadlets), nil
}
