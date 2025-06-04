package tasks_client

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	// Task to roll out a fleet's template to its devices upon update
	FleetRolloutTask = "fleet-rollout"

	// Task to set devices' owners
	FleetSelectorMatchTask = "fleet-selector-match"

	// Task to validate a fleet template
	FleetValidateTask = "fleet-template-validate"

	// Task to render device
	DeviceRenderTask = "device-render"

	// Task to re-evaluate fleets and devices if a repository resource changes
	RepositoryUpdatesTask = "repository-updates"
)

type CallbackManager interface {
	DeviceUpdatedCallback(ctx context.Context, orgId uuid.UUID, before, after *api.Device)
	DeviceUpdatedNoRenderCallback(ctx context.Context, orgId uuid.UUID, before, after *api.Device)
	FleetUpdatedCallback(ctx context.Context, orgId uuid.UUID, before, after *api.Fleet)
	RepositoryUpdatedCallback(ctx context.Context, orgId uuid.UUID, before, after *api.Repository)
	TemplateVersionCreatedCallback(ctx context.Context, orgId uuid.UUID, before, after *api.TemplateVersion)
	AllRepositoriesDeletedCallback(ctx context.Context, orgId uuid.UUID)
	AllFleetsDeletedCallback(ctx context.Context, orgId uuid.UUID)
	AllDevicesDeletedCallback(ctx context.Context, orgId uuid.UUID)
	FleetSourceUpdated(ctx context.Context, orgId uuid.UUID, name string)
	DeviceSourceUpdated(ctx context.Context, orgId uuid.UUID, name string)
	FleetRolloutSelectionUpdated(ctx context.Context, orgId uuid.UUID, name string)
}

type callbackManager struct {
	publisher queues.Publisher
	log       logrus.FieldLogger
}

func TaskQueuePublisher(queuesProvider queues.Provider) (queues.Publisher, error) {
	publisher, err := queuesProvider.NewPublisher(consts.TaskQueue)
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

func (t *callbackManager) submitTask(ctx context.Context, taskName string, resource ResourceReference, op string) {
	resource.TaskName = taskName
	resource.Op = op
	b, err := json.Marshal(&resource)
	if err != nil {
		t.log.WithError(err).Error("failed to marshal payload")
		return
	}
	if err = t.publisher.Publish(ctx, b); err != nil {
		t.log.WithError(err).Error("failed to publish resource")
	}
}

func (t *callbackManager) FleetUpdatedCallback(ctx context.Context, orgId uuid.UUID, before, after *api.Fleet) {
	var templateUpdated bool
	var selectorUpdated bool
	var fleet *api.Fleet

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
		templateUpdated = !api.DeviceSpecsAreEqual(before.Spec.Template.Spec, after.Spec.Template.Spec)
		selectorUpdated = !reflect.DeepEqual(before.Spec.Selector, after.Spec.Selector)
	}

	ref := ResourceReference{OrgID: orgId, Kind: api.FleetKind, Name: *fleet.Metadata.Name}
	if templateUpdated {
		// If the template was updated, start rolling out the new spec
		t.submitTask(ctx, FleetValidateTask, ref, FleetValidateOpUpdate)
	}
	if selectorUpdated {
		op := FleetSelectorMatchOpUpdate
		if fleet.Status != nil && fleet.Status.Conditions != nil && api.IsStatusConditionTrue(fleet.Status.Conditions, api.FleetOverlappingSelectors) {
			op = FleetSelectorMatchOpUpdateOverlap
		}
		t.submitTask(ctx, FleetSelectorMatchTask, ref, op)
	}
}

func (t *callbackManager) FleetSourceUpdated(ctx context.Context, orgId uuid.UUID, name string) {
	ref := ResourceReference{OrgID: orgId, Kind: api.FleetKind, Name: name}
	t.submitTask(ctx, FleetValidateTask, ref, FleetValidateOpUpdate)
}

func (t *callbackManager) RepositoryUpdatedCallback(ctx context.Context, orgId uuid.UUID, before, after *api.Repository) {
	var repository *api.Repository
	if before != nil {
		repository = before
	} else {
		repository = after
	}
	if repository == nil {
		return
	}
	resourceRef := ResourceReference{
		OrgID: orgId,
		Kind:  api.RepositoryKind,
		Name:  *repository.Metadata.Name,
	}
	t.submitTask(ctx, RepositoryUpdatesTask, resourceRef, RepositoryUpdateOpUpdate)
}

func (t *callbackManager) AllRepositoriesDeletedCallback(ctx context.Context, orgId uuid.UUID) {
	t.submitTask(ctx, RepositoryUpdatesTask, ResourceReference{OrgID: orgId, Kind: api.RepositoryKind}, RepositoryUpdateOpDeleteAll)
}

func (t *callbackManager) AllFleetsDeletedCallback(ctx context.Context, orgId uuid.UUID) {
	t.submitTask(ctx, FleetSelectorMatchTask, ResourceReference{OrgID: orgId, Kind: api.FleetKind}, FleetSelectorMatchOpDeleteAll)
}

func (t *callbackManager) AllDevicesDeletedCallback(ctx context.Context, orgId uuid.UUID) {
	t.submitTask(ctx, FleetSelectorMatchTask, ResourceReference{OrgID: orgId, Kind: api.DeviceKind}, FleetSelectorMatchOpDeleteAll)
}

func (t *callbackManager) DeviceUpdatedNoRenderCallback(ctx context.Context, orgId uuid.UUID, before *api.Device, after *api.Device) {
	var labelsUpdated bool
	var ownerUpdated bool
	var device *api.Device

	if before == nil && after == nil {
		// Shouldn't be here, return
		return
	}
	if before == nil {
		// New device
		device = after
		if len(*after.Metadata.Labels) != 0 {
			labelsUpdated = true
		}
		ownerUpdated = false
	} else if after == nil {
		// Deleted device
		device = before
		labelsUpdated = true
		ownerUpdated = false // Nothing to roll out
	} else {
		device = after
		labelsUpdated = !reflect.DeepEqual(*before.Metadata.Labels, *after.Metadata.Labels)
		ownerUpdated = util.DefaultIfNil(before.Metadata.Owner, "") != util.DefaultIfNil(after.Metadata.Owner, "")
	}

	ref := ResourceReference{OrgID: orgId, Kind: api.DeviceKind, Name: *device.Metadata.Name}
	if ownerUpdated || labelsUpdated {
		// If the device's owner was updated, or if labels were updating that might affect parametrers,
		// check if we need to update its spec according to its new fleet
		t.submitTask(ctx, FleetRolloutTask, ref, FleetRolloutOpUpdate)
	}
	if labelsUpdated {
		// Check if the new labels cause the device to move to a different fleet
		op := FleetSelectorMatchOpUpdate

		if api.IsStatusConditionTrue(device.Status.Conditions, api.DeviceMultipleOwners) {
			op = FleetSelectorMatchOpUpdateOverlap
		}
		t.submitTask(ctx, FleetSelectorMatchTask, ref, op)
	}

}

func (t *callbackManager) DeviceUpdatedCallback(ctx context.Context, orgId uuid.UUID, before *api.Device, after *api.Device) {
	t.DeviceUpdatedNoRenderCallback(ctx, orgId, before, after)
	if after != nil && (before == nil || !api.DeviceSpecsAreEqual(*before.Spec, *after.Spec)) {
		t.DeviceSourceUpdated(ctx, orgId, lo.FromPtr(after.Metadata.Name))
	}
}

func (t *callbackManager) DeviceSourceUpdated(ctx context.Context, orgId uuid.UUID, name string) {
	ref := ResourceReference{OrgID: orgId, Kind: api.DeviceKind, Name: name}
	t.submitTask(ctx, DeviceRenderTask, ref, DeviceRenderOpUpdate)
}

// This is called only upon create, so "before" should be nil and "after" should be the TV
func (t *callbackManager) TemplateVersionCreatedCallback(ctx context.Context, orgId uuid.UUID, before, after *api.TemplateVersion) {
	templateVersion := after
	resourceRef := ResourceReference{
		OrgID: orgId,
		Kind:  api.FleetKind,
		Name:  templateVersion.Spec.Fleet,
	}
	t.submitTask(ctx, FleetRolloutTask, resourceRef, FleetRolloutOpUpdate)
}

func (t *callbackManager) FleetRolloutSelectionUpdated(ctx context.Context, orgId uuid.UUID, name string) {
	resourceRef := ResourceReference{
		OrgID: orgId,
		Kind:  api.FleetKind,
		Name:  name,
	}
	t.submitTask(ctx, FleetRolloutTask, resourceRef, FleetRolloutOpUpdate)
}
