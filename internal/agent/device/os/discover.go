package os

import (
	"strings"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/util/validation"
)

const (
	ociDeltaArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	ociDeltaSourceAnnotation = "io.github.containers.delta.source"
)

func selectOSDeltaCandidate(hint *string, targetImage, sourceDigest string, index *client.OCIIndex) string {
	if hint != nil && *hint != "" {
		return *hint
	}
	if index == nil {
		return ""
	}

	repo := imageRepository(targetImage)
	if repo == "" {
		return ""
	}

	for _, ref := range index.Manifests {
		if ref.ArtifactType != ociDeltaArtifactType {
			continue
		}
		if !digestEqual(ref.Annotations[ociDeltaSourceAnnotation], sourceDigest) {
			continue
		}
		if ref.Digest == "" {
			continue
		}
		return repo + "@" + ref.Digest
	}
	return ""
}

func imageRepository(image string) string {
	matches := validation.OciImageReferenceRegexp.FindStringSubmatch(image)
	if len(matches) == 0 {
		return ""
	}
	return matches[1]
}

func digestEqual(a, b string) bool {
	return normalizeDigest(a) == normalizeDigest(b)
}

func normalizeDigest(d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}
	if strings.Contains(d, ":") {
		return d
	}
	return "sha256:" + d
}
