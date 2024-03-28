package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

// Wait to be notified via channel about fleet template updates, exit upon ctx.Done()
// The logic assumes that we deal with one update at a time. Later, we can increase scale
// by dealing with one update per org at a time.
//
// We have 4 cases:
// 1. Fleet with no overlapping selectors, create/update/delete:
//    Reference kind: Fleet
//    Task description: Iterate devices that match the fleet's selector and set owners/conditions as necessary
// 2. Fleet with overlapping selectors, create/update/delete:
//    Reference kind: Fleet
//    Task description: Iterate all fleets and devices in the org and set owners/conditions as necessary
// 3. Device with a single owner, create/update (no work needed for delete):
//    Reference kind: Device
//    Task description: Iterate fleets and set the device's owner/conditions as necessary
// 4. Device with multiple owners, create/update/delete:
//    Reference kind: Device
//    Task description: Iterate all fleets and devices in the org and set owners/conditions as necessary
//
// In addition, we have the cases where the user deleted all fleets or devices in an org

func FleetSelectorMatching(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelFleetSelectorMatch]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := FleetSelectorMatchingLogic{
				taskManager: taskManager,
				log:         log,
				fleetStore:  taskManager.store.Fleet(),
				devStore:    taskManager.store.Device(),
				resourceRef: resourceRef,
			}

			var err error

			switch {
			case resourceRef.Op == FleetSelectorMatchOpUpdate && resourceRef.Kind == model.FleetKind:
				err = logic.FleetSelectorUpdatedNoOverlapping(ctx)
			case resourceRef.Op == FleetSelectorMatchOpUpdateOverlap && resourceRef.Kind == model.FleetKind:
				err = logic.HandleOrgwideUpdate(ctx)
			case resourceRef.Op == FleetSelectorMatchOpDeleteAll && resourceRef.Kind == model.FleetKind:
				err = logic.HandleDeleteAllFleets(ctx)
			case resourceRef.Op == FleetSelectorMatchOpUpdate && resourceRef.Kind == model.DeviceKind:
				err = logic.CompareFleetsAndSetDeviceOwner(ctx)
			case resourceRef.Op == FleetSelectorMatchOpUpdateOverlap && resourceRef.Kind == model.DeviceKind:
				err = logic.HandleOrgwideUpdate(ctx)
			case resourceRef.Op == FleetSelectorMatchOpDeleteAll && resourceRef.Kind == model.DeviceKind:
				err = logic.HandleDeleteAllDevices(ctx)
			default:
				err = fmt.Errorf("FleetSelectorMatching called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}

			if err != nil {
				taskManager.log.Errorf("failed checking device ownership %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
			}
		}
	}
}

type FleetSelectorMatchingLogic struct {
	taskManager  TaskManager
	log          logrus.FieldLogger
	fleetStore   store.Fleet
	devStore     store.Device
	resourceRef  ResourceReference
	itemsPerPage int
}

func NewFleetSelectorMatchingLogic(taskManager TaskManager, log logrus.FieldLogger, storeInst store.Store, resourceRef ResourceReference) FleetSelectorMatchingLogic {
	return FleetSelectorMatchingLogic{
		taskManager:  taskManager,
		log:          log,
		fleetStore:   storeInst.Fleet(),
		devStore:     storeInst.Device(),
		resourceRef:  resourceRef,
		itemsPerPage: ItemsPerPage,
	}
}

func (f *FleetSelectorMatchingLogic) SetItemsPerPage(items int) {
	f.itemsPerPage = items
}

// Iterate devices that match the fleet's selector and set owners/conditions as necessary
func (f FleetSelectorMatchingLogic) FleetSelectorUpdatedNoOverlapping(ctx context.Context) error {
	f.log.Infof("Checking fleet owner due to fleet selector update %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	fleet, err := f.fleetStore.Get(ctx, f.resourceRef.OrgID, f.resourceRef.Name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return f.removeOwnerFromDevicesOwnedByFleet(ctx)
		}
		return err
	}

	// empty selector matches no devices
	if len(getMatchLabelsSafe(fleet)) == 0 {
		return f.removeOwnerFromDevicesOwnedByFleet(ctx)
	}

	// Disown any devices that the fleet owned but no longer match its selector
	err = f.removeOwnerFromOrphanedDevices(ctx, fleet)
	if err != nil {
		f.log.Errorf("failed disowning orphaned devices: %v", err)
	}

	// List the devices that now match the fleet's selector
	listParams := store.ListParams{
		Labels: getMatchLabelsSafe(fleet),
		Limit:  ItemsPerPage,
	}
	errors := 0

	// overlappingFleets acts as a set of strings
	overlappingFleets := map[string]struct{}{}

	for {
		devices, err := f.devStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			return fmt.Errorf("failed to list devices that no longer belong to fleet: %w", err)
		}

		for devIndex := range devices.Items {
			device := devices.Items[devIndex]

			// If the device didn't have an owner, just set it to this fleet
			if device.Metadata.Owner == nil || *device.Metadata.Owner == "" {
				err := f.updateDeviceOwner(ctx, &device, f.resourceRef.Name)
				if err != nil {
					f.log.Errorf("failed to set owner of device %s/%s to %s: %v", f.resourceRef.OrgID, *device.Metadata.Name, f.resourceRef.Name, err)
					errors++
				}
				continue
			}

			// Get the device's current owner
			ownerType, ownerName, err := util.GetResourceOwner(device.Metadata.Owner)
			if err != nil {
				f.log.Errorf("failed to get owner of device %s/%s: %v", f.resourceRef.OrgID, *device.Metadata.Name, err)
				errors++
				continue
			}
			if ownerType != model.FleetKind {
				continue
			}
			currentOwnerFleetName := ownerName

			// If the device's owner didn't change, continue to the next one
			if currentOwnerFleetName == f.resourceRef.Name {
				continue
			}

			// If the device owner changed, check if the previous owner still matches too
			overlapping, err := f.handleOwningFleetChanged(ctx, &device, fleet, currentOwnerFleetName)
			if err != nil {
				f.log.Errorf("failed to set owner of device %s/%s to %s: %v", f.resourceRef.OrgID, *device.Metadata.Name, f.resourceRef.Name, err)
				errors++
				continue
			}
			if overlapping {
				overlappingFleets[currentOwnerFleetName] = struct{}{}
				overlappingFleets[*fleet.Metadata.Name] = struct{}{}
			}
		}

		if devices.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			if err != nil {
				return fmt.Errorf("failed to parse continuation for paging: %w", err)
			}
			listParams.Continue = cont
		}
	}

	// Set overlapping conditions to true
	overlappingFleetNames := make([]string, 0, len(overlappingFleets))
	for overlappingFleet := range overlappingFleets {
		overlappingFleetNames = append(overlappingFleetNames, overlappingFleet)
	}
	err = f.setOverlappingFleetConditions(ctx, overlappingFleetNames)
	if err != nil {
		return err
	}

	if errors != 0 {
		return fmt.Errorf("failed to process %d devices", errors)
	}

	return nil
}

func (f FleetSelectorMatchingLogic) handleOwningFleetChanged(ctx context.Context, device *api.Device, fleet *api.Fleet, currentOwnerFleetName string) (overlapping bool, err error) {
	// "fleet" is potentially the new owner of "device" because, but we first need
	// to make sure that the label selectors of both the current fleet and the new
	// fleet aren't a match for this device.
	currentOwningFleet, err := f.fleetStore.Get(ctx, f.resourceRef.OrgID, currentOwnerFleetName)
	if err != nil && !errors.Is(err, flterrors.ErrResourceNotFound) {
		return false, err
	}

	newOwnerFleet := *fleet.Metadata.Name
	if currentOwningFleet != nil && !util.LabelsMatchLabelSelector(*device.Metadata.Labels, getMatchLabelsSafe(currentOwningFleet)) {
		return false, f.updateDeviceOwner(ctx, device, newOwnerFleet)
	}

	// The device matches more than one fleet
	annotations := map[string]string{
		model.DeviceAnnotationMultipleOwners: fmt.Sprintf("%s,%s", currentOwnerFleetName, *fleet.Metadata.Name),
	}
	err = f.devStore.UpdateAnnotations(ctx, f.resourceRef.OrgID, *device.Metadata.Name, annotations, nil)
	if err != nil {
		return true, err
	}
	return true, nil
}

func (f FleetSelectorMatchingLogic) removeOwnerFromDevicesOwnedByFleet(ctx context.Context) error {
	// Remove the owner from devices that have this owner
	listParams := store.ListParams{
		Owner: util.SetResourceOwner(model.FleetKind, f.resourceRef.Name),
	}
	return f.removeOwnerFromMatchingDevices(ctx, listParams)
}

func (f FleetSelectorMatchingLogic) removeOwnerFromOrphanedDevices(ctx context.Context, fleet *api.Fleet) error {
	// Remove the owner from devices that don't match the label selector but still have this owner
	listParams := store.ListParams{
		Labels:       getMatchLabelsSafe(fleet),
		InvertLabels: util.BoolToPtr(true),
		Owner:        util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name),
		Limit:        ItemsPerPage,
	}
	return f.removeOwnerFromMatchingDevices(ctx, listParams)
}

func (f FleetSelectorMatchingLogic) removeOwnerFromMatchingDevices(ctx context.Context, listParams store.ListParams) error {
	errors := 0

	for {
		devices, err := f.devStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			return fmt.Errorf("failed to list devices that no longer belong to fleet: %w", err)
		}

		for devIndex := range devices.Items {
			device := devices.Items[devIndex]
			err := f.updateDeviceOwner(ctx, &device, "")
			if err != nil {
				f.log.Errorf("failed to remove owner from device %s/%s: %v", f.resourceRef.OrgID, *device.Metadata.Name, err)
				errors++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			if err != nil {
				return fmt.Errorf("failed to parse continuation for paging: %w", err)
			}
			listParams.Continue = cont
		}
	}

	if errors != 0 {
		return fmt.Errorf("failed to remove owner from %d devices", errors)
	}
	return nil
}

// Iterate fleets and set the device's owner/conditions as necessary
func (f FleetSelectorMatchingLogic) CompareFleetsAndSetDeviceOwner(ctx context.Context) error {
	f.log.Infof("Checking fleet owner due to device label update %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	device, err := f.devStore.Get(ctx, f.resourceRef.OrgID, f.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}

	// Get the current owner and make sure it's a fleet
	currentOwnerFleet, isOwnerAFleet, err := getOwnerFleet(device)
	if err != nil {
		return err
	}
	if !isOwnerAFleet {
		return nil
	}

	// If the device now has no labels, make sure it has no owner
	if device.Metadata.Labels == nil || len(*device.Metadata.Labels) == 0 {
		if len(currentOwnerFleet) != 0 {
			return f.updateDeviceOwner(ctx, device, "")
		}
		return nil
	}

	listParams := store.ListParams{Limit: 0}
	fleets, err := f.fleetStore.List(ctx, f.resourceRef.OrgID, listParams)
	if err != nil {
		return fmt.Errorf("failed fetching fleets: %w", err)
	}

	// Iterate over all fleets and find the (hopefully) one that matches
	matchingFleets, err := f.findDeviceOwnerAmongAllFleets(ctx, device, currentOwnerFleet, fleets)
	if err != nil {
		return fmt.Errorf("failed finding matching fleet: %w", err)
	}

	if len(*matchingFleets) > 1 {
		err = f.setOverlappingFleetConditions(ctx, *matchingFleets)
		if err != nil {
			return err
		}
	}

	return nil
}

// We had overlapping selectors and now need to iterate over all devices in the org to see
// if those overlaps were resolved
func (f FleetSelectorMatchingLogic) HandleOrgwideUpdate(ctx context.Context) error {
	listParams := store.ListParams{Limit: 0}
	fleets, err := f.fleetStore.List(ctx, f.resourceRef.OrgID, listParams)
	if err != nil {
		return fmt.Errorf("failed fetching fleets: %w", err)
	}

	listParams.Limit = ItemsPerPage
	errors := 0
	condErrors := 0

	// overlappingFleets acts as a set of strings
	overlappingFleets := map[string]struct{}{}

	for {
		devices, err := f.devStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			return fmt.Errorf("failed to list devices that no longer belong to fleet: %w", err)
		}

		for devIndex := range devices.Items {
			device := devices.Items[devIndex]

			matchingFleets, err := f.handleDeviceWithPotentialOverlap(ctx, &device, fleets)
			if err != nil {
				f.log.Errorf("failed to get owner for device %s/%s: %v", f.resourceRef.OrgID, *device.Metadata.Name, err)
				errors++
			}

			if matchingFleets != nil && len(*matchingFleets) > 1 {
				for _, matchingFleet := range *matchingFleets {
					overlappingFleets[matchingFleet] = struct{}{}
				}
			}
		}

		if devices.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			if err != nil {
				return fmt.Errorf("failed to parse continuation for paging: %w", err)
			}
			listParams.Continue = cont
		}
	}

	// Set overlapping conditions to true/false
	for fleetIndex := range fleets.Items {
		fleet := fleets.Items[fleetIndex]
		_, ok := overlappingFleets[*fleet.Metadata.Name]
		if ok {
			if api.IsStatusConditionFalse(*fleet.Status.Conditions, api.FleetOverlappingSelectors) {
				condErr := f.setOverlappingFleetConditionTrue(ctx, *fleet.Metadata.Name)
				if condErr != nil {
					f.log.Errorf("failed setting overlapping selector condition on fleet %s/%s: %v", f.resourceRef.OrgID, *fleet.Metadata.Name, err)
					condErrors++
				}
			}
		} else {
			if api.IsStatusConditionTrue(*fleet.Status.Conditions, api.FleetOverlappingSelectors) {
				condErr := f.setOverlappingFleetConditionFalse(ctx, *fleet.Metadata.Name)
				if condErr != nil {
					f.log.Errorf("failed unsetting overlapping selector condition on fleet %s/%s: %v", f.resourceRef.OrgID, *fleet.Metadata.Name, err)
					condErrors++
				}
			}
		}
	}

	if errors != 0 || condErrors != 0 {
		return fmt.Errorf("failed to handle owner of %d devices and set conditions on %d fleets", errors, condErrors)
	}
	return nil
}

func (f FleetSelectorMatchingLogic) handleDeviceWithPotentialOverlap(ctx context.Context, device *api.Device, fleets *api.FleetList) (*[]string, error) {
	currentOwnerFleet, isOwnerAFleet, err := getOwnerFleet(device)
	if err != nil {
		return nil, err
	}
	if !isOwnerAFleet {
		return nil, nil
	}

	// If the device now has no labels, make sure it has no owner and no multiple-owner annotation
	if device.Metadata.Labels == nil || len(*device.Metadata.Labels) == 0 {
		if len(currentOwnerFleet) != 0 {
			err = f.updateDeviceOwner(ctx, device, "")
			if err != nil {
				return nil, err
			}
			if len(GetOverlappingAnnotationValue(device.Metadata.Annotations)) > 0 {
				err = f.devStore.UpdateAnnotations(ctx, f.resourceRef.OrgID, *device.Metadata.Name, nil, []string{model.DeviceAnnotationMultipleOwners})
				if err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	}

	// Iterate over all fleets and find the (hopefully) one that matches
	return f.findDeviceOwnerAmongAllFleets(ctx, device, currentOwnerFleet, fleets)
}

func (f FleetSelectorMatchingLogic) findDeviceOwnerAmongAllFleets(ctx context.Context, device *api.Device, currentOwnerFleet string, fleets *api.FleetList) (*[]string, error) {
	// Find all fleets with a selector that the device matches
	var matchingFleets []string

	for fleetIndex := range fleets.Items {
		fleet := &fleets.Items[fleetIndex]
		if util.LabelsMatchLabelSelector(*device.Metadata.Labels, getMatchLabelsSafe(fleet)) {
			matchingFleets = append(matchingFleets, *fleet.Metadata.Name)
		}
	}
	newAnnotationValue := CreateOverlappingAnnotationValue(matchingFleets)
	currentAnnotationValue := GetOverlappingAnnotationValue(device.Metadata.Annotations)

	err := f.setDeviceOwnerAccordingToMatchingFleets(ctx, device, currentOwnerFleet, matchingFleets)
	if err != nil {
		return nil, err
	}

	if currentAnnotationValue != newAnnotationValue {
		annotations := map[string]string{
			model.DeviceAnnotationMultipleOwners: newAnnotationValue,
		}
		err := f.devStore.UpdateAnnotations(ctx, f.resourceRef.OrgID, *device.Metadata.Name, annotations, nil)
		if err != nil {
			return nil, err
		}
	}

	return &matchingFleets, nil
}

func (f FleetSelectorMatchingLogic) setDeviceOwnerAccordingToMatchingFleets(ctx context.Context, device *api.Device, currentOwnerFleet string, matchingFleets []string) error {
	// Get the new owner fleet (empty if no fleet matched, the name if 1 matched, or error if more than one matched)
	switch len(matchingFleets) {
	case 0:
		if len(currentOwnerFleet) != 0 {
			return f.updateDeviceOwner(ctx, device, "")
		}
		return nil
	case 1:
		// Update the device in the DB only if the owner changed
		newOwnerFleet := matchingFleets[0]
		if currentOwnerFleet != newOwnerFleet {
			return f.updateDeviceOwner(ctx, device, newOwnerFleet)
		}
		return nil
	default:
		// The device matches more than one fleet, set fleet conditions
		return f.setOverlappingFleetConditions(ctx, matchingFleets)
	}
}

func (f FleetSelectorMatchingLogic) HandleDeleteAllDevices(ctx context.Context) error {
	listParams := store.ListParams{Limit: ItemsPerPage}
	errors := 0

	condition := api.Condition{
		Type:   api.FleetOverlappingSelectors,
		Status: api.ConditionStatusFalse,
	}

	for {
		fleets, err := f.fleetStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			return fmt.Errorf("failed fetching fleets: %w", err)
		}

		for fleetIndex := range fleets.Items {
			fleet := fleets.Items[fleetIndex]
			changed := false
			if fleet.Status.Conditions != nil {
				changed = api.SetStatusCondition(fleet.Status.Conditions, condition)
			}
			if changed {
				err = f.fleetStore.UpdateConditions(ctx, f.resourceRef.OrgID, *fleet.Metadata.Name, *fleet.Status.Conditions)
				if err != nil {
					f.log.Errorf("failed to update conditions for fleet %s/%s: %v", f.resourceRef.OrgID, *fleet.Metadata.Name, err)
					errors++
				}
			}
		}

		if fleets.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(fleets.Metadata.Continue)
			if err != nil {
				return fmt.Errorf("failed to parse continuation for paging: %w", err)
			}
			listParams.Continue = cont
		}
	}

	if errors != 0 {
		return fmt.Errorf("failed to remove overlapping conditions of %d fleets", errors)
	}
	return nil
}

func (f FleetSelectorMatchingLogic) HandleDeleteAllFleets(ctx context.Context) error {
	listParams := store.ListParams{Limit: ItemsPerPage}
	errors := 0

	for {
		devices, err := f.devStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			return fmt.Errorf("failed fetching devices: %w", err)
		}

		for devIndex := range devices.Items {
			device := devices.Items[devIndex]
			if device.Metadata.Owner != nil {
				err = f.updateDeviceOwner(ctx, &device, "")
			}
			if err != nil {
				f.log.Errorf("failed updating owner of device %s/%s: %v", f.resourceRef.OrgID, *device.Metadata.Name, err)
				errors++
				continue
			}
			if device.Metadata.Annotations != nil {
				_, ok := (*device.Metadata.Annotations)[model.DeviceAnnotationMultipleOwners]
				if ok {
					err = f.devStore.UpdateAnnotations(ctx, f.resourceRef.OrgID, *device.Metadata.Name, nil, []string{model.DeviceAnnotationMultipleOwners})
					if err != nil {
						f.log.Errorf("failed updating annotations of device %s/%s: %v", f.resourceRef.OrgID, *device.Metadata.Name, err)
						errors++
					}
				}
			}
		}

		if devices.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			if err != nil {
				return fmt.Errorf("failed to parse continuation for paging: %w", err)
			}
			listParams.Continue = cont
		}
	}

	if errors != 0 {
		return fmt.Errorf("failed to remove owners of %d devices", errors)
	}
	return nil
}

// Update a device's owner, which in effect updates the fleet (may require rollout to the device)
func (f FleetSelectorMatchingLogic) updateDeviceOwner(ctx context.Context, device *api.Device, newOwnerFleet string) error {
	fieldsToNil := []string{}
	newOwnerRef := util.SetResourceOwner(model.FleetKind, newOwnerFleet)
	if len(newOwnerFleet) == 0 {
		newOwnerRef = nil
		fieldsToNil = append(fieldsToNil, "owner")
	}

	f.log.Infof("Updating fleet of device %s from %s to %s", *device.Metadata.Name, util.DefaultIfNil(device.Metadata.Owner, "<none>"), util.DefaultIfNil(newOwnerRef, "<none>"))
	device.Metadata.Owner = newOwnerRef
	_, _, err := f.devStore.CreateOrUpdate(ctx, f.resourceRef.OrgID, device, fieldsToNil, false, f.taskManager.DeviceUpdatedCallback)
	return err
}

func (f FleetSelectorMatchingLogic) setOverlappingFleetConditions(ctx context.Context, overlappingFleetNames []string) error {
	if len(overlappingFleetNames) == 0 {
		return f.setOverlappingFleetConditionFalse(ctx, f.resourceRef.Name)
	}

	errors := 0
	for _, overlappingFleet := range overlappingFleetNames {
		err := f.setOverlappingFleetConditionTrue(ctx, overlappingFleet)
		if err != nil {
			f.log.Errorf("failed updating fleet condition: %v", err)
			errors++
		}
	}
	if errors > 0 {
		return fmt.Errorf("failed updating fleet conditions: %d", errors)
	}

	return nil
}

func (f FleetSelectorMatchingLogic) setOverlappingFleetConditionTrue(ctx context.Context, fleetName string) error {
	condition := api.Condition{
		Type:    api.FleetOverlappingSelectors,
		Status:  api.ConditionStatusTrue,
		Reason:  util.StrToPtr("Overlapping selectors"),
		Message: util.StrToPtr("Fleet's selector overlaps with at least one other fleet, causing ambiguous device ownership"),
	}
	return f.fleetStore.UpdateConditions(ctx, f.resourceRef.OrgID, fleetName, []api.Condition{condition})
}

func (f FleetSelectorMatchingLogic) setOverlappingFleetConditionFalse(ctx context.Context, fleetName string) error {
	condition := api.Condition{
		Type:   api.FleetOverlappingSelectors,
		Status: api.ConditionStatusFalse,
	}
	return f.fleetStore.UpdateConditions(ctx, f.resourceRef.OrgID, fleetName, []api.Condition{condition})
}

func GetOverlappingAnnotationValue(annotations *map[string]string) string {
	value := ""
	if annotations == nil {
		return value
	}
	owners, ok := (*annotations)[model.DeviceAnnotationMultipleOwners]
	if ok && len(owners) != 0 {
		value = owners
	}
	return value
}

func CreateOverlappingAnnotationValue(matchingFleets []string) string {
	if len(matchingFleets) > 1 {
		return strings.Join(matchingFleets, ",")
	} else {
		return ""
	}
}

func getMatchLabelsSafe(fleet *api.Fleet) map[string]string {
	if fleet.Spec.Selector != nil {
		return fleet.Spec.Selector.MatchLabels
	}
	return map[string]string{}
}
