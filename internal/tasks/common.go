package tasks

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
)

const ItemsPerPage = 1000

type ErrUnknownConfigName struct {
	Err error
}

func (e ErrUnknownConfigName) Error() string {
	return fmt.Sprintf("failed to find configuration item name: %v", e.Err)
}

func (e ErrUnknownConfigName) Unwrap() error {
	return e.Err
}

func NewUnknownConfigNameError(err error) error {
	return ErrUnknownConfigName{Err: err}
}

func getOwnerFleet(device *api.Device) (string, bool, error) {
	if device.Metadata.Owner == nil {
		return "", true, nil
	}

	ownerType, ownerName, err := util.GetResourceOwner(device.Metadata.Owner)
	if err != nil {
		return "", false, err
	}

	if ownerType != model.FleetKind {
		return "", false, nil
	}

	return ownerName, true, nil
}
