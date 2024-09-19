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

// Returns the image formatted in a <base>:<tag>@<digest> format
func (i Image) String() string {
	if i.Digest != "" {
		if i.Tag != "" {
			return fmt.Sprintf("%s:%s@%s", i.Base, i.Tag, i.Digest)
		}
		return fmt.Sprintf("%s@%s", i.Base, i.Digest)
	}
	if i.Tag != "" {
		return fmt.Sprintf("%s:%s", i.Base, i.Tag)
	}
	return i.Base
}

// Bootc does not accept images with a tag AND digest specified
// In cases when both are present the digest will be returned and NOT a tag
func (i *Image) ToBootcTarget() string {
	if i.Digest != "" {
		return fmt.Sprintf("%s@%s", i.Base, i.Digest)
	}
	if i.Tag != "" {
		return fmt.Sprintf("%s:%s", i.Base, i.Tag)
	}
	return i.Base
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
	return parseImage(spec.Image)
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
