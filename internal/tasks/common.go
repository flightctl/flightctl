package tasks

import (
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
)

const ItemsPerPage = 1000

var (
	ErrUnknownConfigName      = errors.New("failed to find configuration item name")
	ErrUnknownApplicationType = errors.New("unknown application type")
)

func getOwnerFleet(device *api.Device) (string, bool, error) {
	if device.Metadata.Owner == nil {
		return "", true, nil
	}

	ownerType, ownerName, err := util.GetResourceOwner(device.Metadata.Owner)
	if err != nil {
		return "", false, err
	}

	if ownerType != api.FleetKind {
		return "", false, nil
	}

	return ownerName, true, nil
}
