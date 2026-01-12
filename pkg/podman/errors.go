package podman

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// Containers
	ErrListContainers      = errors.New("list containers failed")
	ErrUnmarshalContainers = errors.New("unmarshal containers failed")

	// Images
	ErrRemoveImage       = errors.New("remove image failed")
	ErrImageDoesNotExist = errors.New("image does not exist")

	// Volumes
	ErrRemoveVolume       = errors.New("remove volume failed")
	ErrVolumeDoesNotExist = errors.New("volume does not exist")

	// Networks
	ErrRemoveNetwork       = errors.New("remove network failed")
	ErrNetworkDoesNotExist = errors.New("network does not exist")

	// Secrets
	ErrRemoveSecret = errors.New("remove secret failed")
)

func wrapPodmanError(err error, stderr string) error {
	return fmt.Errorf(
		"%w: %s",
		err,
		strings.TrimSpace(stderr),
	)
}
