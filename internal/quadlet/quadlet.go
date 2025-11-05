package quadlet

import (
	"encoding/csv"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// ContainerExtension is the file extension for container quadlet files.
	ContainerExtension = ".container"
	// VolumeExtension is the file extension for volume quadlet files.
	VolumeExtension = ".volume"
	// NetworkExtension is the file extension for network quadlet files.
	NetworkExtension = ".network"
	// ImageExtension is the file extension for image quadlet files.
	ImageExtension = ".image"
	// PodExtension is the file extension for pod quadlet files.
	PodExtension = ".pod"
	// BuildExtension is the file extension for build quadlet files.
	BuildExtension = ".build"
	// ArtifactExtension is the file extension for artifact quadlet files.
	ArtifactExtension = ".artifact"
	// KubeExtension is the file extension for kube quadlet files.
	KubeExtension = ".kube"

	// ContainerGroup is the section name for container specifications in quadlet files.
	ContainerGroup = "Container"
	// VolumeGroup is the section name for volume specifications in quadlet files.
	VolumeGroup = "Volume"
	// NetworkGroup is the section name for network specifications in quadlet files.
	NetworkGroup = "Network"
	// ImageGroup is the section name for image specifications in quadlet files.
	ImageGroup = "Image"
	// PodGroup is the section name for pod specifications in quadlet files.
	PodGroup = "Pod"
	// BuildGroup is the section name for build specifications in quadlet files.
	BuildGroup = "Build"
	// ArtifactGroup is the section name for artifact specifications in quadlet files.
	ArtifactGroup = "Artifact"
	// KubeGroup is the section name for kube specifications in quadlet files.
	KubeGroup = "Kube"

	// ImageKey is the key name for image references in quadlet unit sections.
	ImageKey = "Image"
	// MountKey is the key name for mount specifications in quadlet unit sections.
	MountKey = "Mount"
)

// see https://github.com/containers/podman/blob/main/pkg/systemd/parser/unitfile.go#L942
func templateParts(filename string) (string, string, bool) {
	ext := filepath.Ext(filename)
	basename := strings.TrimSuffix(filename, ext)
	parts := strings.SplitN(basename, "@", 2)
	if len(parts) < 2 {
		return parts[0], "", false
	}
	return parts[0], parts[1], true
}

// DropinDirectories returns the paths in which to search for drop-in conf files for the specified file
// in order of most specific path to least.
// see https://github.com/containers/podman/blob/main/pkg/systemd/parser/unitfile.go#L952
func DropinDirectories(quadlet string) []string {
	unitName, instanceName, isTemplate := templateParts(quadlet)

	ext := filepath.Ext(quadlet)
	dropinExt := ext + ".d"

	var dropinPaths []string

	topLevelDropIn := strings.TrimPrefix(dropinExt, ".")
	dropinPaths = append(dropinPaths, topLevelDropIn)

	truncatedParts := strings.Split(unitName, "-")
	if len(truncatedParts) > 1 {
		truncatedParts = truncatedParts[:len(truncatedParts)-1]
		for i := range truncatedParts {
			truncatedUnitPath := strings.Join(truncatedParts[:i+1], "-") + "-"
			dropinPaths = append(dropinPaths, truncatedUnitPath+dropinExt)
			if isTemplate {
				truncatedTemplatePath := truncatedUnitPath + "@"
				dropinPaths = append(dropinPaths, truncatedTemplatePath+dropinExt)
			}
		}
	}
	if instanceName != "" {
		dropinPaths = append(dropinPaths, unitName+"@"+dropinExt)
	}
	dropinPaths = append(dropinPaths, quadlet+".d")
	// reverse the list so that drop-ins are parsed in order of most specific to least
	slices.Reverse(dropinPaths)
	return dropinPaths
}

func MountParts(mount string) ([]string, error) {
	records, err := csv.NewReader(strings.NewReader(mount)).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, fmt.Errorf("invalid mount format")
	}
	return records[0], nil
}

// MountType returns the type (volume, image, .etc) associated with the mount
// see https://github.com/containers/podman/blob/main/pkg/specgenutilexternal/mount.go
func MountType(mount string) (string, error) {
	parts, err := MountParts(mount)
	if err != nil {
		return "", err
	}
	for _, s := range parts {
		kv := strings.Split(s, "=")
		if len(kv) == 2 && strings.TrimSpace(kv[0]) == "type" {
			return strings.TrimSpace(kv[1]), nil
		}
	}
	return "volume", nil
}

// MountImage parses the Image from a mount if it exists
func MountImage(mount string) (string, error) {
	mountType, err := MountType(mount)
	if err != nil {
		return "", err
	}
	if mountType != "image" {
		return "", nil
	}

	parts, err := MountParts(mount)
	if err != nil {
		return "", err
	}

	for _, part := range parts {
		key, image, hasVal := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		image = strings.TrimSpace(image)
		if key == "source" || key == "src" {
			if !hasVal {
				return "", fmt.Errorf("mount source invalid")
			}
			return image, nil
		}
	}
	return "", nil
}

// IsImageReference returns true if the given string ends with the image quadlet extension.
func IsImageReference(image string) bool {
	return strings.HasSuffix(image, ImageExtension)
}

// IsBuildReference returns true if the given string ends with the build quadlet extension.
func IsBuildReference(ref string) bool {
	return strings.HasSuffix(ref, BuildExtension)
}

var quadlets = map[string]struct{}{
	ContainerExtension: {},
	VolumeExtension:    {},
	NetworkExtension:   {},
	PodExtension:       {},
	ImageExtension:     {},
	KubeExtension:      {},
	ArtifactExtension:  {},
	BuildExtension:     {},
}

// IsQuadletFile returns true if the given file path has a recognized quadlet extension.
func IsQuadletFile(quadlet string) bool {
	_, ok := quadlets[filepath.Ext(quadlet)]
	return ok
}
