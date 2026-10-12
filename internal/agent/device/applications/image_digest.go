package applications

import (
	"strings"

	"github.com/containers/image/v5/docker/reference"
)

// digestFromKubernetesImageID prefers a repository-qualified runtime digest
// when it refers to the same repository as the pod's declared image. Opaque
// runtime IDs and desired references do not verify observed registry content.
func digestFromKubernetesImageID(imageRef, imageID string) string {
	imageID = strings.TrimPrefix(imageID, pullableImagePrefix)

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
			// Use the runtime's registry identity for the observed content.
			// The desired digest can refer to a different publication or image.
			return digested.Digest().String()
		}
	}

	return ""
}
