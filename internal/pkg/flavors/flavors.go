package flavors

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	DefaultFlavorsFile = "hack/flavors.yaml"
)

// LoadFlavors loads and processes flavor configurations from the YAML file
// If overrideFile is specified, it will be merged with the base flavors file
func LoadFlavors(flavorsFile, overrideFile string) (map[string]*FlavorConfig, error) {
	if flavorsFile == "" {
		flavorsFile = DefaultFlavorsFile
	}

	// Load base flavors
	data, err := os.ReadFile(flavorsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read flavors file %s: %w", flavorsFile, err)
	}

	rawFlavors := make(FlavorsMap)
	if err := yaml.Unmarshal(data, &rawFlavors); err != nil {
		return nil, fmt.Errorf("failed to parse flavors YAML: %w", err)
	}

	// Load override flavors if specified
	if overrideFile != "" {
		overrideData, err := os.ReadFile(overrideFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read override file %s: %w", overrideFile, err)
		}

		overrideFlavors := make(FlavorsMap)
		if err := yaml.Unmarshal(overrideData, &overrideFlavors); err != nil {
			return nil, fmt.Errorf("failed to parse override YAML: %w", err)
		}

		// Merge override flavors into base flavors (override takes precedence)
		for name, overrideFlavor := range overrideFlavors {
			rawFlavors[name] = overrideFlavor
		}
	}

	// Process inheritance
	processedFlavors := make(map[string]*FlavorConfig)
	for name, rawFlavor := range rawFlavors {
		// Check for nil flavor entry
		if rawFlavor == nil {
			return nil, fmt.Errorf("flavor %s has nil configuration in YAML", name)
		}

		processed, err := processFlavorInheritance(name, rawFlavor, rawFlavors)
		if err != nil {
			return nil, fmt.Errorf("failed to process flavor %s: %w", name, err)
		}
		processedFlavors[name] = processed
	}

	return processedFlavors, nil
}

// GetFlavor loads and returns a specific flavor configuration
func GetFlavor(flavorName, flavorsFile, overrideFile string) (*FlavorConfig, error) {
	flavors, err := LoadFlavors(flavorsFile, overrideFile)
	if err != nil {
		return nil, err
	}

	flavor, exists := flavors[flavorName]
	if !exists {
		return nil, fmt.Errorf("flavor %s not found", flavorName)
	}

	return flavor, nil
}

// ListFlavors returns a list of available flavor names
func ListFlavors(flavorsFile, overrideFile string) ([]string, error) {
	flavors, err := LoadFlavors(flavorsFile, overrideFile)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(flavors))
	for name := range flavors {
		names = append(names, name)
	}

	return names, nil
}

// processFlavorInheritance resolves inheritance for a flavor
func processFlavorInheritance(name string, rawFlavor *FlavorConfigRaw, allFlavors FlavorsMap) (*FlavorConfig, error) {
	return processFlavorInheritanceWithStack(name, rawFlavor, allFlavors, make(map[string]bool))
}

func processFlavorInheritanceWithStack(name string, rawFlavor *FlavorConfigRaw, allFlavors FlavorsMap, visited map[string]bool) (*FlavorConfig, error) {
	// Check for nil rawFlavor entry
	if rawFlavor == nil {
		return nil, fmt.Errorf("flavor %s has nil configuration", name)
	}

	// Check for circular inheritance
	if visited[name] {
		return nil, fmt.Errorf("circular inheritance detected involving flavor %s", name)
	}

	if rawFlavor.Inherit == "" {
		// No inheritance, return as-is
		return &rawFlavor.FlavorConfig, nil
	}

	// Find parent flavor
	parent, exists := allFlavors[rawFlavor.Inherit]
	if !exists {
		return nil, fmt.Errorf("parent flavor %s not found for flavor %s", rawFlavor.Inherit, name)
	}

	// Check for nil parent flavor entry
	if parent == nil {
		return nil, fmt.Errorf("parent flavor %s has nil configuration (referenced by flavor %s)", rawFlavor.Inherit, name)
	}

	// Add current flavor to visited set
	visited[name] = true

	// Recursively process parent (in case it also inherits)
	processedParent, err := processFlavorInheritanceWithStack(rawFlavor.Inherit, parent, allFlavors, visited)
	if err != nil {
		return nil, err
	}

	// Remove current flavor from visited set (backtrack)
	delete(visited, name)

	// Merge parent with current flavor
	result := mergeFlavorConfigs(processedParent, &rawFlavor.FlavorConfig)
	return result, nil
}

// mergeFlavorConfigs merges child flavor config into parent, with child values taking precedence
func mergeFlavorConfigs(parent, child *FlavorConfig) *FlavorConfig {
	result := &FlavorConfig{}

	// Copy parent first
	*result = *parent

	// Deep copy map fields to avoid shared references
	deepCopyMaps(result, parent)

	// Override with child values where they exist
	mergeBasicFields(result, child)
	mergeAnnotations(result, child)
	mergeBuildImages(result, child)
	mergeImages(result, child)
	mergeAgentImages(result, child)
	mergeTimeouts(result, child)

	return result
}

// deepCopyMaps creates new maps to avoid shared references
func deepCopyMaps(result, parent *FlavorConfig) {
	// Copy Annotations
	if parent.Annotations != nil {
		result.Annotations = make(map[string]string)
		for k, v := range parent.Annotations {
			result.Annotations[k] = v
		}
	}

	// Copy Images
	if parent.Images != nil {
		result.Images = make(map[string]ImageConfig)
		for k, v := range parent.Images {
			result.Images[k] = v
		}
	}
}

// mergeBasicFields merges basic string fields
func mergeBasicFields(result, child *FlavorConfig) {
	if child.Name != "" {
		result.Name = child.Name
	}
	if child.Description != "" {
		result.Description = child.Description
	}
	if child.Home != "" {
		result.Home = child.Home
	}
	if child.Icon != "" {
		result.Icon = child.Icon
	}
}

// mergeAnnotations merges annotation maps
func mergeAnnotations(result, child *FlavorConfig) {
	if child.Annotations != nil {
		if result.Annotations == nil {
			result.Annotations = make(map[string]string)
		}
		for k, v := range child.Annotations {
			result.Annotations[k] = v
		}
	}
}

// mergeBuildImages merges build image configuration
func mergeBuildImages(result, child *FlavorConfig) {
	if child.BuildImages.GoToolset != "" {
		result.BuildImages.GoToolset = child.BuildImages.GoToolset
	}
	if child.BuildImages.UbiMinimal != "" {
		result.BuildImages.UbiMinimal = child.BuildImages.UbiMinimal
	}
	if child.BuildImages.Base.Image != "" {
		result.BuildImages.Base.Image = child.BuildImages.Base.Image
	}
	if child.BuildImages.Base.Tag != "" {
		result.BuildImages.Base.Tag = child.BuildImages.Base.Tag
	}
	if child.BuildImages.Base.MinimalImage.Image != "" {
		result.BuildImages.Base.MinimalImage.Image = child.BuildImages.Base.MinimalImage.Image
	}
	if child.BuildImages.Base.MinimalImage.Tag != "" {
		result.BuildImages.Base.MinimalImage.Tag = child.BuildImages.Base.MinimalImage.Tag
	}
}

// mergeImages merges service image configurations
func mergeImages(result, child *FlavorConfig) {
	if child.Images != nil {
		if result.Images == nil {
			result.Images = make(map[string]ImageConfig)
		}
		for k, v := range child.Images {
			if existing, exists := result.Images[k]; exists {
				// Merge individual image config
				merged := existing
				if v.Image != "" {
					merged.Image = v.Image
				}
				if v.Tag != "" {
					merged.Tag = v.Tag
				}
				result.Images[k] = merged
			} else {
				result.Images[k] = v
			}
		}
	}
}

// mergeAgentImages merges agent image configuration with pointer boolean handling
func mergeAgentImages(result, child *FlavorConfig) {
	if child.AgentImages.OsId != "" {
		result.AgentImages.OsId = child.AgentImages.OsId
	}
	if child.AgentImages.DeviceBaseImage != "" {
		result.AgentImages.DeviceBaseImage = child.AgentImages.DeviceBaseImage
	}
	// Handle pointer boolean fields properly - nil means inherit from parent
	if child.AgentImages.EnableCrb != nil {
		result.AgentImages.EnableCrb = child.AgentImages.EnableCrb
	}
	if child.AgentImages.EpelNext != nil {
		result.AgentImages.EpelNext = child.AgentImages.EpelNext
	}
}

// mergeTimeouts merges timeout configurations
func mergeTimeouts(result, child *FlavorConfig) {
	if child.Timeouts.DB != 0 {
		result.Timeouts.DB = child.Timeouts.DB
	}
	if child.Timeouts.KV != 0 {
		result.Timeouts.KV = child.Timeouts.KV
	}
	if child.Timeouts.Migration != 0 {
		result.Timeouts.Migration = child.Timeouts.Migration
	}
}

// GetFlavorImageTag returns the image and tag for a specific service in a flavor
func (f *FlavorConfig) GetFlavorImageTag(serviceName string) (image, tag string, found bool) {
	if imageConfig, exists := f.Images[serviceName]; exists {
		return imageConfig.Image, imageConfig.Tag, true
	}
	return "", "", false
}

// GetBuildImageReference returns the full image reference for build images
func (f *FlavorConfig) GetBuildImageReference(buildImageName string) (string, error) {
	switch buildImageName {
	case "goToolset":
		if f.BuildImages.GoToolset == "" {
			return "", fmt.Errorf("goToolset not defined for flavor")
		}
		return f.BuildImages.GoToolset, nil
	case "ubiMinimal":
		if f.BuildImages.UbiMinimal == "" {
			return "", fmt.Errorf("ubiMinimal not defined for flavor")
		}
		return f.BuildImages.UbiMinimal, nil
	case "base":
		if f.BuildImages.Base.Image == "" {
			return "", fmt.Errorf("base image not defined for flavor")
		}
		if f.BuildImages.Base.Tag == "" {
			return f.BuildImages.Base.Image, nil
		}
		return f.BuildImages.Base.Image + ":" + f.BuildImages.Base.Tag, nil
	case "baseMinimal":
		if f.BuildImages.Base.MinimalImage.Image == "" {
			return "", fmt.Errorf("base minimal image not defined for flavor")
		}
		if f.BuildImages.Base.MinimalImage.Tag == "" {
			return f.BuildImages.Base.MinimalImage.Image, nil
		}
		return f.BuildImages.Base.MinimalImage.Image + ":" + f.BuildImages.Base.MinimalImage.Tag, nil
	default:
		return "", fmt.Errorf("unknown build image name: %s", buildImageName)
	}
}

// FindFlavorsFile searches for the flavors file starting from the current directory
func FindFlavorsFile() (string, error) {
	// Start from current directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	for {
		flavorsPath := filepath.Join(dir, DefaultFlavorsFile)
		if _, err := os.Stat(flavorsPath); err == nil {
			return flavorsPath, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root directory
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("flavors file not found in current directory or any parent directory")
}
