package tasks

import (
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const ItemsPerPage = 1000

const (
	FleetRolloutOpUpdate              = "update"
	FleetSelectorMatchOpUpdate        = "update"
	FleetSelectorMatchOpUpdateOverlap = "update-overlap"
	FleetSelectorMatchOpDeleteAll     = "delete-all"
	TemplateVersionPopulateOpCreated  = "create"
	FleetValidateOpUpdate             = "update"
	DeviceRenderOpUpdate              = "update"
	RepositoryUpdateOpUpdate          = "update"
	RepositoryUpdateOpDeleteAll       = "delete-all"
	AsyncSignOpSignAll                = "sign-all"
	AsyncSignOpSignCSR                = "sign-csrs"
	AsyncSignOpSignEnrollment         = "sign-enrollment"
)

type ResourceReference struct {
	TaskName string
	Op       string
	OrgID    uuid.UUID
	Kind     string
	Name     string
	Owner    string
}

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
