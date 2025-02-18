package tasks_client

import (
	"github.com/google/uuid"
)

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
)

type ResourceReference struct {
	TaskName string
	Op       string
	OrgID    uuid.UUID
	Kind     string
	Name     string
	Owner    string
}
