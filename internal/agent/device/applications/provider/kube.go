package provider

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/quadlet"
	"sigs.k8s.io/yaml"
)

// podSpec is a minimal struct for parsing the subset of a Podman Pod YAML that
// the agent needs for OCI prefetch and VM container name resolution.
// Only fields required by the design are defined — no external k8s.io/api dependency.
type podSpec struct {
	Metadata podMetadata  `yaml:"metadata"`
	Spec     podSpecInner `yaml:"spec"`
}

type podMetadata struct {
	Name        string            `yaml:"name"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
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

// Annotation keys set by kubevirt-vm-to-pod on the generated pod spec.
const (
	annotationDefaultContainer = "kubectl.kubernetes.io/default-container"
	annotationKubeVirtDomain   = "kubevirt.io/domain"

	// virtLauncherDomainNamespace is the libvirt namespace prefix used by
	// KubeVirt virt-launcher in standalone (non-Kubernetes) deployments.
	virtLauncherDomainNamespace = "default"
)

// VMContainerInfo holds the resolved container and domain names parsed from the
// pod YAML produced by kubevirt-vm-to-pod, before appID namespacing is applied.
type VMContainerInfo struct {
	// OriginalPodName is metadata.name from the pod YAML (e.g. "virt-launcher-demo-vm").
	// The caller must apply appID namespacing to get the final Podman pod name.
	OriginalPodName string
	// ContainerName is the default container (kubectl.kubernetes.io/default-container annotation).
	ContainerName string
	// KubeVirtDomain is the libvirt domain identifier (kubevirt.io/domain annotation).
	KubeVirtDomain string
}

// parseVMContainerInfo extracts container and domain information from a pod YAML
// produced by kubevirt-vm-to-pod. Returns an error if required fields are missing.
func parseVMContainerInfo(podYAML []byte) (VMContainerInfo, error) {
	var pod podSpec
	if err := yaml.Unmarshal(podYAML, &pod); err != nil {
		return VMContainerInfo{}, fmt.Errorf("parsing pod YAML: %w", err)
	}

	if pod.Metadata.Name == "" {
		return VMContainerInfo{}, fmt.Errorf("pod YAML missing metadata.name")
	}

	containerName := pod.Metadata.Annotations[annotationDefaultContainer]
	if containerName == "" {
		return VMContainerInfo{}, fmt.Errorf("pod YAML missing %s annotation", annotationDefaultContainer)
	}

	domain := pod.Metadata.Annotations[annotationKubeVirtDomain]
	if domain == "" {
		return VMContainerInfo{}, fmt.Errorf("pod YAML missing %s annotation", annotationKubeVirtDomain)
	}

	return VMContainerInfo{
		OriginalPodName: pod.Metadata.Name,
		ContainerName:   containerName,
		KubeVirtDomain:  domain,
	}, nil
}

// lookupVMContainerInfo finds the .kube unit in inlineContent, follows its Yaml=
// directive to the pod YAML, and parses VM container names from that pod YAML.
func lookupVMContainerInfo(inlineContent []v1beta1.ApplicationContent) (VMContainerInfo, error) {
	for i := range inlineContent {
		if !strings.HasSuffix(inlineContent[i].Path, ".kube") {
			continue
		}

		kubeContent, err := inlineContent[i].ContentsDecoded()
		if err != nil {
			return VMContainerInfo{}, fmt.Errorf("decoding kube unit %q: %w", inlineContent[i].Path, err)
		}

		unit, err := quadlet.NewUnit(kubeContent)
		if err != nil {
			return VMContainerInfo{}, fmt.Errorf("parsing kube unit %q: %w", inlineContent[i].Path, err)
		}

		cleanPath, found, err := lookupKubeYamlPath(unit)
		if err != nil {
			return VMContainerInfo{}, err
		}
		if !found {
			continue
		}

		for j := range inlineContent {
			if filepath.Clean(inlineContent[j].Path) != cleanPath {
				continue
			}
			podYAML, err := inlineContent[j].ContentsDecoded()
			if err != nil {
				return VMContainerInfo{}, fmt.Errorf("decoding pod YAML %q: %w", cleanPath, err)
			}
			return parseVMContainerInfo(podYAML)
		}

		return VMContainerInfo{}, fmt.Errorf("pod YAML %q not found in inline content", cleanPath)
	}

	return VMContainerInfo{}, fmt.Errorf("no .kube unit found in inline content")
}

// parsePodYAMLTargets parses a Kubernetes Pod YAML produced by kubevirt-vm-to-pod and
// extracts all OCI pull targets according to the design (§4.1):
//   - spec.volumes[].image.reference → OCITypeAuto
//   - spec.initContainers[].image    → OCITypeAuto
//   - spec.containers[].image        → OCITypePodmanImage
//
// References are deduplicated by value: the first source that registers a reference wins,
// so volumes (OCITypeAuto) take precedence over containers (OCITypePodmanImage) for the
// same reference.
func parsePodYAMLTargets(
	podYAML []byte,
	configProvider dependency.PullConfigResolver,
	user v1beta1.Username,
) ([]dependency.OCIPullTarget, error) {
	var pod podSpec
	if err := yaml.Unmarshal(podYAML, &pod); err != nil {
		return nil, fmt.Errorf("parsing pod YAML: %w", err)
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

	return targets, nil
}

// lookupKubeYamlPath reads the Yaml= directive from a parsed .kube unit and
// validates the value as a safe relative path. It returns (cleanPath, true, nil)
// on success; ("", false, nil) when the [Kube] section or Yaml= key is absent
// (not an error — the caller decides whether to skip or fail); and ("", false, err)
// for any unexpected lookup error or an unsafe path value.
func lookupKubeYamlPath(unit *quadlet.Unit) (string, bool, error) {
	yamlFilename, err := unit.Lookup(quadlet.KubeGroup, quadlet.KubeYamlKey)
	if err != nil {
		if errors.Is(err, quadlet.ErrSectionNotFound) || errors.Is(err, quadlet.ErrKeyNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("looking up %s= in [%s]: %w", quadlet.KubeYamlKey, quadlet.KubeGroup, err)
	}
	cleanPath := filepath.Clean(yamlFilename)
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("pod YAML path %q is not a valid relative path", yamlFilename)
	}
	return cleanPath, true, nil
}

// collectKubePodTargets extracts OCI pull targets from a .kube inline Quadlet application.
// It reads the Yaml= directive in the .kube unit to locate the pod YAML filename, finds
// that file in inlineContent, and delegates to parsePodYAMLTargets.
// Returns an error if the .kube unit cannot be parsed, if the Yaml= directive is absent,
// if the path is absolute or contains traversal sequences, or if the referenced pod YAML
// file is not found in the inline set.
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

	cleanPath, found, err := lookupKubeYamlPath(unit)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("kube unit missing %s= directive in [%s]", quadlet.KubeYamlKey, quadlet.KubeGroup)
	}

	for i := range inlineContent {
		if filepath.Clean(inlineContent[i].Path) == cleanPath {
			podYAML, err := inlineContent[i].ContentsDecoded()
			if err != nil {
				return nil, fmt.Errorf("decoding pod YAML %q: %w", cleanPath, err)
			}
			targets, err := parsePodYAMLTargets(podYAML, configProvider, user)
			if err != nil {
				return nil, fmt.Errorf("extracting OCI targets from pod YAML %q: %w", cleanPath, err)
			}
			return targets, nil
		}
	}

	return nil, fmt.Errorf("pod YAML file %q not found in inline content", cleanPath)
}
