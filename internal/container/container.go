package container

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/util/validation"
)

var ErrParsingImage = deviceerrors.ErrUnableToParseImageReference

type BootcHost struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
	Status     Status   `json:"status"`
}

type Metadata struct {
	Name string `json:"name"`
}

type Spec struct {
	Image ImageSpec `json:"image"`
}

type ImageSpec struct {
	Image string `json:"image"`
}

type Status struct {
	Staged   ImageStatus `json:"staged"`
	Booted   ImageStatus `json:"booted"`
	Rollback ImageStatus `json:"rollback"`
	Type     string      `json:"type"`
}

type ImageStatus struct {
	Image ImageDetails `json:"image"`
}

type ImageDetails struct {
	Image       ImageSpec `json:"image"`
	ImageDigest string    `json:"imageDigest"`
}

type BootcClient interface {
	// Status returns the current bootc status.
	Status(ctx context.Context) (*BootcHost, error)
	// Switch targets a new container image reference to boot.
	Switch(ctx context.Context, image string) error
	// UsrOverlay adds a transient writable overlayfs on `/usr` that will be discarded on reboot.
	UsrOverlay(ctx context.Context) error
	// Apply restart or reboot into the new target image.
	Apply(ctx context.Context) error
}

// IsOsImageReconciled returns true if the booted image equals the target for the spec image.
func IsOsImageReconciled(host *BootcHost, desiredSpec *v1beta1.DeviceSpec) (bool, error) {
	if desiredSpec.Os == nil {
		return false, nil
	}

	target, err := ImageToBootcTarget(desiredSpec.Os.Image)
	if err != nil {
		return false, err
	}
	// If the booted image equals the desired target, the OS image is reconciled
	return host.GetBootedImage() == target, nil
}

func (b *BootcHost) GetBootedImage() string {
	return b.Status.Booted.Image.Image.Image
}

func (b *BootcHost) GetBootedImageDigest() string {
	return b.Status.Booted.Image.ImageDigest
}

func (b *BootcHost) GetStagedImage() string {
	return b.Status.Staged.Image.Image.Image
}

func (b *BootcHost) GetRollbackImage() string {
	return b.Status.Rollback.Image.Image.Image
}

// Bootc does not accept images with tags AND digests specified - in the case when we
// get both we will use the image digest.
//
// Related underlying issue: https://github.com/containers/image/issues/1736
func ImageToBootcTarget(image string) (string, error) {
	matches := validation.OciImageReferenceRegexp.FindStringSubmatch(image)
	if len(matches) == 0 {
		return image, ErrParsingImage
	}

	// The OciImageReferenceRegexp has 3 capture groups for the base, tag, and digest
	base := matches[1]
	tag := matches[2]
	digest := matches[3]

	if tag != "" && digest != "" {
		return fmt.Sprintf("%s@%s", base, digest), nil
	}

	return image, nil
}
