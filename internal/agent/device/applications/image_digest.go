package applications

import (
	"strings"

	"github.com/containers/image/v5/docker/reference"
)

// digestFromKubernetesImageID accepts only a matching qualified runtime digest.
func digestFromKubernetesImageID(imageRef, imageID string) string {
	const pullableImagePrefix = "docker-pullable://"
	isPullableReference := strings.HasPrefix(imageID, pullableImagePrefix)
	if isPullableReference {
		imageID = strings.TrimPrefix(imageID, pullableImagePrefix)
	}

	// CRI implementations may report either a runtime-specific opaque ID or a
	// repository-qualified digest (for example, quay.io/acme/app@sha256:...).
	// Only the latter identifies a registry manifest we can use as a source.
	observed, err := reference.ParseNormalizedNamed(imageID)
	if err == nil {
		if digested, ok := observed.(reference.Digested); ok {
			declared, err := reference.ParseNormalizedNamed(imageRef)
			if err != nil || observed.Name() != declared.Name() {
				return ""
			}
			// The runtime's pullable ID identifies the manifest selected from an
			// index. Prefer it to the declared digest, which can name the index.
			return digested.Digest().String()
		}
	}

	// Preserve the old handling of an explicitly pullable ID: if it does not
	// contain a matching repository digest, do not infer one from the spec.
	if isPullableReference {
		return ""
	}

	return ""
}
