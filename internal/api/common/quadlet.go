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

// QuadletReferences represents a parsed Quadlet file's external references.
type QuadletReferences struct {
	// Type defines The quadlet type
	Type QuadletType
	// Image defines the Image associated with the quadlet. This can be an OCI image or a reference to an Image quadlet
	Image *string
	// MountImages defines a list images associated with the quadlet through mechanisms such as mounts.
	// These can be OCI images or references to Image quadlets
	MountImages []string
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
			}
		}
	}

	return spec, nil
}
