package validation

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
)

var quadletNameLabels = map[common.QuadletType]string{
	common.QuadletTypeContainer: quadlet.ContainerNameKey,
	common.QuadletTypeNetwork:   quadlet.NetworkNameKey,
	common.QuadletTypePod:       quadlet.PodNameKey,
	common.QuadletTypeVolume:    quadlet.VolumeNameKey,
}

func validateContainerImage(image string, path string, fleetTemplate bool) error {
	if quadlet.IsBuildReference(image) {
		return fmt.Errorf(".build quadlet types are unsupported: %s", image)
	}
	if !quadlet.IsImageReference(image) {
		return errors.Join(ValidateOCIReferenceStrict(&image, path, fleetTemplate)...)
	}
	return nil
}

// ValidateQuadletSpec verifies the QuadletSpec for common issues.
// When fleetTemplate is true, template expressions like {{ .metadata.labels.x }} are allowed in image references.
func ValidateQuadletSpec(spec *common.QuadletReferences, path string, fleetTemplate bool) []error {
	var errs []error

	typeToExtension := map[common.QuadletType]string{
		common.QuadletTypeContainer: quadlet.ContainerExtension,
		common.QuadletTypeVolume:    quadlet.VolumeExtension,
		common.QuadletTypeNetwork:   quadlet.NetworkExtension,
		common.QuadletTypeImage:     quadlet.ImageExtension,
		common.QuadletTypePod:       quadlet.PodExtension,
	}

	if expectedExt, ok := typeToExtension[spec.Type]; !ok {
		errs = append(errs, fmt.Errorf("invalid quadlet type: %s", spec.Type))
	} else {
		actualExt := filepath.Ext(path)
		if expectedExt != actualExt {
			errs = append(errs, fmt.Errorf("quadlet type %q does not match file extension %q (expected %q)", spec.Type, actualExt, expectedExt))
		}
	}

	switch spec.Type {
	case common.QuadletTypeContainer:
		if spec.Image == nil {
			errs = append(errs, fmt.Errorf(".container quadlet must have an Image key"))
		} else {
			if err := validateContainerImage(*spec.Image, "container.image", fleetTemplate); err != nil {
				errs = append(errs, err)
			}
		}

		for _, mountImage := range spec.MountImages {
			if err := validateContainerImage(mountImage, "container.mount.image", fleetTemplate); err != nil {
				errs = append(errs, err)
			}
		}

	case common.QuadletTypeVolume:
		if spec.Image != nil {
			image := *spec.Image
			if !quadlet.IsImageReference(image) {
				if err := ValidateOCIReferenceStrict(&image, "volume.image", fleetTemplate); err != nil {
					errs = append(errs, err...)
				}
			}
		}

	case common.QuadletTypeImage:
		if spec.Image == nil {
			errs = append(errs, fmt.Errorf(".image quadlet must have an Image key"))
		} else {
			if err := ValidateOCIReferenceStrict(spec.Image, "image.image", fleetTemplate); err != nil {
				errs = append(errs, err...)
			}
		}

	case common.QuadletTypeNetwork, common.QuadletTypePod:
		// no validation required

	default:
		errs = append(errs, fmt.Errorf("%w: %q", common.ErrUnsupportedQuadletType, spec.Type))
	}

	return errs
}

// ValidateQuadletCrossReferences validates that all quadlet file references within an application
// actually exist in the application's defined files. This ensures that quadlet files don't reference
// other quadlet files that aren't part of the same application (since applications are namespaced).
func ValidateQuadletCrossReferences(specs map[string]*common.QuadletReferences) []error {
	var errs []error

	for path, spec := range specs {
		if spec.Image != nil && quadlet.IsImageReference(*spec.Image) {
			if _, exists := specs[*spec.Image]; !exists {
				errs = append(errs, fmt.Errorf("quadlet file %q references %q which is not defined in the application", path, *spec.Image))
			}
		}

		for _, mountImage := range spec.MountImages {
			if quadlet.IsImageReference(mountImage) {
				if _, exists := specs[mountImage]; !exists {
					errs = append(errs, fmt.Errorf("quadlet file %q references %q which is not defined in the application", path, mountImage))
				}
			}
		}

		for _, volume := range spec.Volumes {
			if _, exists := specs[volume]; !exists {
				errs = append(errs, fmt.Errorf("quadlet file %q references %q which is not defined in the application", path, volume))
			}
		}

		for _, mountVolume := range spec.MountVolumes {
			if _, exists := specs[mountVolume]; !exists {
				errs = append(errs, fmt.Errorf("quadlet file %q references %q which is not defined in the application", path, mountVolume))
			}
		}

		for _, network := range spec.Networks {
			if _, exists := specs[network]; !exists {
				errs = append(errs, fmt.Errorf("quadlet file %q references %q which is not defined in the application", path, network))
			}
		}

		for _, pod := range spec.Pods {
			if _, exists := specs[pod]; !exists {
				errs = append(errs, fmt.Errorf("quadlet file %q references %q which is not defined in the application", path, pod))
			}
		}
	}

	return errs
}

// ValidateQuadletPaths validates a list of paths for inline quadlet applications
func ValidateQuadletPaths(paths []string) error {
	var errs []error

	if len(paths) == 0 {
		return fmt.Errorf("no paths provided")
	}

	foundSupported := false
	hasWorkloads := false

	for _, path := range paths {
		ext := filepath.Ext(path)

		if _, ok := common.SupportedQuadletExtensions[ext]; ok {
			if !isAtRoot(path) {
				errs = append(errs, fmt.Errorf("quadlet file must be at root level: %q", path))
			}
			foundSupported = true
			hasWorkloads = hasWorkloads || quadlet.IsWorkload(path)
			continue
		}

		if _, ok := common.UnsupportedQuadletExtensions[ext]; ok {
			errs = append(errs, fmt.Errorf("unsupported quadlet type %q in path: %s", ext, path))
			continue
		}
	}

	if !foundSupported {
		errs = append(errs, fmt.Errorf("no supported quadlet types supplied"))
	}

	if !hasWorkloads {
		errs = append(errs, fmt.Errorf("at least one quadlet workload must be supplied"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// ValidateQuadletNames ensures custom quadlet names are unique.
func ValidateQuadletNames(specs map[string]*common.QuadletReferences) []error {
	var errs []error
	seen := make(map[string]string)

	for path, spec := range specs {
		if spec == nil || spec.Name == nil {
			continue
		}

		label, ok := quadletNameLabels[spec.Type]
		if !ok {
			continue
		}

		name := strings.TrimSpace(*spec.Name)
		if name == "" {
			continue
		}

		key := fmt.Sprintf("%s:%s", label, name)
		if prevPath, exists := seen[key]; exists {
			errs = append(errs, fmt.Errorf("duplicate %s %q found in %s and %s", label, name, prevPath, path))
			continue
		}

		seen[key] = path
	}

	return errs
}
