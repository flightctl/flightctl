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
	return parseQuadletReferencesFromSpec(contents, false)
}

type inlineQuadletFile struct {
	data []byte
	err  error
}

func parseQuadletReferencesFromSpec(contents []v1beta1.ApplicationContent, partial bool) (map[string]*common.QuadletReferences, error) {
	baseFiles := make(map[string]inlineQuadletFile)
	dropinFiles := make(map[string]map[string]inlineQuadletFile)
	var firstErr error

	for _, c := range contents {
		filename := c.Path
		if filename == "" {
			continue
		}

		data, err := c.ContentsDecoded()
		if err != nil {
			err = fmt.Errorf("decoding content %q: %w", filename, err)
			if !partial {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		file := inlineQuadletFile{data: data, err: err}

		ext := filepath.Ext(filename)
		if _, ok := common.SupportedQuadletExtensions[ext]; ok {
			baseFiles[filename] = file
		} else if ext == dropinExtension {
			// treat all .conf files as dropins for simplicity
			// when processed later, only .conf files that are in dropin directories
			// will actually be processed
			dir := filepath.Dir(filename)
			if _, ok := dropinFiles[dir]; !ok {
				dropinFiles[dir] = make(map[string]inlineQuadletFile)
			}
			dropinFiles[dir][filepath.Base(filename)] = file
		}
	}

	if len(baseFiles) == 0 {
		return nil, fmt.Errorf("%w: in app spec", common.ErrNoQuadletFile)
	}

	quadlets := make(map[string]*common.QuadletReferences)

	for filename, file := range baseFiles {
		spec, err := parseMergedQuadlet(filename, file, dropinFiles)
		if err != nil {
			if !partial {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		quadlets[filename] = spec
	}
	return quadlets, firstErr
}

func parseMergedQuadlet(filename string, file inlineQuadletFile, dropinFiles map[string]map[string]inlineQuadletFile) (*common.QuadletReferences, error) {
	if file.err != nil {
		return nil, file.err
	}
	baseUnit, err := quadlet.NewUnit(file.data)
	if err != nil {
		return nil, fmt.Errorf("deserializing content %q: %w", filename, err)
	}

	var dropinNames []string
	dropinContents := make(map[string]inlineQuadletFile)
	// The first directory containing a given drop-in name takes precedence.
	for _, dir := range quadlet.DropinDirectories(filename) {
		for name, dropin := range dropinFiles[dir] {
			if _, ok := dropinContents[name]; !ok {
				dropinNames = append(dropinNames, name)
				dropinContents[name] = dropin
			}
		}
	}
	// Apply drop-ins in ascending order so later settings override earlier ones.
	sort.Strings(dropinNames)
	for _, name := range dropinNames {
		file := dropinContents[name]
		if file.err != nil {
			return nil, file.err
		}
		dropin, err := quadlet.NewUnit(file.data)
		if err != nil {
			return nil, fmt.Errorf("deserializing drop-in content %q: %w", name, err)
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
	return spec, nil
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
	refs, err := parseQuadletImageReferencesFromSpec(contents, false)
	if err != nil {
		return nil, err
	}
	var images []string
	for _, ref := range refs {
		images = append(images, ref.Image)
	}
	return images, nil
}

type QuadletImageReference struct {
	Image    string
	IsVolume bool
}

// ParsePartialQuadletImageReferencesFromSpec retains image references from readable units and Pod files.
func ParsePartialQuadletImageReferencesFromSpec(contents []v1beta1.ApplicationContent) ([]QuadletImageReference, error) {
	return parseQuadletImageReferencesFromSpec(contents, true)
}

func parseQuadletImageReferencesFromSpec(contents []v1beta1.ApplicationContent, partial bool) ([]QuadletImageReference, error) {
	quadlets, firstErr := parseQuadletReferencesFromSpec(contents, partial)
	if firstErr != nil && !partial {
		return nil, firstErr
	}

	var refs []QuadletImageReference
	for _, parsed := range quadlets {
		if parsed.Image != nil && !quadlet.IsImageReference(*parsed.Image) {
			refs = append(refs, QuadletImageReference{Image: *parsed.Image, IsVolume: parsed.Type == common.QuadletTypeVolume})
		}
		for _, image := range parsed.MountImages {
			if !quadlet.IsImageReference(image) {
				refs = append(refs, QuadletImageReference{Image: image, IsVolume: true})
			}
		}
		if parsed.Type != common.QuadletTypeKube {
			continue
		}
		podImages, err := inlinePodImages(parsed.YamlFile, contents)
		if err != nil {
			if !partial {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, image := range podImages.VolumeImages {
			refs = append(refs, QuadletImageReference{Image: image, IsVolume: true})
		}
		for _, image := range podImages.InitContainerImages {
			refs = append(refs, QuadletImageReference{Image: image})
		}
		for _, image := range podImages.ContainerImages {
			refs = append(refs, QuadletImageReference{Image: image})
		}
	}
	return refs, firstErr
}

func inlinePodImages(yamlFile *string, contents []v1beta1.ApplicationContent) (KubePodImageReferences, error) {
	if yamlFile == nil {
		return KubePodImageReferences{}, fmt.Errorf("kube unit is missing %s= directive in [%s]", quadlet.KubeYamlKey, quadlet.KubeGroup)
	}
	yamlPath := filepath.Clean(*yamlFile)
	if !filepath.IsLocal(yamlPath) {
		return KubePodImageReferences{}, fmt.Errorf("pod YAML path %q is not a valid relative path", *yamlFile)
	}
	for i := range contents {
		if filepath.Clean(contents[i].Path) != yamlPath {
			continue
		}
		podYAML, err := contents[i].ContentsDecoded()
		if err != nil {
			return KubePodImageReferences{}, fmt.Errorf("decoding pod YAML %q: %w", yamlPath, err)
		}
		podImages, err := ParseKubePodImageReferences(podYAML)
		if err != nil {
			return KubePodImageReferences{}, fmt.Errorf("extracting OCI image references from pod YAML %q: %w", yamlPath, err)
		}
		return podImages, nil
	}
	return KubePodImageReferences{}, fmt.Errorf("pod YAML file %q not found in inline content", yamlPath)
}
