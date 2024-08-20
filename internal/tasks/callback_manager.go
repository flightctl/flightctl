package tasks

import (
	"encoding/json"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	// Task to roll out a fleet's template to its devices upon update
	FleetRolloutTask = "fleet-rollout"

	// Task to set devices' owners
	FleetSelectorMatchTask = "fleet-selector-match"

	// Task to populate a template version
	TemplateVersionPopulateTask = "template-version-populate"

	// Task to validate a fleet template
	FleetValidateTask = "fleet-template-validate"

	// Task to render device
	DeviceRenderTask = "device-render"

	// Task to re-evaluate fleets and devices if a repository resource changes
	RepositoryUpdatesTask = "repository-updates"
)

type CallbackManager interface {
	FleetUpdatedCallback(before *model.Fleet, after *model.Fleet)
	RepositoryUpdatedCallback(repository *model.Repository)
	AllRepositoriesDeletedCallback(orgId uuid.UUID)
	AllFleetsDeletedCallback(orgId uuid.UUID)
	AllDevicesDeletedCallback(orgId uuid.UUID)
	DeviceUpdatedCallback(before *model.Device, after *model.Device)
	TemplateVersionCreatedCallback(templateVersion *model.TemplateVersion)
	TemplateVersionValidatedCallback(templateVersion *model.TemplateVersion)
	FleetSourceUpdated(orgId uuid.UUID, name string)
	DeviceSourceUpdated(orgId uuid.UUID, name string)
}

type callbackManager struct {
	publisher queues.Publisher
	log       logrus.FieldLogger
}

func TaskQueuePublisher(provider queues.Provider) (queues.Publisher, error) {
	publisher, err := provider.NewPublisher(TaskQueue)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}
	return publisher, nil
}

func NewCallbackManager(publisher queues.Publisher, log logrus.FieldLogger) CallbackManager {
	return &callbackManager{
		publisher: publisher,
		log:       log,
	}
}

func (t *callbackManager) submitTask(taskName string, resource ResourceReference, op string) {
	resource.TaskName = taskName
	resource.Op = op
	b, err := json.Marshal(&resource)
	if err != nil {
		t.log.WithError(err).Error("failed to marshal payload")
		return
	}
	if err = t.publisher.Publish(b); err != nil {
		t.log.WithError(err).Error("failed to publish resource")
	}
}

func (t *callbackManager) FleetUpdatedCallback(before *model.Fleet, after *model.Fleet) {
	var templateUpdated bool
	var selectorUpdated bool
	var fleet *model.Fleet

	if before == nil && after == nil {
		// Shouldn't be here, return
		return
	}
	if before == nil {
		// New fleet
		fleet = after
		templateUpdated = true
		selectorUpdated = true
	} else if after == nil {
		// Deleted fleet
		fleet = before
		templateUpdated = false
		selectorUpdated = true
	} else {
		fleet = after
		templateUpdated = !reflect.DeepEqual(before.Spec.Data.Template.Spec, after.Spec.Data.Template.Spec)
		selectorUpdated = !reflect.DeepEqual(before.Spec.Data.Selector, after.Spec.Data.Selector)
	}

	ref := ResourceReference{OrgID: fleet.OrgID, Kind: model.FleetKind, Name: fleet.Name}
	if templateUpdated {
		// If the template was updated, start rolling out the new spec
		t.submitTask(FleetValidateTask, ref, FleetValidateOpUpdate)
	}
	if selectorUpdated {
		op := FleetSelectorMatchOpUpdate
		if fleet.Status != nil && fleet.Status.Data.Conditions != nil && api.IsStatusConditionTrue(fleet.Status.Data.Conditions, api.FleetOverlappingSelectors) {
			op = FleetSelectorMatchOpUpdateOverlap
		}
		t.submitTask(FleetSelectorMatchTask, ref, op)
	}
}

func (t *callbackManager) FleetSourceUpdated(orgId uuid.UUID, name string) {
	ref := ResourceReference{OrgID: orgId, Kind: model.FleetKind, Name: name}
	t.submitTask(FleetValidateTask, ref, FleetValidateOpUpdate)
}

func (t *callbackManager) RepositoryUpdatedCallback(repository *model.Repository) {
	resourceRef := ResourceReference{
		OrgID: repository.OrgID,
		Kind:  model.RepositoryKind,
		Name:  repository.Name,
	}
	t.submitTask(RepositoryUpdatesTask, resourceRef, RepositoryUpdateOpUpdate)
}

func (t *callbackManager) AllRepositoriesDeletedCallback(orgId uuid.UUID) {
	t.submitTask(RepositoryUpdatesTask, ResourceReference{OrgID: orgId, Kind: model.RepositoryKind}, RepositoryUpdateOpDeleteAll)
}

func (t *callbackManager) AllFleetsDeletedCallback(orgId uuid.UUID) {
	t.submitTask(FleetSelectorMatchTask, ResourceReference{OrgID: orgId, Kind: model.FleetKind}, FleetSelectorMatchOpDeleteAll)
}

func (t *callbackManager) AllDevicesDeletedCallback(orgId uuid.UUID) {
	t.submitTask(FleetSelectorMatchTask, ResourceReference{OrgID: orgId, Kind: model.DeviceKind}, FleetSelectorMatchOpDeleteAll)
}

func (t *callbackManager) DeviceUpdatedCallback(before *model.Device, after *model.Device) {
	var labelsUpdated bool
	var ownerUpdated bool
	var specUpdated bool
	var device *model.Device

	if before == nil && after == nil {
		// Shouldn't be here, return
		return
	}
	if before == nil {
		// New device
		device = after
		if len(after.Resource.Labels) != 0 {
			labelsUpdated = true
		}
		ownerUpdated = false
		specUpdated = true
	} else if after == nil {
		// Deleted device
		device = before
		labelsUpdated = true
		ownerUpdated = false // Nothing to roll out
		specUpdated = false  // Nothing to render
	} else {
		device = after
		labelsUpdated = !reflect.DeepEqual(before.Labels, after.Labels)
		ownerUpdated = util.DefaultIfNil(before.Owner, "") != util.DefaultIfNil(after.Owner, "")
		specUpdated = !reflect.DeepEqual(before.Spec, after.Spec)
	}

	ref := ResourceReference{OrgID: device.OrgID, Kind: model.DeviceKind, Name: device.Name}
	if ownerUpdated || labelsUpdated {
		// If the device's owner was updated, or if labels were updating that might affect parametrers,
		// check if we need to update its spec according to its new fleet
		t.submitTask(FleetRolloutTask, ref, FleetRolloutOpUpdate)
	}
	if labelsUpdated {
		// Check if the new labels cause the device to move to a different fleet
		op := FleetSelectorMatchOpUpdate

		if api.IsStatusConditionTrue(device.Status.Data.Conditions, api.DeviceMultipleOwners) {
			op = FleetSelectorMatchOpUpdateOverlap
		}
		t.submitTask(FleetSelectorMatchTask, ref, op)
	}
	if specUpdated {
		t.submitTask(DeviceRenderTask, ref, DeviceRenderOpUpdate)
	}
}

func (t *callbackManager) DeviceSourceUpdated(orgId uuid.UUID, name string) {
	ref := ResourceReference{OrgID: orgId, Kind: model.DeviceKind, Name: name}
	t.submitTask(DeviceRenderTask, ref, DeviceRenderOpUpdate)
}

func (t *callbackManager) TemplateVersionCreatedCallback(templateVersion *model.TemplateVersion) {
	resourceRef := ResourceReference{
		OrgID: templateVersion.OrgID,
		Kind:  model.TemplateVersionKind,
		Name:  templateVersion.Name,
		Owner: *util.SetResourceOwner(model.FleetKind, templateVersion.FleetName),
	}
	t.submitTask(TemplateVersionPopulateTask, resourceRef, TemplateVersionPopulateOpCreated)
}

func (t *callbackManager) TemplateVersionValidatedCallback(templateVersion *model.TemplateVersion) {
	resourceRef := ResourceReference{
		OrgID: templateVersion.OrgID,
		Kind:  model.FleetKind,
		Name:  templateVersion.FleetName,
	}
	t.submitTask(FleetRolloutTask, resourceRef, FleetRolloutOpUpdate)
}
