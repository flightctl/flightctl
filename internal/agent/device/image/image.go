package image

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/container"
)

type Image struct {
	Base   string
	Tag    string
	Digest string
}

func parseImage(image string) *Image {
	imageObj := &Image{}

	imageAndDigest := strings.SplitN(image, "@", 2)
	if len(imageAndDigest) == 2 {
		imageObj.Digest = imageAndDigest[1]
	}

	imageAndTag := strings.SplitN(imageAndDigest[0], ":", 2)
	imageObj.Base = imageAndTag[0]
	if len(imageAndTag) == 2 {
		imageObj.Tag = imageAndTag[1]
	}

	return imageObj
}

func SpecToImage(spec *v1alpha1.DeviceOSSpec) *Image {
	if spec == nil || spec.Image == "" {
		return nil
	}
	image := parseImage(spec.Image)
	// It is possible for the spec image string to NOT contain a digest but the
	// saved spec has one
	if image.Digest == "" && spec.ImageDigest != nil && *spec.ImageDigest != "" {
		image.Digest = *spec.ImageDigest
	}
	return image
}

func BootcStatusToImage(bootc *container.BootcHost) *Image {
	if bootc == nil {
		return nil
	}

	bootedOsImage := bootc.GetBootedImage()
	image := parseImage(bootedOsImage)

	// If the parsed image string doesn't have a digest, explicitly set it from the bootc status
	if image.Digest == "" {
		image.Digest = bootc.GetBootedImageDigeest()
	}

	return image
}

// TODO does this need more handling for cases where the image might be defined but fields are ""?
func AreImagesEquivalent(first, second *Image) bool {
	if first == nil && second == nil {
		return true
	} else if first == nil && second != nil || first != nil && second == nil {
		return false
	}

	// Digests are unique identifiers and have precedence if defined
	if first.Digest != "" && second.Digest != "" {
		return first.Digest == second.Digest
	}

	if first.Base != second.Base {
		return false
	}

	if first.Tag != second.Tag {
		return false
	}

	return true
}

// TODO define on Image?
// TODO also define a string representation that spits back out the fully built image string?
func ImageToBootcTarget(image *Image) string {
	if image.Digest != "" {
		return fmt.Sprintf("%s@%s", image.Base, image.Digest)
	}
	if image.Tag != "" {
		return fmt.Sprintf("%s:%s", image.Base, image.Tag)
	}
	return image.Base
}
