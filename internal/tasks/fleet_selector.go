package tasks

// Fleet Selector Matching Task
//
// This task handles fleet selector updates and device label changes to maintain
// proper device ownership and fleet assignments.
//
// TODO: Future Improvements
// - Implement batch device updates instead of individual ReplaceDevice calls for better performance
//
// The task ensures:
// 1. Devices that match fleet selectors are assigned the correct owner
// 2. Devices with multiple matching fleets have MultipleOwners condition set
// 3. Orphaned devices (no longer matching any fleet) have their owner removed
//
// We have 2 cases:
// 1. Fleet create/update/delete:
//    Reference kind: Fleet
//    Task description: Iterate devices that match the fleet's selector and set owners/conditions as necessary
// 2. Device create/update (no work needed for delete):
//    Reference kind: Device
//    Task description: Iterate fleets and set the device's owner/conditions as necessary

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

func fleetSelectorMatching(ctx context.Context, orgId uuid.UUID, event domain.Event, serviceHandler service.Service, log logrus.FieldLogger) error {
	logic := FleetSelectorMatchingLogic{
		log:            log,
		serviceHandler: serviceHandler,
		orgId:          orgId,
		event:          event,
		itemsPerPage:   ItemsPerPage,
	}

	var err error

	switch {
	case event.InvolvedObject.Kind == domain.DeviceKind:
		err = logic.DeviceLabelsUpdated(ctx)
	case event.InvolvedObject.Kind == domain.FleetKind:
		err = logic.FleetSelectorUpdated(ctx)
	default:
		err = fmt.Errorf("FleetSelectorMatching called with unexpected kind %s and op %s", event.InvolvedObject.Kind, event.Reason)
	}

	return err
}

type FleetSelectorMatchingLogic struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	orgId          uuid.UUID
	event          domain.Event
	itemsPerPage   int32
}

func NewFleetSelectorMatchingLogic(log logrus.FieldLogger, serviceHandler service.Service, orgId uuid.UUID, event domain.Event) FleetSelectorMatchingLogic {
	return FleetSelectorMatchingLogic{
		log:            log,
		serviceHandler: serviceHandler,
		orgId:          orgId,
		event:          event,
		itemsPerPage:   ItemsPerPage,
	}
}

func (f *FleetSelectorMatchingLogic) SetItemsPerPage(items int32) {
	f.itemsPerPage = items
}

func (f FleetSelectorMatchingLogic) DeviceLabelsUpdated(ctx context.Context) error {
	f.log.Infof("Checking fleet owner due to device label update %s/%s", f.orgId, f.event.InvolvedObject.Name)

	device, status := f.serviceHandler.GetDevice(ctx, f.orgId, f.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		if status.Code == http.StatusNotFound {
			return nil
		}
		errorMsg := f.formatCriticalError("device labels update", fmt.Sprintf("failed to get device: %s", status.Message))
		f.log.Errorf("%s", errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}

	// Get the current owner and make sure it's a fleet
	currentOwnerFleet, isOwnerAFleet, err := getOwnerFleet(device)
	if err != nil {
		errorMsg := f.formatDeviceError(f.event.InvolvedObject.Name, "device labels update", fmt.Sprintf("failed to get owner fleet: %v", err))
		f.log.Warnf("%s", errorMsg)
		return err
	}
	if !isOwnerAFleet {
		// No fleet owner, so nothing to do
		return nil
	}

	if !f.hasLabels(device) {
		return f.handleUnlabeledDevice(ctx, device)
	}

	// Find all fleets that match the device's labels
	fleets, err := f.fetchAllFleets(ctx)
	if err != nil {
		errorMsg := f.formatCriticalError("device labels update", fmt.Sprintf("failed to fetch fleets: %v", err))
		f.log.Errorf("%s", errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}
	matchingFleets := findMatchingFleets(*device.Metadata.Labels, fleets)

	var processedWithErrors bool
	// Handle different cases based on number of matching fleets
	switch len(matchingFleets) {
	case 0:
		// No fleet matches, remove owner if it exists
		if len(currentOwnerFleet) != 0 {
			err = f.updateDeviceOwner(ctx, device, "")
			if err != nil {
				f.log.Warnf("Device-specific error: failed to update device owner for device %s (removing owner from %s): %v", f.event.InvolvedObject.Name, currentOwnerFleet, err)
				processedWithErrors = true
			}
		}
		// Set MultipleOwners condition to false
		err = f.setDeviceMultipleOwnersCondition(ctx, device, matchingFleets)
		if err != nil {
			f.log.Warnf("Device-specific error: failed to set multiple owners condition for device %s (no matching fleets): %v", f.event.InvolvedObject.Name, err)
			processedWithErrors = true
		}
	case 1:
		// Single fleet matches, update owner if needed
		newOwnerFleet := matchingFleets[0]
		if currentOwnerFleet != newOwnerFleet {
			err = f.updateDeviceOwner(ctx, device, newOwnerFleet)
			if err != nil {
				f.log.Warnf("Device-specific error: failed to update device owner for device %s (from %s to %s): %v", f.event.InvolvedObject.Name, currentOwnerFleet, newOwnerFleet, err)
				processedWithErrors = true
			}
		}
		// Set MultipleOwners condition to false
		err = f.setDeviceMultipleOwnersCondition(ctx, device, matchingFleets)
		if err != nil {
			f.log.Warnf("Device-specific error: failed to set multiple owners condition for device %s (single fleet match: %s): %v", f.event.InvolvedObject.Name, newOwnerFleet, err)
			processedWithErrors = true
		}
	default:
		// Multiple fleets match - do NOT update device owner, only set condition
		err = f.setDeviceMultipleOwnersCondition(ctx, device, matchingFleets)
		if err != nil {
			f.log.Warnf("Device-specific error: failed to set multiple owners condition for device %s (multiple fleet matches: %v): %v", f.event.InvolvedObject.Name, matchingFleets, err)
			processedWithErrors = true
		}
	}

	if processedWithErrors {
		return fmt.Errorf("device labels update completed with errors")
	}
	return nil
}

// Iterate devices that match the fleet's selector and set owners/conditions as necessary
func (f FleetSelectorMatchingLogic) FleetSelectorUpdated(ctx context.Context) error {
	startTime := time.Now()
	f.log.Infof("Checking fleet owner due to fleet selector update %s/%s", f.orgId, f.event.InvolvedObject.Name)

	// Setup lazy fleet fetching
	allFleetsFetcher := f.createFleetFetcher(ctx)

	// Validate and get fleet
	result := f.validateAndGetFleet(ctx, allFleetsFetcher, startTime)
	if result.Error != nil {
		return result.Error
	}

	// Process fleet selector updates (only if fleet exists)
	var stats ProcessingStats
	if result.Fleet != nil {
		stats = f.processFleetSelectorUpdate(ctx, result.Fleet, allFleetsFetcher)
	}

	if stats.TotalErrors > 0 {
		return fmt.Errorf("fleet selector processing completed with %d errors out of %d devices processed", stats.TotalErrors, stats.TotalDevicesProcessed)
	}

	return nil
}

// createFleetFetcher creates a lazy fleet fetcher using sync.Once
func (f FleetSelectorMatchingLogic) createFleetFetcher(ctx context.Context) func() ([]domain.Fleet, error) {
	var once sync.Once
	var allFleets []domain.Fleet
	var fetchErr error

	return func() ([]domain.Fleet, error) {
		once.Do(func() {
			allFleets, fetchErr = f.fetchAllFleets(ctx)
		})
		return allFleets, fetchErr
	}
}

// FleetValidationResult holds the result of fleet validation
type FleetValidationResult struct {
	Fleet *domain.Fleet
	Error error
}

// validateAndGetFleet validates the fleet exists and returns it, or handles deletion cases
func (f FleetSelectorMatchingLogic) validateAndGetFleet(ctx context.Context, allFleetsFetcher func() ([]domain.Fleet, error), startTime time.Time) FleetValidationResult {
	fleet, status := f.serviceHandler.GetFleet(ctx, f.orgId, f.event.InvolvedObject.Name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		if status.Code == http.StatusNotFound {
			// Case 1: Fleet was deleted - recompute matching fleets for devices that had this fleet as owner
			f.log.Infof("Fleet %s was deleted, recomputing owners for devices that had this fleet as owner", f.event.InvolvedObject.Name)
			err := f.clearFleetOwnershipFromDevices(ctx, allFleetsFetcher)
			return FleetValidationResult{Fleet: nil, Error: err}
		}
		errorMsg := f.formatCriticalError("fleet selector update", fmt.Sprintf("failed to get fleet: %s", status.Message))
		f.log.Errorf("%s", errorMsg)
		return FleetValidationResult{Fleet: nil, Error: fmt.Errorf("%s", errorMsg)}
	}

	// empty selector matches no devices - treat as if fleet was deleted
	if len(getMatchLabelsSafe(fleet)) == 0 {
		f.log.Infof("Fleet %s has empty selector (matches no devices), clearing device ownership", f.event.InvolvedObject.Name)
		err := f.clearFleetOwnershipFromDevices(ctx, allFleetsFetcher)
		return FleetValidationResult{Fleet: nil, Error: err}
	}

	return FleetValidationResult{Fleet: fleet, Error: nil}
}

// ProcessingStats holds the results of fleet selector processing
type ProcessingStats struct {
	TotalDevicesProcessed int
	TotalErrors           int
}

// processFleetSelectorUpdate handles all the fleet selector processing steps
func (f FleetSelectorMatchingLogic) processFleetSelectorUpdate(ctx context.Context, fleet *domain.Fleet, allFleetsFetcher func() ([]domain.Fleet, error)) ProcessingStats {
	var stats ProcessingStats

	// Case 2: Handle devices previously owned by this fleet but no longer match
	devicesProcessed, numErrors := f.handleOrphanedDevices(ctx, fleet, allFleetsFetcher)
	stats.TotalDevicesProcessed += devicesProcessed
	stats.TotalErrors += numErrors

	// Case 3: Handle devices now matching this fleet that have no owner or multipleowners condition
	devicesProcessed, numErrors = f.handleDevicesMatchingFleet(ctx, fleet, allFleetsFetcher)
	stats.TotalDevicesProcessed += devicesProcessed
	stats.TotalErrors += numErrors

	// Case 4: Re-examine all devices with multipleowners condition set
	devicesProcessed, numErrors = f.handleDevicesWithMultipleOwnersCondition(ctx, allFleetsFetcher)
	stats.TotalDevicesProcessed += devicesProcessed
	stats.TotalErrors += numErrors

	return stats
}

// Helper method to clear fleet ownership from devices and cleanup multiple owners conditions
func (f FleetSelectorMatchingLogic) clearFleetOwnershipFromDevices(ctx context.Context, allFleetsFetcher func() ([]domain.Fleet, error)) error {

	// Get all devices that had this fleet as owner
	listParams := domain.ListDevicesParams{
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *util.SetResourceOwner(domain.FleetKind, f.event.InvolvedObject.Name))),
		Limit:         lo.ToPtr(f.itemsPerPage),
	}

	numErrors := 0
	for {
		devices, status := f.serviceHandler.ListDevices(ctx, f.orgId, listParams, nil)
		if status.Code != http.StatusOK {
			return fmt.Errorf("failed to list devices owned by deleted fleet: %s", status.Message)
		}

		for _, device := range devices.Items {
			// Recompute matching fleets for this device
			allFleets, err := allFleetsFetcher()
			if err != nil {
				f.log.Errorf("failed to fetch all fleets: %v", err)
				numErrors++
				continue
			}
			err = f.recomputeDeviceOwnership(ctx, &device, allFleets)
			if err != nil {
				f.log.Errorf("failed to recompute ownership for device %s/%s: %v", f.orgId, *device.Metadata.Name, err)
				numErrors++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}

	// Also re-examine devices with multipleowners condition since fleet deletion
	// might resolve multiple owner conflicts
	_, multipleOwnersErrors := f.handleDevicesWithMultipleOwnersCondition(ctx, allFleetsFetcher)
	numErrors += multipleOwnersErrors

	if numErrors != 0 {
		return fmt.Errorf("failed to recompute ownership for %d devices", numErrors)
	}
	return nil
}

// Case 3: Handle devices now matching this fleet that have no owner or multipleowners condition
func (f FleetSelectorMatchingLogic) handleDevicesMatchingFleet(ctx context.Context, fleet *domain.Fleet, allFleetsFetcher func() ([]domain.Fleet, error)) (int, int) {
	f.log.Infof("Handling devices now matching fleet %s", f.event.InvolvedObject.Name)

	// Get devices that match this fleet's selector
	listParams := domain.ListDevicesParams{
		LabelSelector: labelSelectorFromLabelMap(getMatchLabelsSafe(fleet)),
		Limit:         lo.ToPtr(f.itemsPerPage),
	}

	devicesProcessed, numErrors := 0, 0
	for {
		// Check for context cancellation in long-running loops
		if ctx.Err() != nil {
			f.log.Warnf("Context cancelled during matching fleet devices processing, stopping early. Processed %d devices so far", devicesProcessed)
			return devicesProcessed, numErrors
		}

		devices, status := f.serviceHandler.ListDevices(ctx, f.orgId, listParams, nil)
		if status.Code != http.StatusOK {
			f.log.Errorf("Critical system error: failed to list devices matching fleet: %s", status.Message)
			numErrors++
			break
		}

		for _, device := range devices.Items {
			// Check for context cancellation in long-running loops
			if ctx.Err() != nil {
				f.log.Warnf("Context cancelled during device processing, stopping early. Processed %d devices so far", devicesProcessed)
				return devicesProcessed, numErrors
			}

			// Get the device's current owner for comparison
			currentOwner := lo.FromPtr(device.Metadata.Owner)

			// Check current multiple owners condition status
			currentHasMultipleOwners := false
			if device.Status != nil {
				if cond := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceMultipleOwners); cond != nil {
					currentHasMultipleOwners = cond.Status == domain.ConditionStatusTrue
				}
			}

			// Process device to recompute ownership
			allFleets, err := allFleetsFetcher()
			if err != nil {
				f.log.Errorf("Critical system error: failed to fetch all fleets while processing device %s: %v", lo.FromPtr(device.Metadata.Name), err)
				numErrors++
				return devicesProcessed, numErrors
			}
			err = f.recomputeDeviceOwnership(ctx, &device, allFleets)
			if err != nil {
				f.log.Warnf("Device-specific error: failed to recompute ownership for device %s (labels: %v, current owner: %s): %v",
					lo.FromPtr(device.Metadata.Name),
					lo.FromPtr(device.Metadata.Labels),
					currentOwner,
					err)
				numErrors++
				continue
			}

			// Check if the device's ownership or multiple owners condition actually changed
			newOwner := lo.FromPtr(device.Metadata.Owner)
			newHasMultipleOwners := false

			// Get the updated status from the device object (no need to fetch from DB)
			if device.Status != nil {
				if cond := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceMultipleOwners); cond != nil {
					newHasMultipleOwners = cond.Status == domain.ConditionStatusTrue
				}
			}

			// Only count as processed if something actually changed
			if currentOwner != newOwner || currentHasMultipleOwners != newHasMultipleOwners {
				devicesProcessed++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}

	return devicesProcessed, numErrors
}

// hasLabels returns true if the device has labels assigned to it
func (f FleetSelectorMatchingLogic) hasLabels(device *domain.Device) bool {
	return device.Metadata.Labels != nil && len(*device.Metadata.Labels) != 0
}

// handleUnlabeledDevice handles the necessary logic for processing a device that has no labels
func (f FleetSelectorMatchingLogic) handleUnlabeledDevice(ctx context.Context, device *domain.Device) error {
	// remove owner if it exists
	if lo.FromPtr(device.Metadata.Owner) != "" {
		err := f.updateDeviceOwner(ctx, device, "")
		if err != nil {
			return err
		}
	}
	// Set MultipleOwners condition to false (matching fleets == empty)
	return f.setDeviceMultipleOwnersCondition(ctx, device, []string{})
}

// Helper function to recompute device ownership given all fleets
func (f FleetSelectorMatchingLogic) recomputeDeviceOwnership(ctx context.Context, device *domain.Device, allFleets []domain.Fleet) error {
	if !f.hasLabels(device) {
		return f.handleUnlabeledDevice(ctx, device)
	}

	// Find all fleets that match the device's labels
	matchingFleets := findMatchingFleets(*device.Metadata.Labels, allFleets)

	// Get current owner fleet
	currentOwnerFleet, isOwnerAFleet, err := getOwnerFleet(device)
	if err != nil {
		return err
	}
	if !isOwnerAFleet {
		currentOwnerFleet = ""
	}

	// Handle different cases based on number of matching fleets
	switch len(matchingFleets) {
	case 0:
		// No fleet matches, remove owner if it exists
		if currentOwnerFleet != "" {
			err = f.updateDeviceOwner(ctx, device, "")
			if err != nil {
				return err
			}
		}
		// Set MultipleOwners condition to false
		return f.setDeviceMultipleOwnersCondition(ctx, device, matchingFleets)
	case 1:
		// Single fleet matches, update owner if needed
		newOwnerFleet := matchingFleets[0]
		if currentOwnerFleet != newOwnerFleet {
			err = f.updateDeviceOwner(ctx, device, newOwnerFleet)
			if err != nil {
				return err
			}
		}
		// Set MultipleOwners condition to false
		return f.setDeviceMultipleOwnersCondition(ctx, device, matchingFleets)
	default:
		// Multiple fleets match - do NOT update device owner, only set condition
		return f.setDeviceMultipleOwnersCondition(ctx, device, matchingFleets)
	}
}

func (f FleetSelectorMatchingLogic) setDeviceMultipleOwnersCondition(ctx context.Context, device *domain.Device, matchingFleets []string) error {
	newConditionMessage := createMultipleOwnersConditionMessage(matchingFleets)
	currentConditionMessage := ""

	if device.Status != nil {
		if cond := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceMultipleOwners); cond != nil {
			currentConditionMessage = cond.Message
		}
	}

	// Always update the condition if the message changed or if we have multiple fleets now
	shouldUpdateCondition := currentConditionMessage != newConditionMessage

	if shouldUpdateCondition {
		condition := domain.Condition{Type: domain.ConditionTypeDeviceMultipleOwners, Status: domain.ConditionStatusFalse}
		if len(matchingFleets) > 1 {
			condition.Status = domain.ConditionStatusTrue
			condition.Reason = "MultipleOwners"
			condition.Message = newConditionMessage
		}

		status := f.serviceHandler.SetDeviceServiceConditions(ctx, f.orgId, *device.Metadata.Name, []domain.Condition{condition})
		if status.Code != http.StatusOK {
			return service.ApiStatusToErr(status)
		}

		// Update the device object in-place to reflect the new condition state
		if device.Status == nil {
			device.Status = &domain.DeviceStatus{}
		}
		domain.SetStatusCondition(&device.Status.Conditions, condition)
	}

	return nil
}

// Update a device's owner, which in effect updates the fleet (may require rollout to the device)
func (f FleetSelectorMatchingLogic) updateDeviceOwner(ctx context.Context, device *domain.Device, newOwnerFleet string) error {
	// do not update decommissioning devices
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		f.log.Debugf("Skipping update of device owner for decommissioned device: %s", device.Metadata.Name)
		return nil
	}

	fieldsToNil := []string{}
	newOwnerRef := util.SetResourceOwner(domain.FleetKind, newOwnerFleet)
	if len(newOwnerFleet) == 0 {
		newOwnerRef = nil
		fieldsToNil = append(fieldsToNil, "owner")
	}

	f.log.Infof("Updating fleet of device %s from %s to %s", *device.Metadata.Name, util.DefaultIfNil(device.Metadata.Owner, "<none>"), util.DefaultIfNil(newOwnerRef, "<none>"))
	device.Metadata.Owner = newOwnerRef
	_, status := f.serviceHandler.ReplaceDevice(ctx, f.orgId, *device.Metadata.Name, lo.FromPtr(device), fieldsToNil)

	if err := service.ApiStatusToErr(status); err != nil {
		return err
	}
	return f.serviceHandler.UpdateServerSideDeviceStatus(ctx, f.orgId, *device.Metadata.Name)
}

func (f FleetSelectorMatchingLogic) fetchAllFleets(ctx context.Context) ([]domain.Fleet, error) {
	var fleets []domain.Fleet
	fleetListParams := domain.ListFleetsParams{Limit: lo.ToPtr(f.itemsPerPage)}
	for {
		fleetBatch, status := f.serviceHandler.ListFleets(ctx, f.orgId, fleetListParams)
		if status.Code != http.StatusOK {
			return nil, fmt.Errorf("failed fetching fleets: %s", status.Message)
		}

		fleets = append(fleets, fleetBatch.Items...)
		if fleetBatch.Metadata.Continue == nil {
			break
		}
		fleetListParams.Continue = fleetBatch.Metadata.Continue
	}
	return fleets, nil
}

func createMultipleOwnersConditionMessage(matchingFleets []string) string {
	if len(matchingFleets) > 1 {
		// this message is used to determine whether an update occurs or not, so do a quick sort on a copy to ensure
		// stable updates without mutating the caller args
		fleets := append([]string{}, matchingFleets...)
		sort.Strings(fleets)
		return strings.Join(fleets, ",")
	} else {
		return ""
	}
}

func getMatchLabelsSafe(fleet *domain.Fleet) map[string]string {
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

func findMatchingFleets(labels map[string]string, fleets []domain.Fleet) []string {
	// Find all fleets with a selector that the device matches
	var matchingFleets []string
	for _, fleet := range fleets {
		if util.LabelsMatchLabelSelector(labels, getMatchLabelsSafe(&fleet)) {
			matchingFleets = append(matchingFleets, *fleet.Metadata.Name)
		}
	}
	return matchingFleets
}

// Helper methods for consistent error formatting
func (f FleetSelectorMatchingLogic) formatCriticalError(operation, message string) string {
	return fmt.Sprintf("Critical system error during %s: %s", operation, message)
}

func (f FleetSelectorMatchingLogic) formatDeviceError(deviceName, operation, message string) string {
	return fmt.Sprintf("Device-specific error for %s during %s: %s", deviceName, operation, message)
}

// Wrapper methods that return device counts and error counts
func (f FleetSelectorMatchingLogic) handleOrphanedDevices(ctx context.Context, fleet *domain.Fleet, allFleetsFetcher func() ([]domain.Fleet, error)) (int, int) {
	f.log.Infof("Handling devices previously owned by fleet %s but no longer match", f.event.InvolvedObject.Name)

	// Get devices owned by this fleet that no longer match its selector
	labelsMap := getMatchLabelsSafe(fleet)

	// Build selector for devices that DON'T match the fleet's labels
	var keys, values []string
	for k, v := range labelsMap {
		keys = append(keys, k)
		values = append(values, v)
	}

	// Construct selector: devices that don't match AND are owned by this fleet
	listParams := domain.ListDevicesParams{
		Limit:         lo.ToPtr(f.itemsPerPage),
		LabelSelector: lo.ToPtr(fmt.Sprintf("(%s) != (%s)", strings.Join(keys, ","), strings.Join(values, ","))),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *util.SetResourceOwner(domain.FleetKind, f.event.InvolvedObject.Name))),
	}

	devicesProcessed, numErrors := 0, 0
	for {
		// Check for context cancellation in long-running loops
		if ctx.Err() != nil {
			f.log.Warnf("Context cancelled during orphaned devices processing, stopping early. Processed %d devices so far", devicesProcessed)
			return devicesProcessed, numErrors
		}

		devices, status := f.serviceHandler.ListDevices(ctx, f.orgId, listParams, nil)
		if status.Code != http.StatusOK {
			f.log.Errorf("Critical system error: failed to list orphaned devices: %s", status.Message)
			numErrors++
			break
		}

		for _, device := range devices.Items {
			// Check for context cancellation in long-running loops
			if ctx.Err() != nil {
				f.log.Warnf("Context cancelled during device processing, stopping early. Processed %d devices so far", devicesProcessed)
				return devicesProcessed, numErrors
			}

			devicesProcessed++
			// Recompute matching fleets for this orphaned device
			allFleets, err := allFleetsFetcher()
			if err != nil {
				f.log.Errorf("Critical system error: failed to fetch all fleets while processing orphaned device %s: %v", lo.FromPtr(device.Metadata.Name), err)
				return devicesProcessed, numErrors + 1
			}
			err = f.recomputeDeviceOwnership(ctx, &device, allFleets)
			if err != nil {
				f.log.Warnf("Device-specific error: failed to recompute ownership for orphaned device %s (labels: %v, current owner: %s): %v",
					lo.FromPtr(device.Metadata.Name),
					lo.FromPtr(device.Metadata.Labels),
					lo.FromPtr(device.Metadata.Owner),
					err)
				numErrors++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}

	return devicesProcessed, numErrors
}

func (f FleetSelectorMatchingLogic) handleDevicesWithMultipleOwnersCondition(ctx context.Context, allFleetsFetcher func() ([]domain.Fleet, error)) (int, int) {
	f.log.Infof("Re-examining all devices with multipleowners condition")

	devicesProcessed, numErrors := 0, 0

	// Use store-level pagination - start with empty continue
	listParams := store.ListParams{Limit: int(f.itemsPerPage)}

	for {
		// Check for context cancellation in long-running loops
		if ctx.Err() != nil {
			f.log.Warnf("Context cancelled during multiple owners condition processing, stopping early. Processed %d devices so far", devicesProcessed)
			return devicesProcessed, numErrors
		}

		// Use the specialized service method for querying by service condition
		devices, status := f.serviceHandler.ListDevicesByServiceCondition(ctx, f.orgId, string(domain.ConditionTypeDeviceMultipleOwners), string(domain.ConditionStatusTrue), listParams)
		if status.Code != http.StatusOK {
			f.log.Errorf("Critical system error: failed to list devices with multiple owners condition: %s", status.Message)
			return devicesProcessed, numErrors + 1
		}

		if len(devices.Items) == 0 {
			break // No more devices to process
		}

		for _, device := range devices.Items {
			// Check for context cancellation in long-running loops
			if ctx.Err() != nil {
				f.log.Warnf("Context cancelled during device processing, stopping early. Processed %d devices so far", devicesProcessed)
				return devicesProcessed, numErrors
			}

			// Get the device's current state for comparison
			currentOwner := lo.FromPtr(device.Metadata.Owner)
			currentHasMultipleOwners := false
			currentConditionMessage := ""

			if device.Status != nil {
				if cond := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceMultipleOwners); cond != nil {
					currentHasMultipleOwners = cond.Status == domain.ConditionStatusTrue
					currentConditionMessage = cond.Message
				}
			}

			// Recompute ownership for this device
			allFleets, err := allFleetsFetcher()
			if err != nil {
				f.log.Errorf("Critical system error: failed to fetch all fleets while processing device with multiple owners condition %s: %v", lo.FromPtr(device.Metadata.Name), err)
				numErrors++
				continue
			}
			err = f.recomputeDeviceOwnership(ctx, &device, allFleets)
			if err != nil {
				f.log.Warnf("Device-specific error: failed to recompute ownership for device with multiple owners condition %s (labels: %v, current owner: %s): %v",
					lo.FromPtr(device.Metadata.Name),
					lo.FromPtr(device.Metadata.Labels),
					currentOwner,
					err)
				numErrors++
				continue
			}

			// Check if anything actually changed
			newOwner := lo.FromPtr(device.Metadata.Owner)
			newHasMultipleOwners := false
			newConditionMessage := ""

			// Get the updated status from the device object (no need to fetch from DB)
			if device.Status != nil {
				if cond := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceMultipleOwners); cond != nil {
					newHasMultipleOwners = cond.Status == domain.ConditionStatusTrue
					newConditionMessage = cond.Message
				}
			}

			// Only count as processed if something actually changed
			if currentOwner != newOwner || currentHasMultipleOwners != newHasMultipleOwners || currentConditionMessage != newConditionMessage {
				devicesProcessed++
			}
		}

		// Check if there are more pages
		if devices.Metadata.Continue == nil {
			break // No more pages
		}

		// Convert the API-level continue token back to store-level format for next iteration
		nextContinue, err := store.ParseContinueString(devices.Metadata.Continue)
		if err != nil {
			f.log.Errorf("Failed to parse continue token: %v", err)
			numErrors++
			break
		}
		listParams.Continue = nextContinue
	}

	f.log.Infof("Re-examined %d devices with multipleowners condition", devicesProcessed)
	return devicesProcessed, numErrors
}
