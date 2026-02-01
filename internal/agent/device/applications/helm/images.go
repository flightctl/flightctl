package helm

import (
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type k8sResource struct {
	APIVersion string            `yaml:"apiVersion,omitempty"`
	Kind       string            `yaml:"kind,omitempty"`
	Metadata   k8sMetadata       `yaml:"metadata,omitempty"`
	Spec       k8sPodSpecWrapper `yaml:"spec,omitempty"`
}

type k8sMetadata struct {
	Name      string `yaml:"name,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`
}

type k8sPodSpecWrapper struct {
	Template       *k8sPodTemplateSpec `yaml:"template,omitempty"`
	JobTemplate    *k8sJobTemplateSpec `yaml:"jobTemplate,omitempty"`
	Containers     []k8sContainer      `yaml:"containers,omitempty"`
	InitContainers []k8sContainer      `yaml:"initContainers,omitempty"`
}

type k8sPodTemplateSpec struct {
	Spec k8sPodSpec `yaml:"spec,omitempty"`
}

type k8sJobTemplateSpec struct {
	Spec k8sJobSpec `yaml:"spec,omitempty"`
}

type k8sJobSpec struct {
	Template k8sPodTemplateSpec `yaml:"template,omitempty"`
}

type k8sPodSpec struct {
	Containers     []k8sContainer `yaml:"containers,omitempty"`
	InitContainers []k8sContainer `yaml:"initContainers,omitempty"`
}

type k8sContainer struct {
	Name  string `yaml:"name,omitempty"`
	Image string `yaml:"image,omitempty"`
}

// ExtractImagesFromManifests parses Kubernetes manifests and returns all container image references.
// It handles multi-document YAML and extracts images from Pods, Deployments, StatefulSets,
// DaemonSets, ReplicaSets, Jobs, and CronJobs.
func ExtractImagesFromManifests(manifests string) ([]string, error) {
	imageSet := make(map[string]struct{})

	decoder := yaml.NewDecoder(strings.NewReader(manifests))
	for {
		var resource k8sResource
		err := decoder.Decode(&resource)
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		images := extractImagesFromResource(&resource)
		for _, img := range images {
			if img != "" {
				imageSet[img] = struct{}{}
			}
		}
	}

	result := make([]string, 0, len(imageSet))
	for img := range imageSet {
		result = append(result, img)
	}
	return result, nil
}

func extractImagesFromResource(resource *k8sResource) []string {
	var images []string

	switch resource.Kind {
	case "Pod":
		images = append(images, extractImagesFromContainers(resource.Spec.Containers)...)
		images = append(images, extractImagesFromContainers(resource.Spec.InitContainers)...)

	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		if resource.Spec.Template != nil {
			images = append(images, extractImagesFromContainers(resource.Spec.Template.Spec.Containers)...)
			images = append(images, extractImagesFromContainers(resource.Spec.Template.Spec.InitContainers)...)
		}

	case "Job":
		if resource.Spec.Template != nil {
			images = append(images, extractImagesFromContainers(resource.Spec.Template.Spec.Containers)...)
			images = append(images, extractImagesFromContainers(resource.Spec.Template.Spec.InitContainers)...)
		}

	case "CronJob":
		if resource.Spec.JobTemplate != nil {
			images = append(images, extractImagesFromContainers(resource.Spec.JobTemplate.Spec.Template.Spec.Containers)...)
			images = append(images, extractImagesFromContainers(resource.Spec.JobTemplate.Spec.Template.Spec.InitContainers)...)
		}
	}

	return images
}

func extractImagesFromContainers(containers []k8sContainer) []string {
	images := make([]string, 0, len(containers))
	for _, c := range containers {
		if c.Image != "" {
			images = append(images, c.Image)
		}
	}
	return images
}
