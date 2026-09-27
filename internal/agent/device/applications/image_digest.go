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

// digestFromKubernetesImageID returns a registry digest from a pullable image ID
// only when it refers to the same repository as the pod's declared image. CRI
// runtime IDs can instead identify a config object, which is not usable for a
// registry delta lookup.
func digestFromKubernetesImageID(imageRef, imageID string) string {
	if digest := digestFromReference(imageRef); digest != "" {
		return digest
	}

	const pullableImagePrefix = "docker-pullable://"
	if !strings.HasPrefix(imageID, pullableImagePrefix) {
		return ""
	}

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
	return digested.Digest().String()
}
