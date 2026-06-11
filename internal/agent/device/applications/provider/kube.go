package provider

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/quadlet"
	"sigs.k8s.io/yaml"
)

const (
	// virtLauncherImagePrefix is the OCI image prefix used by KubeVirt's virt-launcher.
	// Any pod whose volumes or containers reference this prefix is classified as a VM workload.
	virtLauncherImagePrefix = "quay.io/kubevirt/virt-launcher"
)

// podSpec is a minimal struct for parsing the subset of a Podman Pod YAML that
// the agent needs for OCI prefetch and VM workload detection.
// Only fields required by the design (§4.1) are defined — no external k8s.io/api dependency.
type podSpec struct {
	Spec podSpecInner `yaml:"spec"`
}

type podSpecInner struct {
	Containers     []podContainer `yaml:"containers"`
	InitContainers []podContainer `yaml:"initContainers"`
	Volumes        []podVolume    `yaml:"volumes"`
}

type podContainer struct {
	Image string `yaml:"image"`
}

type podVolume struct {
	Image *podVolumeImage `yaml:"image,omitempty"`
}

type podVolumeImage struct {
	Reference string `yaml:"reference"`
}

// isVMWorkload reports whether the parsed pod represents a VM workload by checking
// whether any volume image reference or container image matches the virt-launcher prefix.
func isVMWorkload(pod *podSpec) bool {
	for _, vol := range pod.Spec.Volumes {
		if vol.Image != nil && strings.HasPrefix(vol.Image.Reference, virtLauncherImagePrefix) {
			return true
		}
	}
	for _, c := range pod.Spec.Containers {
		if strings.HasPrefix(c.Image, virtLauncherImagePrefix) {
			return true
		}
	}
	return false
}

// parsePodYAMLTargets parses a Kubernetes Pod YAML produced by kubevirt-vm-to-pod and
// extracts all OCI pull targets according to the design (§4.1):
//   - spec.volumes[].image.reference → OCITypeAuto
//   - spec.initContainers[].image    → OCITypeAuto
//   - spec.containers[].image        → OCITypePodmanImage
//
// References are deduplicated by value: the first source that registers a reference wins,
// so volumes (OCITypeAuto) take precedence over containers (OCITypePodmanImage) for the
// same reference. Returns the list of pull targets and whether the pod is a VM workload.
func parsePodYAMLTargets(
	podYAML []byte,
	configProvider dependency.PullConfigResolver,
	user v1beta1.Username,
) ([]dependency.OCIPullTarget, bool, error) {
	var pod podSpec
	if err := yaml.Unmarshal(podYAML, &pod); err != nil {
		return nil, false, fmt.Errorf("parsing pod YAML: %w", err)
	}

	seen := make(map[string]struct{})
	var targets []dependency.OCIPullTarget

	addTarget := func(ref string, ociType dependency.OCIType) {
		if ref == "" {
			return
		}
		if _, exists := seen[ref]; exists {
			return
		}
		seen[ref] = struct{}{}
		targets = append(targets, dependency.OCIPullTarget{
			Type:         ociType,
			Reference:    ref,
			PullPolicy:   v1beta1.PullIfNotPresent,
			ClientOptsFn: containerPullOptions(configProvider, user),
		})
	}

	for _, vol := range pod.Spec.Volumes {
		if vol.Image != nil {
			addTarget(vol.Image.Reference, dependency.OCITypeAuto)
		}
	}

	for _, c := range pod.Spec.InitContainers {
		addTarget(c.Image, dependency.OCITypeAuto)
	}

	for _, c := range pod.Spec.Containers {
		addTarget(c.Image, dependency.OCITypePodmanImage)
	}

	return targets, isVMWorkload(&pod), nil
}

// collectKubePodTargets extracts OCI pull targets from a .kube inline Quadlet application.
// It reads the Yaml= directive in the .kube unit to locate the pod YAML filename, finds
// that file in inlineContent, and delegates to parsePodYAMLTargets.
// Returns an error if the .kube unit cannot be parsed, if the Yaml= directive is absent,
// or if the referenced pod YAML file is not found in the inline set.
func collectKubePodTargets(
	kubeContent []byte,
	inlineContent []v1beta1.ApplicationContent,
	configProvider dependency.PullConfigResolver,
	user v1beta1.Username,
) ([]dependency.OCIPullTarget, error) {
	unit, err := quadlet.NewUnit(kubeContent)
	if err != nil {
		return nil, fmt.Errorf("parsing kube unit: %w", err)
	}

	yamlFilename, err := unit.Lookup(quadlet.KubeGroup, quadlet.KubeYamlKey)
	if err != nil {
		return nil, fmt.Errorf("kube unit missing %s= directive in [%s]: %w", quadlet.KubeYamlKey, quadlet.KubeGroup, err)
	}
	yamlFilename = filepath.Base(yamlFilename)

	for i := range inlineContent {
		if filepath.Base(inlineContent[i].Path) == yamlFilename {
			podYAML, err := inlineContent[i].ContentsDecoded()
			if err != nil {
				return nil, fmt.Errorf("decoding pod YAML %q: %w", yamlFilename, err)
			}
			targets, _, err := parsePodYAMLTargets(podYAML, configProvider, user)
			if err != nil {
				return nil, fmt.Errorf("extracting OCI targets from pod YAML %q: %w", yamlFilename, err)
			}
			return targets, nil
		}
	}

	return nil, fmt.Errorf("pod YAML file %q not found in inline content", yamlFilename)
}
