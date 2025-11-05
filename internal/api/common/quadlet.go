package common

import (
	"fmt"

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

// QuadletReferences represents a Quadlet unit file's parsed references.
type QuadletReferences struct {
	Type        QuadletType
	Image       *string
	MountImages []string
}

// ParseQuadletReferences parses unit file data into a QuadletReferences object
func ParseQuadletReferences(data []byte) (*QuadletReferences, error) {
	unit, err := quadlet.NewUnit(data)
	if err != nil {
		return nil, fmt.Errorf("parsing quadlet references: %w", err)
	}

	for group := range UnsupportedQuadletSections {
		if unit.HasSection(group) {
			return nil, fmt.Errorf("%w: type: %s", ErrUnsupportedQuadletType, group)
		}
	}

	typeSections := map[string]QuadletType{
		quadlet.ContainerGroup: QuadletTypeContainer,
		quadlet.VolumeGroup:    QuadletTypeVolume,
		quadlet.NetworkGroup:   QuadletTypeNetwork,
		quadlet.ImageGroup:     QuadletTypeImage,
		quadlet.PodGroup:       QuadletTypePod,
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
			}
		}
	}

	return spec, nil
}
