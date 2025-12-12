package common

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/quadlet"
)

// QuadletType represents the type of Quadlet unit file.
type QuadletType string

const (
	QuadletTypeContainer QuadletType = "container"
	QuadletTypeVolume    QuadletType = "volume"
	QuadletTypeNetwork   QuadletType = "network"
	QuadletTypeImage     QuadletType = "image"
	QuadletTypePod       QuadletType = "pod"
)

var (
	ErrUnsupportedQuadletType = fmt.Errorf("unsupported quadlet type")
	ErrNonQuadletType         = fmt.Errorf("non quadlet type")
)

var (
	SupportedQuadletExtensions = map[string]struct{}{
		quadlet.ContainerExtension: {},
		quadlet.VolumeExtension:    {},
		quadlet.NetworkExtension:   {},
		quadlet.ImageExtension:     {},
		quadlet.PodExtension:       {},
	}
	UnsupportedQuadletExtensions = map[string]struct{}{
		quadlet.BuildExtension:    {},
		quadlet.ArtifactExtension: {},
		quadlet.KubeExtension:     {},
	}
	UnsupportedQuadletSections = map[string]struct{}{
		quadlet.BuildGroup:    {},
		quadlet.ArtifactGroup: {},
		quadlet.KubeGroup:     {},
	}
)

// QuadletReferences represents a parsed Quadlet file's external references.
type QuadletReferences struct {
	// Type defines The quadlet type
	Type QuadletType
	// Image defines the Image associated with the quadlet. This can be an OCI image or a reference to an Image quadlet
	Image *string
	// MountImages defines a list images associated with the quadlet through mechanisms such as mounts.
	// These can be OCI images or references to Image quadlets
	MountImages []string
	// Volumes defines a list of .volume quadlet references
	Volumes []string
	// MountVolumes defines a list of .volume references from Mount= keys
	MountVolumes []string
	// Networks defines a list of .network quadlet references
	Networks []string
	// Pods defines a list of .pod quadlet references
	Pods []string
	// The Name of the quadlet if the default will be overwritten
	Name *string
}

// ParseQuadletReferences parses unit file data into a QuadletSpec
func ParseQuadletReferences(data []byte) (*QuadletReferences, error) {
	typeSections := map[string]QuadletType{
		quadlet.ContainerGroup: QuadletTypeContainer,
		quadlet.VolumeGroup:    QuadletTypeVolume,
		quadlet.NetworkGroup:   QuadletTypeNetwork,
		quadlet.ImageGroup:     QuadletTypeImage,
		quadlet.PodGroup:       QuadletTypePod,
	}

	unit, err := quadlet.NewUnit(data)
	if err != nil {
		return nil, err
	}

	for section := range UnsupportedQuadletSections {
		if unit.HasSection(section) {
			return nil, fmt.Errorf("%w: type: %s", ErrUnsupportedQuadletType, section)
		}
	}

	var detectedType QuadletType
	var detectedSection string
	foundCount := 0

	for sectionName, quadletType := range typeSections {
		if unit.HasSection(sectionName) {
			detectedType = quadletType
			detectedSection = sectionName
			foundCount++
		}
	}
	if foundCount == 0 {
		return nil, ErrNonQuadletType
	}

	if foundCount > 1 {
		return nil, fmt.Errorf("multiple quadlet type sections found")
	}

	spec := &QuadletReferences{
		Type: detectedType,
	}

	image, err := unit.Lookup(detectedSection, quadlet.ImageKey)
	if err != nil {
		if !errors.Is(err, quadlet.ErrKeyNotFound) {
			return nil, fmt.Errorf("finding image key: %w", err)
		}
	} else {
		spec.Image = &image
	}

	mounts, err := unit.LookupAll(detectedSection, quadlet.MountKey)
	if err != nil {
		if !errors.Is(err, quadlet.ErrKeyNotFound) {
			return nil, fmt.Errorf("finding mount key: %w", err)
		}
	} else {
		for _, mount := range mounts {
			mountImage, err := quadlet.MountImage(mount)
			if err != nil {
				return nil, fmt.Errorf("parsing mount image: %w", err)
			}
			if mountImage != "" {
				spec.MountImages = append(spec.MountImages, mountImage)
				continue
			}

			mountVolume, err := quadlet.MountVolume(mount)
			if err != nil {
				return nil, fmt.Errorf("parsing mount volume: %w", err)
			}
			if quadlet.IsVolumeReference(mountVolume) {
				spec.MountVolumes = append(spec.MountVolumes, mountVolume)
			}
		}
	}

	spec.Volumes, err = lookupQuadletReferences(unit, detectedSection, quadlet.VolumeKey, quadlet.VolumeExtension)
	if err != nil {
		return nil, err
	}

	spec.Networks, err = lookupQuadletReferences(unit, detectedSection, quadlet.NetworkKey, quadlet.NetworkExtension)
	if err != nil {
		return nil, err
	}

	spec.Pods, err = lookupQuadletReferences(unit, detectedSection, quadlet.PodKey, quadlet.PodExtension)
	if err != nil {
		return nil, err
	}

	nameKeys := map[QuadletType]struct {
		key      string
		errLabel string
	}{
		QuadletTypeContainer: {key: quadlet.ContainerNameKey, errLabel: "container name"},
		QuadletTypeVolume:    {key: quadlet.VolumeNameKey, errLabel: "volume name"},
		QuadletTypeNetwork:   {key: quadlet.NetworkNameKey, errLabel: "network name"},
		QuadletTypePod:       {key: quadlet.PodNameKey, errLabel: "pod name"},
	}

	if nameKey, ok := nameKeys[spec.Type]; ok {
		name, err := unit.Lookup(detectedSection, nameKey.key)
		if err != nil {
			if !errors.Is(err, quadlet.ErrKeyNotFound) {
				return nil, fmt.Errorf("finding %s: %w", nameKey.errLabel, err)
			}
		} else {
			spec.Name = &name
		}
	}

	return spec, nil
}

func lookupQuadletReferences(unit *quadlet.Unit, section string, key string, extension string) ([]string, error) {
	values, err := unit.LookupAll(section, key)
	if err != nil {
		if !errors.Is(err, quadlet.ErrKeyNotFound) {
			return nil, fmt.Errorf("finding %q key: %w", key, err)
		}
		return nil, nil
	}

	var refs []string
	for _, value := range values {
		parts := strings.Split(value, ":")
		if quadlet.IsQuadletReference(parts[0], extension) {
			refs = append(refs, parts[0])
		}
	}
	return refs, nil
}
