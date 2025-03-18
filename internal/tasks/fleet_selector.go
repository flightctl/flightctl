package tasks

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
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

func fleetSelectorMatching(ctx context.Context, resourceRef *tasks_client.ResourceReference, serviceHandler *service.ServiceHandler, callbackManager tasks_client.CallbackManager, log logrus.FieldLogger) error {
	logic := FleetSelectorMatchingLogic{
		callbackManager: callbackManager,
		log:             log,
		serviceHandler:  serviceHandler,
		resourceRef:     *resourceRef,
	}

	var err error

	switch {
	case resourceRef.Op == tasks_client.FleetSelectorMatchOpUpdate && resourceRef.Kind == api.FleetKind:
		err = logic.FleetSelectorUpdatedNoOverlapping(ctx)
	case resourceRef.Op == tasks_client.FleetSelectorMatchOpUpdateOverlap && resourceRef.Kind == api.FleetKind:
		err = logic.HandleOrgwideUpdate(ctx)
	case resourceRef.Op == tasks_client.FleetSelectorMatchOpDeleteAll && resourceRef.Kind == api.FleetKind:
		err = logic.HandleDeleteAllFleets(ctx)
	case resourceRef.Op == tasks_client.FleetSelectorMatchOpUpdate && resourceRef.Kind == api.DeviceKind:
		err = logic.CompareFleetsAndSetDeviceOwner(ctx)
	case resourceRef.Op == tasks_client.FleetSelectorMatchOpUpdateOverlap && resourceRef.Kind == api.DeviceKind:
		err = logic.HandleOrgwideUpdate(ctx)
	case resourceRef.Op == tasks_client.FleetSelectorMatchOpDeleteAll && resourceRef.Kind == api.DeviceKind:
		err = logic.HandleDeleteAllDevices(ctx)
	default:
		err = fmt.Errorf("FleetSelectorMatching called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
	}

	if err != nil {
		log.Errorf("failed checking device ownership %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
	}
	return err
}

type FleetSelectorMatchingLogic struct {
	callbackManager tasks_client.CallbackManager
	log             logrus.FieldLogger
	serviceHandler  *service.ServiceHandler
	resourceRef     tasks_client.ResourceReference
	itemsPerPage    int
}

func NewFleetSelectorMatchingLogic(callbackManager tasks_client.CallbackManager, log logrus.FieldLogger, serviceHandler *service.ServiceHandler, resourceRef tasks_client.ResourceReference) FleetSelectorMatchingLogic {
	return FleetSelectorMatchingLogic{
		callbackManager: callbackManager,
		log:             log,
		serviceHandler:  serviceHandler,
		resourceRef:     resourceRef,
		itemsPerPage:    ItemsPerPage,
	}
}

func (f *FleetSelectorMatchingLogic) SetItemsPerPage(items int) {
	f.itemsPerPage = items
}

// Iterate devices that match the fleet's selector and set owners/conditions as necessary
func (f FleetSelectorMatchingLogic) FleetSelectorUpdatedNoOverlapping(ctx context.Context) error {
	f.log.Infof("Checking fleet owner due to fleet selector update %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	fleet, status := f.serviceHandler.GetFleet(ctx, f.resourceRef.Name, api.GetFleetParams{})
	if status.Code != http.StatusOK {
		if status.Code == http.StatusNotFound {
			return f.removeOwnerFromDevicesOwnedByFleet(ctx)
		}
		return service.ApiStatusToErr(status)
	}

	// empty selector matches no devices
	if len(getMatchLabelsSafe(fleet)) == 0 {
		return f.removeOwnerFromDevicesOwnedByFleet(ctx)
	}

	// Disown any devices that the fleet owned but no longer match its selector
	err := f.removeOwnerFromOrphanedDevices(ctx, fleet)
	if err != nil {
		f.log.Errorf("failed disowning orphaned devices: %v", err)
	}

	// List the devices that now match the fleet's selector
	listParams := api.ListDevicesParams{
		LabelSelector: labelSelectorFromLabelMap(getMatchLabelsSafe(fleet)),
		Limit:         lo.ToPtr(int32(ItemsPerPage)),
	}
	errors := 0

	// overlappingFleets acts as a set of strings
	overlappingFleets := map[string]struct{}{}

	for {
		devices, status := f.serviceHandler.ListDevices(ctx, listParams, nil)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed to list devices that no longer belong to fleet: %s", status.Message)
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
			if ownerType != api.FleetKind {
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
		}
		listParams.Continue = devices.Metadata.Continue
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
	currentOwningFleet, status := f.serviceHandler.GetFleet(ctx, currentOwnerFleetName, api.GetFleetParams{})
	if status.Code != http.StatusOK && status.Code != http.StatusNotFound {
		return false, service.ApiStatusToErr(status)
	}

	newOwnerFleet := *fleet.Metadata.Name
	if currentOwningFleet != nil && !util.LabelsMatchLabelSelector(*device.Metadata.Labels, getMatchLabelsSafe(currentOwningFleet)) {
		return false, f.updateDeviceOwner(ctx, device, newOwnerFleet)
	}

	// The device matches more than one fleet
	condition := api.Condition{
		Type:    api.ConditionTypeDeviceMultipleOwners,
		Status:  api.ConditionStatusTrue,
		Reason:  "MultipleOwners",
		Message: fmt.Sprintf("%s,%s", currentOwnerFleetName, *fleet.Metadata.Name),
	}
	status = f.serviceHandler.SetDeviceServiceConditions(ctx, *device.Metadata.Name, []api.Condition{condition})
	if status.Code != http.StatusOK {
		return true, service.ApiStatusToErr(status)
	}
	return true, nil
}

func (f FleetSelectorMatchingLogic) removeOwnerFromDevicesOwnedByFleet(ctx context.Context) error {
	// Remove the owner from devices that have this owner
	listParams := api.ListDevicesParams{
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *util.SetResourceOwner(api.FleetKind, f.resourceRef.Name))),
	}
	return f.removeOwnerFromMatchingDevices(ctx, listParams)
}

func (f FleetSelectorMatchingLogic) removeOwnerFromOrphanedDevices(ctx context.Context, fleet *api.Fleet) error {
	// Create a new LabelSelector from the fleet's match labels.
	labelsMap := getMatchLabelsSafe(fleet)

	// Build the keyset-based selector string
	var keys, values []string
	for k, v := range labelsMap {
		keys = append(keys, k)
		values = append(values, v)
	}

	// Construct the selector string using the keyset.
	// This selector matches objects whose labels do not match the specified key-value pairs as a whole.
	// For example, the selector "(k1,k2) != (v1,v2)" matches objects that do not have both k1=v1 and k2=v2 together.
	ls := fmt.Sprintf("(%s) != (%s)", strings.Join(keys, ","), strings.Join(values, ","))

	// Remove the owner from devices that don't match the label selector but still have this owner
	listParams := api.ListDevicesParams{
		Limit:         lo.ToPtr(int32(ItemsPerPage)),
		LabelSelector: lo.ToPtr(ls),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *util.SetResourceOwner(api.FleetKind, *fleet.Metadata.Name))),
	}

	return f.removeOwnerFromMatchingDevices(ctx, listParams)
}

func (f FleetSelectorMatchingLogic) removeOwnerFromMatchingDevices(ctx context.Context, listParams api.ListDevicesParams) error {
	errors := 0

	for {
		devices, status := f.serviceHandler.ListDevices(ctx, listParams, nil)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed to list devices that no longer belong to fleet: %s", status.Message)
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
		}
		listParams.Continue = devices.Metadata.Continue
	}

	if errors != 0 {
		return fmt.Errorf("failed to remove owner from %d devices", errors)
	}
	return nil
}

// Iterate fleets and set the device's owner/conditions as necessary
func (f FleetSelectorMatchingLogic) CompareFleetsAndSetDeviceOwner(ctx context.Context) error {
	f.log.Infof("Checking fleet owner due to device label update %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	device, status := f.serviceHandler.GetDevice(ctx, f.resourceRef.Name)
	if status.Code != http.StatusOK {
		if status.Code == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("failed to get device: %s", status.Message)
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

	var fleets []api.Fleet
	fleetListParams := api.ListFleetsParams{Limit: lo.ToPtr(int32(ItemsPerPage))}
	for {
		fleetBatch, status := f.serviceHandler.ListFleets(ctx, fleetListParams)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed fetching fleets: %s", status.Message)
		}

		fleets = append(fleets, fleetBatch.Items...)
		if fleetBatch.Metadata.Continue == nil {
			break
		}
		fleetListParams.Continue = fleetBatch.Metadata.Continue
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
	errors := 0
	condErrors := 0

	// overlappingFleets acts as a set of strings
	overlappingFleets := map[string]struct{}{}

	var fleets []api.Fleet
	fleetListParams := api.ListFleetsParams{Limit: lo.ToPtr(int32(ItemsPerPage))}
	for {
		fleetBatch, status := f.serviceHandler.ListFleets(ctx, fleetListParams)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed fetching fleets: %s", status.Message)
		}

		fleets = append(fleets, fleetBatch.Items...)
		if fleetBatch.Metadata.Continue == nil {
			break
		}
		fleetListParams.Continue = fleetBatch.Metadata.Continue
	}

	devListParams := api.ListDevicesParams{Limit: lo.ToPtr(int32(ItemsPerPage))}
	for {
		devices, status := f.serviceHandler.ListDevices(ctx, devListParams, nil)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed to list devices that no longer belong to fleet: %s", status.Message)
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
		}
		devListParams.Continue = devices.Metadata.Continue
	}

	// Set overlapping conditions to true/false
	for fleetIndex := range fleets {
		fleet := fleets[fleetIndex]
		_, ok := overlappingFleets[*fleet.Metadata.Name]
		if ok {
			if api.IsStatusConditionFalse(fleet.Status.Conditions, api.ConditionTypeFleetOverlappingSelectors) {
				condErr := f.setOverlappingFleetConditionTrue(ctx, *fleet.Metadata.Name)
				if condErr != nil {
					f.log.Errorf("failed setting overlapping selector condition on fleet %s/%s: %v", f.resourceRef.OrgID, *fleet.Metadata.Name, condErr)
					condErrors++
				}
			}
		} else {
			if api.IsStatusConditionTrue(fleet.Status.Conditions, api.ConditionTypeFleetOverlappingSelectors) {
				condErr := f.setOverlappingFleetConditionFalse(ctx, *fleet.Metadata.Name)
				if condErr != nil {
					f.log.Errorf("failed unsetting overlapping selector condition on fleet %s/%s: %v", f.resourceRef.OrgID, *fleet.Metadata.Name, condErr)
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

func (f FleetSelectorMatchingLogic) handleDeviceWithPotentialOverlap(ctx context.Context, device *api.Device, fleets []api.Fleet) (*[]string, error) {
	currentOwnerFleet, isOwnerAFleet, err := getOwnerFleet(device)
	if err != nil {
		return nil, err
	}
	if !isOwnerAFleet {
		return nil, nil
	}

	// If the device now has no labels, make sure it has no owner and no multiple-owner condition
	if device.Metadata.Labels == nil || len(*device.Metadata.Labels) == 0 {
		if len(currentOwnerFleet) != 0 {
			err = f.updateDeviceOwner(ctx, device, "")
			if err != nil {
				return nil, err
			}

			condition := api.Condition{
				Type:   api.ConditionTypeDeviceMultipleOwners,
				Status: api.ConditionStatusFalse,
			}
			status := f.serviceHandler.SetDeviceServiceConditions(ctx, *device.Metadata.Name, []api.Condition{condition})
			if status.Code != http.StatusOK {
				return nil, service.ApiStatusToErr(status)
			}
		}
		return nil, nil
	}

	// Iterate over all fleets and find the (hopefully) one that matches
	return f.findDeviceOwnerAmongAllFleets(ctx, device, currentOwnerFleet, fleets)
}

func (f FleetSelectorMatchingLogic) findDeviceOwnerAmongAllFleets(ctx context.Context, device *api.Device, currentOwnerFleet string, fleets []api.Fleet) (*[]string, error) {
	// Find all fleets with a selector that the device matches
	var matchingFleets []string

	for fleetIndex := range fleets {
		fleet := &fleets[fleetIndex]
		if util.LabelsMatchLabelSelector(*device.Metadata.Labels, getMatchLabelsSafe(fleet)) {
			matchingFleets = append(matchingFleets, *fleet.Metadata.Name)
		}
	}
	newConditionMessage := createOverlappingConditionMessage(matchingFleets)
	currentConditionMessage := ""
	condition := api.FindStatusCondition(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)
	if condition != nil {
		currentConditionMessage = condition.Message
	}

	err := f.setDeviceOwnerAccordingToMatchingFleets(ctx, device, currentOwnerFleet, matchingFleets)
	if err != nil {
		return nil, err
	}

	if currentConditionMessage != newConditionMessage {
		condition := api.Condition{Type: api.ConditionTypeDeviceMultipleOwners}
		if len(matchingFleets) > 1 {
			condition.Status = api.ConditionStatusTrue
			condition.Reason = "MultipleOwners"
			condition.Message = newConditionMessage
		} else {
			condition.Status = api.ConditionStatusFalse
		}

		status := f.serviceHandler.SetDeviceServiceConditions(ctx, *device.Metadata.Name, []api.Condition{condition})
		if status.Code != http.StatusOK {
			return nil, service.ApiStatusToErr(status)
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
	listParams := api.ListFleetsParams{Limit: lo.ToPtr(int32(ItemsPerPage))}
	errors := 0

	condition := api.Condition{
		Type:   api.ConditionTypeFleetOverlappingSelectors,
		Status: api.ConditionStatusFalse,
	}

	for {
		fleets, status := f.serviceHandler.ListFleets(ctx, listParams)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed fetching fleets: %s", status.Message)
		}

		for fleetIndex := range fleets.Items {
			fleet := fleets.Items[fleetIndex]
			changed := false
			if fleet.Status.Conditions != nil {
				changed = api.SetStatusCondition(&fleet.Status.Conditions, condition)
			}
			if changed {
				status = f.serviceHandler.UpdateFleetConditions(ctx, *fleet.Metadata.Name, fleet.Status.Conditions)
				if status.Code != http.StatusOK {
					f.log.Errorf("failed to update conditions for fleet %s/%s: %s", f.resourceRef.OrgID, *fleet.Metadata.Name, status.Message)
					errors++
				}
			}
		}

		if fleets.Metadata.Continue == nil {
			break
		}
		listParams.Continue = fleets.Metadata.Continue
	}

	if errors != 0 {
		return fmt.Errorf("failed to remove overlapping conditions of %d fleets", errors)
	}
	return nil
}

func (f FleetSelectorMatchingLogic) HandleDeleteAllFleets(ctx context.Context) error {
	listParams := api.ListDevicesParams{Limit: lo.ToPtr(int32(ItemsPerPage))}
	errors := 0

	for {
		devices, status := f.serviceHandler.ListDevices(ctx, listParams, nil)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed fetching devices: %s", status.Message)
		}

		for devIndex := range devices.Items {
			device := devices.Items[devIndex]
			if device.Metadata.Owner != nil {
				err := f.updateDeviceOwner(ctx, &device, "")
				if err != nil {
					f.log.Errorf("failed updating owner of device %s/%s: %v", f.resourceRef.OrgID, *device.Metadata.Name, err)
					errors++
					continue
				}
			}
			if api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners) {
				condition := api.Condition{Type: api.ConditionTypeDeviceMultipleOwners, Status: api.ConditionStatusFalse}
				status = f.serviceHandler.SetDeviceServiceConditions(ctx, *device.Metadata.Name, []api.Condition{condition})
				if status.Code != http.StatusOK {
					f.log.Errorf("failed updating conditions of device %s/%s: %s", f.resourceRef.OrgID, *device.Metadata.Name, status.Message)
					errors++
				}
			}
		}

		if devices.Metadata.Continue == nil {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}

	if errors != 0 {
		return fmt.Errorf("failed to remove owners of %d devices", errors)
	}
	return nil
}

// Update a device's owner, which in effect updates the fleet (may require rollout to the device)
func (f FleetSelectorMatchingLogic) updateDeviceOwner(ctx context.Context, device *api.Device, newOwnerFleet string) error {
	// do not update decommissioning devices
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		f.log.Debugf("SKipping update of device owner for decommissioned device: %s", device.Metadata.Name)
		return nil
	}

	fieldsToNil := []string{}
	newOwnerRef := util.SetResourceOwner(api.FleetKind, newOwnerFleet)
	if len(newOwnerFleet) == 0 {
		newOwnerRef = nil
		fieldsToNil = append(fieldsToNil, "owner")
	}

	f.log.Infof("Updating fleet of device %s from %s to %s", *device.Metadata.Name, util.DefaultIfNil(device.Metadata.Owner, "<none>"), util.DefaultIfNil(newOwnerRef, "<none>"))
	device.Metadata.Owner = newOwnerRef
	_, status := f.serviceHandler.ReplaceDevice(ctx, *device.Metadata.Name, lo.FromPtr(device), fieldsToNil)
	return service.ApiStatusToErr(status)
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
		Type:    api.ConditionTypeFleetOverlappingSelectors,
		Status:  api.ConditionStatusTrue,
		Reason:  "Overlapping selectors",
		Message: "Fleet's selector overlaps with at least one other fleet, causing ambiguous device ownership",
	}
	status := f.serviceHandler.UpdateFleetConditions(ctx, fleetName, []api.Condition{condition})
	return service.ApiStatusToErr(status)
}

func (f FleetSelectorMatchingLogic) setOverlappingFleetConditionFalse(ctx context.Context, fleetName string) error {
	condition := api.Condition{
		Type:   api.ConditionTypeFleetOverlappingSelectors,
		Status: api.ConditionStatusFalse,
	}
	status := f.serviceHandler.UpdateFleetConditions(ctx, fleetName, []api.Condition{condition})
	return service.ApiStatusToErr(status)
}

func createOverlappingConditionMessage(matchingFleets []string) string {
	if len(matchingFleets) > 1 {
		return strings.Join(matchingFleets, ",")
	} else {
		return ""
	}
}

func getMatchLabelsSafe(fleet *api.Fleet) map[string]string {
	if fleet.Spec.Selector != nil {
		return lo.FromPtr(fleet.Spec.Selector.MatchLabels)
	}
	return map[string]string{}
}

func labelSelectorFromLabelMap(labels map[string]string) *string {
	var parts []string
	if len(labels) == 0 {
		return nil
	}
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return lo.ToPtr(strings.Join(parts, ","))
}
