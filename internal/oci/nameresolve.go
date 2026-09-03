package oci

import (
	"fmt"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/domain"
)

func ImageDestRef(registry, imageName, imageTag string) string {
	return fmt.Sprintf("%s/%s:%s", registry, imageName, imageTag)
}

func RepoDestRef(registry, imageName string) string {
	return fmt.Sprintf("%s/%s", registry, imageName)
}

func RegistryObjectRef(spec *domain.OciRepoSpec, imageName string) (string, error) {
	if spec == nil || spec.Registry == "" {
		return "", fmt.Errorf("OCI write target registry is required")
	}
	repositorySet := spec.Repository != nil && *spec.Repository != ""
	namespaceSet := spec.Namespace != nil && *spec.Namespace != ""
	if repositorySet && namespaceSet {
		return "", fmt.Errorf("spec.repository and spec.namespace are mutually exclusive")
	}

	registry := strings.TrimRight(spec.Registry, "/")
	if repositorySet {
		return RepoDestRef(registry, strings.TrimLeft(*spec.Repository, "/")), nil
	}

	name := strings.TrimLeft(imageName, "/")
	if name == "" {
		return "", fmt.Errorf("image name is required")
	}
	if namespaceSet {
		last := name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			last = name[i+1:]
		}
		return registry + "/" + strings.TrimLeft(*spec.Namespace, "/") + "/" + last, nil
	}
	return registry + "/" + name, nil
}

func SelectWriteTarget(orgTarget, defaultTarget *domain.OciRepoSpec) *domain.OciRepoSpec {
	if orgTarget != nil {
		return orgTarget
	}
	if defaultTarget == nil || defaultTarget.Registry == "" {
		return nil
	}
	return defaultTarget
}

func ResolveDeltaPushPath(spec *domain.OciRepoSpec, imageRepository string) (string, error) {
	imagePath, _, err := imagePathParts(imageRepository)
	if err != nil {
		return "", err
	}
	return RegistryObjectRef(spec, imagePath)
}

func imagePathParts(imageRepository string) (path, lastSegment string, err error) {
	named, err := reference.ParseNormalizedNamed(imageRepository)
	if err != nil {
		return "", "", fmt.Errorf("unparseable image repository %q: %w", imageRepository, err)
	}
	if !hasExplicitRegistryHost(imageRepository) {
		return "", "", fmt.Errorf("unparseable image repository %q: missing registry host", imageRepository)
	}
	path = reference.Path(named)
	if path == "" {
		return "", "", fmt.Errorf("unparseable image repository %q: missing repository path", imageRepository)
	}
	lastSegment = path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		lastSegment = path[i+1:]
	}
	return path, lastSegment, nil
}

func hasExplicitRegistryHost(imageRepository string) bool {
	slash := strings.Index(imageRepository, "/")
	if slash <= 0 {
		return false
	}
	first := imageRepository[:slash]
	return strings.ContainsAny(first, ".:") || strings.EqualFold(first, "localhost")
}
