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
	if spec == nil || spec.Registry == "" {
		return "", fmt.Errorf("OCI write target registry is required")
	}
	repositorySet := spec.Repository != nil && *spec.Repository != ""
	namespaceSet := spec.Namespace != nil && *spec.Namespace != ""
	if repositorySet && namespaceSet {
		return "", fmt.Errorf("spec.repository and spec.namespace are mutually exclusive")
	}

	imagePath, lastSegment, err := imagePathParts(imageRepository)
	if err != nil {
		return "", err
	}
	if repositorySet {
		return spec.Registry + "/" + *spec.Repository, nil
	}
	if namespaceSet {
		return spec.Registry + "/" + *spec.Namespace + "/" + lastSegment, nil
	}
	return spec.Registry + "/" + imagePath, nil
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
