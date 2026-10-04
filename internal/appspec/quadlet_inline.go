package appspec

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
	"sigs.k8s.io/yaml"
)

const dropinExtension = ".conf"

// ParseQuadletReferencesFromSpec parses Quadlet specifications from a slice of inline application content.
// Drop-in overrides are applied to ensure that the specs
// It returns a map where the key is the filename and the value is the parsed QuadletSpec.
func ParseQuadletReferencesFromSpec(contents []v1beta1.ApplicationContent) (map[string]*common.QuadletReferences, error) {
	baseFiles := make(map[string][]byte)
	dropinFiles := make(map[string]map[string][]byte)

	for _, c := range contents {
		filename := c.Path
		if filename == "" {
			continue
		}

		contentBytes, err := c.ContentsDecoded()
		if err != nil {
			return nil, fmt.Errorf("decoding content %q: %w", filename, err)
		}

		ext := filepath.Ext(filename)
		if _, ok := common.SupportedQuadletExtensions[ext]; ok {
			baseFiles[filename] = contentBytes
		} else if ext == dropinExtension {
			// treat all .conf files as dropins for simplicity
			// when processed later, only .conf files that are in dropin directories
			// will actually be processed
			dir := filepath.Dir(filename)
			if _, ok := dropinFiles[dir]; !ok {
				dropinFiles[dir] = make(map[string][]byte)
			}
			dropinFiles[dir][filepath.Base(filename)] = contentBytes
		}
	}

	if len(baseFiles) == 0 {
		return nil, fmt.Errorf("%w: in app spec", common.ErrNoQuadletFile)
	}

	quadlets := make(map[string]*common.QuadletReferences)

	// process dropins to ensure all images are retrieved properly
	for filename, content := range baseFiles {
		baseUnit, err := quadlet.NewUnit(content)
		if err != nil {
			return nil, fmt.Errorf("deserializing content %q: %w", filename, err)
		}

		dropinDirs := quadlet.DropinDirectories(filename)
		var dropinNames []string
		// map of dropin file to the contents of the drop in.
		dropinContents := make(map[string][]byte)

		// once a dropin is discovered, any other dropins matching the same name will be ignored
		for _, dropinDir := range dropinDirs {
			if dropins, ok := dropinFiles[dropinDir]; ok {
				for name, data := range dropins {
					if _, ok = dropinContents[name]; !ok {
						dropinNames = append(dropinNames, name)
						dropinContents[name] = data
					}
				}
			}

		}
		// sort the names in ascending order so that the highest priority items are applied
		// last which allows for proper overriding
		sort.Strings(dropinNames)
		for _, dropinName := range dropinNames {
			dropinContent := dropinContents[dropinName]
			dropin, err := quadlet.NewUnit(dropinContent)
			if err != nil {
				return nil, fmt.Errorf("deserializing drop-in content %q: %w", dropinName, err)
			}

			baseUnit.Merge(dropin)
		}

		mergedContents, err := baseUnit.Write()
		if err != nil {
			return nil, fmt.Errorf("serializing merged quadlet: %w", err)
		}
		spec, err := common.ParseQuadletReferences(mergedContents)
		if err != nil {
			return nil, fmt.Errorf("parsing quadlet spec from %q: %w", filename, err)
		}

		quadlets[filename] = spec
	}

	return quadlets, nil
}

// KubePodImageReferences lists images referenced by a Pod manifest used by a
// .kube Quadlet.
type KubePodImageReferences struct {
	// VolumeImages lists images used by image-backed Pod volumes.
	VolumeImages []string
	// InitContainerImages lists images used by init containers.
	InitContainerImages []string
	// ContainerImages lists images used by regular containers.
	ContainerImages []string
}

type kubePodManifest struct {
	Spec struct {
		Volumes []struct {
			Image *struct {
				Reference string `yaml:"reference"`
			} `yaml:"image,omitempty"`
		} `yaml:"volumes"`
		InitContainers []struct {
			Image string `yaml:"image"`
		} `yaml:"initContainers"`
		Containers []struct {
			Image string `yaml:"image"`
		} `yaml:"containers"`
	} `yaml:"spec"`
}

// ParseKubePodImageReferences parses the image references from a Pod manifest.
func ParseKubePodImageReferences(podYAML []byte) (KubePodImageReferences, error) {
	var pod kubePodManifest
	if err := yaml.Unmarshal(podYAML, &pod); err != nil {
		return KubePodImageReferences{}, fmt.Errorf("parsing pod YAML: %w", err)
	}

	var refs KubePodImageReferences
	for _, volume := range pod.Spec.Volumes {
		if volume.Image != nil && volume.Image.Reference != "" {
			refs.VolumeImages = append(refs.VolumeImages, volume.Image.Reference)
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if container.Image != "" {
			refs.InitContainerImages = append(refs.InitContainerImages, container.Image)
		}
	}
	for _, container := range pod.Spec.Containers {
		if container.Image != "" {
			refs.ContainerImages = append(refs.ContainerImages, container.Image)
		}
	}
	return refs, nil
}

// ParseQuadletImageReferencesFromSpec returns external image references from
// inline Quadlet units, including the referenced Pod YAML for .kube units.
func ParseQuadletImageReferencesFromSpec(contents []v1beta1.ApplicationContent) ([]string, error) {
	quadlets, err := ParseQuadletReferencesFromSpec(contents)
	if err != nil {
		return nil, err
	}

	var refs []string
	for _, parsed := range quadlets {
		if parsed == nil {
			continue
		}
		if parsed.Image != nil && !quadlet.IsImageReference(*parsed.Image) {
			refs = append(refs, *parsed.Image)
		}
		for _, image := range parsed.MountImages {
			if !quadlet.IsImageReference(image) {
				refs = append(refs, image)
			}
		}
		if parsed.Type != common.QuadletTypeKube {
			continue
		}
		if parsed.YamlFile == nil {
			return nil, fmt.Errorf("kube unit is missing %s= directive in [%s]", quadlet.KubeYamlKey, quadlet.KubeGroup)
		}

		yamlPath := filepath.Clean(*parsed.YamlFile)
		if !filepath.IsLocal(yamlPath) {
			return nil, fmt.Errorf("pod YAML path %q is not a valid relative path", *parsed.YamlFile)
		}
		var podYAML []byte
		found := false
		for i := range contents {
			if filepath.Clean(contents[i].Path) != yamlPath {
				continue
			}
			podYAML, err = contents[i].ContentsDecoded()
			if err != nil {
				return nil, fmt.Errorf("decoding pod YAML %q: %w", yamlPath, err)
			}
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("pod YAML file %q not found in inline content", yamlPath)
		}
		podImages, err := ParseKubePodImageReferences(podYAML)
		if err != nil {
			return nil, fmt.Errorf("extracting OCI image references from pod YAML %q: %w", yamlPath, err)
		}
		refs = append(refs, podImages.VolumeImages...)
		refs = append(refs, podImages.InitContainerImages...)
		refs = append(refs, podImages.ContainerImages...)
	}
	return refs, nil
}
