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

// digestFromKubernetesImageID prefers a pullable registry digest when the
// runtime ID refers to the same repository as the pod's declared image. If the
// runtime ID is opaque, an immutable declared reference remains a usable
// fallback; CRI IDs can identify config objects rather than registry manifests.
func digestFromKubernetesImageID(imageRef, imageID string) string {
	const pullableImagePrefix = "docker-pullable://"
	if strings.HasPrefix(imageID, pullableImagePrefix) {
		declared, err := reference.ParseNormalizedNamed(imageRef)
		if err != nil {
			return ""
		}
		observed, err := reference.ParseNormalizedNamed(strings.TrimPrefix(imageID, pullableImagePrefix))
		if err != nil || observed.Name() != declared.Name() {
			return ""
		}
		digested, ok := observed.(reference.Digested)
		if !ok {
			return ""
		}
		// The runtime's pullable ID identifies the manifest selected from an
		// index. Prefer it to the declared digest, which can name the index.
		return digested.Digest().String()
	}

	// An opaque runtime ID (for example containerd://<config-digest>) cannot be
	// used as a registry source digest. Retain an immutable reference when it is
	// the only registry digest available; the server resolves index refs to the
	// device's selected platform before generating a delta.
	return digestFromReference(imageRef)
}
