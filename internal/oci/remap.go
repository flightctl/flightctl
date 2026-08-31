package oci

import (
	"fmt"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	"github.com/containers/image/v5/types"
)

func RewriteImageRef(image string) (string, error) {
	return rewriteImageRef(nil, image)
}

func rewriteImageRef(sys *types.SystemContext, image string) (string, error) {
	prefix := ""
	raw := image
	if strings.HasPrefix(raw, "docker://") {
		prefix = "docker://"
		raw = strings.TrimPrefix(raw, "docker://")
	}
	named, err := reference.ParseNormalizedNamed(raw)
	if err != nil {
		return "", fmt.Errorf("parse image reference: %w", err)
	}
	reg, err := sysregistriesv2.FindRegistry(sys, named.Name())
	if err != nil {
		return "", fmt.Errorf("read container registries config: %w", err)
	}
	if reg == nil {
		return image, nil
	}
	sources, err := reg.PullSourcesFromReference(named)
	if err != nil {
		return "", fmt.Errorf("rewrite image reference %q: %w", image, err)
	}
	if len(sources) == 0 {
		return "", fmt.Errorf("rewrite image reference %q: no pull sources", image)
	}
	return prefix + sources[0].Reference.String(), nil
}
