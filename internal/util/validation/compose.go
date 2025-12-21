package validation

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/api/common"
)

var ErrHardCodedContainerName = errors.New("hardcoded container_name")

func isAtRoot(path string) bool {
	return filepath.Base(path) == path
}

func ValidateComposePaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if len(paths) > 2 {
		return errors.New("too many compose paths: only one base and one override allowed")
	}

	var base, override string
	for _, path := range paths {
		kind, err := getComposePathType(path)
		if err != nil {
			return err
		}
		switch kind {
		case BaseCompose:
			if base != "" {
				return fmt.Errorf("multiple compose paths provided: %q and %q", base, path)
			}
			base = path
		case OverrideCompose:
			if override != "" {
				return fmt.Errorf("multiple override compose paths provided: %q and %q", override, path)
			}
			override = path
		}
	}

	if len(paths) == 1 {
		if base == "" {
			return fmt.Errorf("override path %q cannot be used without a base path", paths[0])
		}
		return nil
	}

	if getComposePrefix(base) != getComposePrefix(override) {
		return fmt.Errorf("mismatched tool types: base is %q, override is %q", base, override)
	}

	return nil
}

// ValidateComposeSpec verifies the ComposeSpec for common issues.
func ValidateComposeSpec(spec *common.ComposeSpec, fleetTemplate bool) []error {
	services := spec.Services
	if len(services) == 0 {
		return []error{fmt.Errorf("compose spec has no services")}
	}

	var errs []error
	for name, service := range spec.Services {
		containerName := service.ContainerName
		if service.ContainerName != "" {
			errs = append(errs, fmt.Errorf("service %s has a %w %q which is not supported", name, ErrHardCodedContainerName, containerName))
		}
		image := service.Image
		if image == "" {
			errs = append(errs, fmt.Errorf("service %s is missing an image", name))
		}
		if err := ValidateOCIReferenceStrict(&image, "services."+name+".image", fleetTemplate); err != nil {
			errs = append(errs, err...)
		}
	}
	return errs
}

func getComposePrefix(f string) string {
	if strings.HasPrefix(f, "docker-") {
		return "docker"
	}
	if strings.HasPrefix(f, "podman-") {
		return "podman"
	}
	return ""
}

type ComposePathType int

const (
	InvalidCompose ComposePathType = iota
	BaseCompose
	OverrideCompose
)

func getComposePathType(path string) (ComposePathType, error) {
	if !isAtRoot(path) {
		return InvalidCompose, fmt.Errorf("compose file must be at root level: %q", path)
	}

	base := filepath.Base(path)

	if !strings.HasSuffix(base, ".yaml") && !strings.HasSuffix(base, ".yml") {
		return InvalidCompose, fmt.Errorf("compose path must have .yaml or .yml extension: %q", path)
	}

	if strings.Contains(base, ".override.") {
		if strings.HasPrefix(base, "docker-compose.") || strings.HasPrefix(base, "podman-compose.") {
			return OverrideCompose, nil
		}
		return InvalidCompose, fmt.Errorf("invalid override compose path: %q", path)
	}

	validBaseNames := []string{
		"docker-compose.yaml",
		"docker-compose.yml",
		"podman-compose.yaml",
		"podman-compose.yml",
	}

	for _, name := range validBaseNames {
		if base == name {
			return BaseCompose, nil
		}
	}

	return InvalidCompose, fmt.Errorf("invalid compose path: %q, supported paths: %v", path, strings.Join(validBaseNames, ", "))
}
