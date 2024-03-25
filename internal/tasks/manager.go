package tasks

import (
	"context"
	"reflect"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks/repotester"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/flightctl/flightctl/pkg/thread"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type ResourceReference struct {
	Op    string
	OrgID uuid.UUID
	Kind  string
	Name  string
	Owner string
}

type TaskManager struct {
	log        logrus.FieldLogger
	ctx        context.Context
	cancelFunc context.CancelFunc
	channels   map[string]chan ResourceReference
	store      store.Store
	threads    []*thread.Thread
	once       *sync.Once
}

const (
	// Task to roll out a fleet's template to its devices upon update
	ChannelFleetRollout = "fleet-rollout"
	// Task to set devices' owners
	ChannelFleetSelectorMatch = "fleet-selector-match"
	// Task to populate a template version
	ChannelTemplateVersionPopulate = "template-version-populate"
	// Task to validate a fleet template
	ChannelFleetValidate = "fleet-template-validate"

	ChannelSize = 20

	FleetRolloutOpUpdate              = "update"
	FleetSelectorMatchOpUpdate        = "update"
	FleetSelectorMatchOpUpdateOverlap = "update-overlap"
	FleetSelectorMatchOpDeleteAll     = "delete-all"
	TemplateVersionPopulateOpCreated  = "create"
	FleetValidateOpUpdate             = "update"
)

func Init(log logrus.FieldLogger, store store.Store) TaskManager {
	ctx := context.Background()
	ctxWithCancel, cancelFunc := context.WithCancel(ctx)

	channels := make(map[string](chan ResourceReference))
	channels[ChannelFleetRollout] = make(chan ResourceReference, ChannelSize)
	channels[ChannelFleetSelectorMatch] = make(chan ResourceReference, ChannelSize)
	channels[ChannelTemplateVersionPopulate] = make(chan ResourceReference, ChannelSize)
	channels[ChannelFleetValidate] = make(chan ResourceReference, ChannelSize)

	reqid.OverridePrefix("tasks")

	return TaskManager{
		log:        log,
		ctx:        ctxWithCancel,
		cancelFunc: cancelFunc,
		channels:   channels,
		store:      store,
		threads:    make([]*thread.Thread, 2),
		once:       new(sync.Once),
	}
}

func (t TaskManager) Start() {
	repoTester := repotester.NewRepoTester(t.log, t.store)
	repoTesterThread := thread.New(
		t.log.WithField("pkg", "repository-tester"), "Repository tester", threadIntervalMinute(2), repoTester.TestRepositories)
	repoTesterThread.Start()

	go FleetRollouts(t)
	go FleetSelectorMatching(t)
	go TemplateVersionPopulate(t)
	go FleetValidate(t)

	resourceSync := NewResourceSync(t)
	resourceSyncThread := thread.New(
		t.log.WithField("pkg", "resourcesync"), "ResourceSync", threadIntervalMinute(2), resourceSync.Poll)
	resourceSyncThread.Start()

	t.threads[0] = repoTesterThread
	t.threads[1] = resourceSyncThread
}

func (t TaskManager) Stop() {
	t.once.Do(func() {
		for _, thread := range t.threads {
			thread.Stop()
		}
		t.cancelFunc()
		for c := range t.channels {
			close(t.channels[c])
		}
	})
}

func (t TaskManager) SubmitTask(taskName string, resource ResourceReference, op string) {
	resource.Op = op
	t.channels[taskName] <- resource
}

func (t TaskManager) GetTask(taskName string) ResourceReference {
	return <-t.channels[taskName]
}

func (t TaskManager) FleetUpdatedCallback(before *model.Fleet, after *model.Fleet) {
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
		t.SubmitTask(ChannelFleetValidate, ref, FleetValidateOpUpdate)
	}
	if selectorUpdated {
		op := FleetSelectorMatchOpUpdate
		if fleet.Status != nil && fleet.Status.Data.Conditions != nil && api.IsStatusConditionTrue(*fleet.Status.Data.Conditions, api.FleetOverlappingSelectors) {
			op = FleetSelectorMatchOpUpdateOverlap
		}
		t.SubmitTask(ChannelFleetSelectorMatch, ref, op)
	}
}

func (t TaskManager) AllFleetsDeletedCallback(orgId uuid.UUID) {
	t.SubmitTask(ChannelFleetSelectorMatch, ResourceReference{OrgID: orgId, Kind: model.FleetKind}, FleetSelectorMatchOpDeleteAll)
}

func (t TaskManager) AllDevicesDeletedCallback(orgId uuid.UUID) {
	t.SubmitTask(ChannelFleetSelectorMatch, ResourceReference{OrgID: orgId, Kind: model.DeviceKind}, FleetSelectorMatchOpDeleteAll)
}

func (t TaskManager) DeviceUpdatedCallback(before *model.Device, after *model.Device) {
	var labelsUpdated bool
	var ownerUpdated bool
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
	} else if after == nil {
		// Deleted device
		device = before
		labelsUpdated = false // Nothing to roll out
		ownerUpdated = true
	} else {
		device = after
		labelsUpdated = !reflect.DeepEqual(before.Labels, after.Labels)
		ownerUpdated = util.DefaultIfNil(before.Owner, "") != util.DefaultIfNil(after.Owner, "")
	}

	ref := ResourceReference{OrgID: device.OrgID, Kind: model.DeviceKind, Name: device.Name}
	if ownerUpdated {
		// If the device's owner was updated, check if we need to update its spec according to its new fleet
		t.SubmitTask(ChannelFleetRollout, ref, FleetRolloutOpUpdate)
	}
	if labelsUpdated {
		// If the label selector was updated, check the devices matching the new one
		op := FleetSelectorMatchOpUpdate
		if len(GetOverlappingAnnotationValue(device.ToApiResource().Metadata.Annotations)) != 0 {
			op = FleetSelectorMatchOpUpdateOverlap
		}
		t.SubmitTask(ChannelFleetSelectorMatch, ref, op)
	}
}

func threadIntervalMinute(min float64) time.Duration {
	return time.Duration(min * float64(time.Minute))
}

func (t TaskManager) TemplateVersionCreatedCallback(templateVersion *model.TemplateVersion) {
	resourceRef := ResourceReference{
		OrgID: templateVersion.OrgID,
		Kind:  model.TemplateVersionKind,
		Name:  templateVersion.Name,
		Owner: *templateVersion.Owner,
	}
	t.SubmitTask(ChannelTemplateVersionPopulate, resourceRef, TemplateVersionPopulateOpCreated)
}

func (t TaskManager) TemplateVersionValidatedCallback(templateVersion *model.TemplateVersion) {
	_, fleetName, err := util.GetResourceOwner(templateVersion.Owner)
	if err != nil {
		t.log.Errorf("failed getting templateVersion owner: %v", err)
		return
	}
	resourceRef := ResourceReference{
		OrgID: templateVersion.OrgID,
		Kind:  model.FleetKind,
		Name:  fleetName,
	}
	t.SubmitTask(ChannelFleetRollout, resourceRef, FleetRolloutOpUpdate)
}
