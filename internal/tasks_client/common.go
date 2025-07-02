package tasks_client

import (
	"github.com/google/uuid"
)

const (
	FleetRolloutOpUpdate              = "update"
	FleetSelectorMatchOpUpdate        = "update"
	FleetSelectorMatchOpUpdateOverlap = "update-overlap"
	TemplateVersionPopulateOpCreated  = "create"
	FleetValidateOpUpdate             = "update"
	DeviceRenderOpUpdate              = "update"
	RepositoryUpdateOpUpdate          = "update"
)

type ResourceReference struct {
	TaskName string
	Op       string
	OrgID    uuid.UUID
	Kind     string
	Name     string
	Owner    string
}
