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
	// LabelKey is the key name for label specifications in quadlet unit sections.
	LabelKey = "Label"
	// VolumeKey is the key name for volume references in quadlet unit sections.
	VolumeKey = "Volume"
	// EnvironmentFileKey is the key name for environment file references in quadlet unit sections.
	EnvironmentFileKey = "EnvironmentFile"
	// NetworkKey is the key name for network references in quadlet unit sections.
	NetworkKey = "Network"
	// PodKey is the key name for pod references in quadlet unit sections.
	PodKey = "Pod"
	// ContainerNameKey is the key name for specifying a custom container name in the [Container] section.
	ContainerNameKey = "ContainerName"
	// VolumeNameKey is the key name for specifying a custom volume name in the [Volume] section.
	VolumeNameKey = "VolumeName"
	// PodNameKey is the key name for specifying a custom pod name in the [Pod] section.
	PodNameKey = "PodName"
	// NetworkNameKey is the key name for specifying a custom network name in the [Network] section.
	NetworkNameKey = "NetworkName"
	// PodmanArgsKey is the key name for specifying arbitrary arguments to podman
	PodmanArgsKey = "PodmanArgs"
	// ServiceNameKey is the key name for overriding the default service name
	ServiceNameKey = "ServiceName"
	// PublishPortKey is the key name for exposing ports from a container
	PublishPortKey = "PublishPort"
	// DriverKey is the key name for specifying a Volume driver
	DriverKey = "Driver"
)

// Sections maps quadlet section names to their corresponding file extensions.
var Sections = map[string]string{
	ContainerGroup: ContainerExtension,
	VolumeGroup:    VolumeExtension,
	ImageGroup:     ImageExtension,
	PodGroup:       PodExtension,
	BuildGroup:     BuildExtension,
	ArtifactGroup:  ArtifactExtension,
	KubeGroup:      KubeExtension,
	NetworkGroup:   NetworkExtension,
}

// Extensions maps quadlet file extensions to their corresponding section names.
var Extensions = map[string]string{
	ContainerExtension: ContainerGroup,
	VolumeExtension:    VolumeGroup,
	NetworkExtension:   NetworkGroup,
	PodExtension:       PodGroup,
	ImageExtension:     ImageGroup,
	KubeExtension:      KubeGroup,
	ArtifactExtension:  ArtifactGroup,
	BuildExtension:     BuildGroup,
}

// templateParts parses systemd template unit naming conventions from the given filename.
// It returns the unit name prefix, instance name, and a boolean indicating whether the filename
// represents a template unit. For example, "foo@bar.container" returns ("foo", "bar", true),
// while "foo.container" returns ("foo", "", false).
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

// MountParts parses a CSV-formatted mount specification into individual parts.
// Mount specifications are comma-separated key=value pairs (e.g., "type=image,source=myapp.image,destination=/data").
// Returns an error if the mount string is not valid CSV format or does not contain exactly one record.
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

func mountValue(mount string, mountType string) (string, error) {
	t, err := MountType(mount)
	if err != nil {
		return "", err
	}
	if t != mountType {
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

// MountImage parses the Image from a mount if it exists
func MountImage(mount string) (string, error) {
	return mountValue(mount, "image")
}

// MountVolume parses the Volume from a mount if it exists
func MountVolume(mount string) (string, error) {
	return mountValue(mount, "volume")
}

// IsImageReference returns true if the given string ends with the image quadlet extension.
func IsImageReference(image string) bool {
	return IsQuadletReference(image, ImageExtension)
}

// IsBuildReference returns true if the given string ends with the build quadlet extension.
func IsBuildReference(ref string) bool {
	return IsQuadletReference(ref, BuildExtension)
}

// IsVolumeReference returns true if the given string ends with the volume quadlet extension.
func IsVolumeReference(ref string) bool {
	return IsQuadletReference(ref, VolumeExtension)
}

// IsQuadletReference returns true if the given reference matches the expected quadlet extension
func IsQuadletReference(ref, expected string) bool {
	return strings.HasSuffix(ref, expected)
}

// IsQuadletFile returns true if the given file path has a recognized quadlet extension.
func IsQuadletFile(quadlet string) bool {
	_, ok := Extensions[filepath.Ext(quadlet)]
	return ok
}

// VolumeName returns the volume name to use for a quadlet volume file.
// If volumeName is provided (non-nil), it returns the custom name.
// Otherwise, it generates a default name in the format "systemd-<basename>"
// where basename is the filename without its extension.
// For example, "data.volume" becomes "systemd-data".
func VolumeName(volumeName *string, filename string) string {
	if volumeName != nil {
		return *volumeName
	}

	return fmt.Sprintf("systemd-%s", strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
}

// NamespaceResource namespaces the supplied quadlet resource
func NamespaceResource(id string, resource string) string {
	return fmt.Sprintf("%s-%s", id, resource)
}

// IsWorkload returns true if a quadlet file is considered to be a workload
func IsWorkload(quadlet string) bool {
	return filepath.Ext(quadlet) == ContainerExtension
}
