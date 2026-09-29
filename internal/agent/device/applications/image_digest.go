package applications

import (
	"strings"

	"github.com/containers/image/v5/docker/reference"
)

// digestFromReference returns the content digest when image is immutable.
func digestFromReference(image string) string {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return ""
	}
	digested, ok := named.(reference.Digested)
	if !ok {
		return ""
	}
	return digested.Digest().String()
}

// digestFromKubernetesImageID prefers a repository-qualified runtime digest
// when it refers to the same repository as the pod's declared image. If the
// runtime ID is opaque, an immutable declared reference remains a usable
// fallback; CRI IDs can identify config objects rather than registry manifests.
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

	// An opaque runtime ID (for example containerd://<config-digest>) cannot be
	// used as a registry source digest; keep an immutable declared reference as
	// the fallback when one is available.
	return digestFromReference(imageRef)
}
