package common

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	ApplicationStatusInfoHealthy      = "Device's application workloads are healthy."
	ApplicationStatusInfoUndefined    = "Device has not reported any application workloads yet."
	DeviceStatusInfoHealthy           = "Device's system resources are healthy."
	DeviceStatusInfoRebooting         = "Device is rebooting."
	DeviceStatusInfoAwaitingReconnect = "Device has not reconnected since restore to confirm its current state."
	DeviceStatusInfoConflictPaused    = "Device reconciliation is paused due to a state conflict between the service and the device's agent; manual intervention is required."
	CPUIsCritical                     = "CPU utilization has reached a critical level."
	CPUIsWarning                      = "CPU utilization has reached a warning level."
	CPUIsNormal                       = "CPU utilization has returned to normal."
	MemoryIsCritical                  = "Memory utilization has reached a critical level."
	MemoryIsWarning                   = "Memory utilization has reached a warning level."
	MemoryIsNormal                    = "Memory utilization has returned to normal."
	DiskIsCritical                    = "Disk utilization has reached a critical level."
	DiskIsWarning                     = "Disk utilization has reached a warning level."
	DiskIsNormal                      = "Disk utilization has returned to normal."
)

type DeviceSuccessEvent func(ctx context.Context, created bool, resourceKind domain.ResourceKind, resourceName string, updateDetails *domain.ResourceUpdatedDetailsUpdatedFields, log logrus.FieldLogger) *domain.Event
type DeviceFailureEvent func(ctx context.Context, created bool, resourceKind domain.ResourceKind, resourceName string, status domain.Status) *domain.Event

type ResourceUpdate struct {
	Reason  domain.EventReason
	Details string
}
type ResourceUpdates []ResourceUpdate

type GetResourceEventFromUpdateDetailsFunc func(ctx context.Context, update ResourceUpdate) *domain.Event

type statusType map[domain.DeviceResourceStatusType]ResourceUpdate

var (
	cpuStatus = statusType{
		domain.DeviceResourceStatusCritical: ResourceUpdate{Reason: domain.EventReasonDeviceCPUCritical, Details: CPUIsCritical},
		domain.DeviceResourceStatusWarning:  ResourceUpdate{Reason: domain.EventReasonDeviceCPUWarning, Details: CPUIsWarning},
		domain.DeviceResourceStatusHealthy:  ResourceUpdate{Reason: domain.EventReasonDeviceCPUNormal, Details: CPUIsNormal},
	}

	memoryStatus = statusType{
		domain.DeviceResourceStatusCritical: ResourceUpdate{Reason: domain.EventReasonDeviceMemoryCritical, Details: MemoryIsCritical},
		domain.DeviceResourceStatusWarning:  ResourceUpdate{Reason: domain.EventReasonDeviceMemoryWarning, Details: MemoryIsWarning},
		domain.DeviceResourceStatusHealthy:  ResourceUpdate{Reason: domain.EventReasonDeviceMemoryNormal, Details: MemoryIsNormal},
	}

	diskStatus = statusType{
		domain.DeviceResourceStatusCritical: ResourceUpdate{Reason: domain.EventReasonDeviceDiskCritical, Details: DiskIsCritical},
		domain.DeviceResourceStatusWarning:  ResourceUpdate{Reason: domain.EventReasonDeviceDiskWarning, Details: DiskIsWarning},
		domain.DeviceResourceStatusHealthy:  ResourceUpdate{Reason: domain.EventReasonDeviceDiskNormal, Details: DiskIsNormal},
	}
)

// do not overwrite valid service-side statuses with placeholder device-side status
func KeepDBDeviceStatus(device, dbDevice *domain.Device) {
	if device.Status.Summary.Status == domain.DeviceSummaryStatusUnknown {
		device.Status.Summary.Status = dbDevice.Status.Summary.Status
	}
	if device.Status.Lifecycle.Status == domain.DeviceLifecycleStatusUnknown {
		device.Status.Lifecycle.Status = dbDevice.Status.Lifecycle.Status
	}
	if device.Status.Updated.Status == domain.DeviceUpdatedStatusUnknown {
		device.Status.Updated.Status = dbDevice.Status.Updated.Status
	}
	if device.Status.ApplicationsSummary.Status == domain.ApplicationsSummaryStatusUnknown {
		device.Status.ApplicationsSummary.Status = dbDevice.Status.ApplicationsSummary.Status
	}
	if device.Status.Integrity.Status == domain.DeviceIntegrityStatusUnknown {
		device.Status.Integrity = dbDevice.Status.Integrity
	}

	// Preserve service-side statuses that should take precedence over agent-reported status
	// These statuses are set by the service based on annotations and should not be overwritten
	if dbDevice.Status.Summary.Status == domain.DeviceSummaryStatusAwaitingReconnect ||
		dbDevice.Status.Summary.Status == domain.DeviceSummaryStatusConflictPaused {
		device.Status.Summary.Status = dbDevice.Status.Summary.Status
		device.Status.Summary.Info = dbDevice.Status.Summary.Info
	}
}

func ComputeDeviceStatusChanges(ctx context.Context, oldDevice, newDevice *domain.Device, orgId uuid.UUID) ResourceUpdates {
	resourceUpdates := make(ResourceUpdates, 0, 7)

	// Don't generate status change events during device creation (when oldDevice is nil)
	if oldDevice == nil {
		return resourceUpdates
	}

	// Check if OS image digest changed (used for CVE lifecycle event processing)
	oldDigest := getOSImageDigest(oldDevice)
	newDigest := getOSImageDigest(newDevice)
	if oldDigest != newDigest && newDigest != "" {
		var details string
		if oldDigest == "" {
			details = fmt.Sprintf("Initial OS image detected: %s", newDigest)
		} else {
			details = fmt.Sprintf("OS image changed from %s to %s", oldDigest, newDigest)
		}
		resourceUpdates = append(resourceUpdates, ResourceUpdate{
			Reason:  domain.EventReasonDeviceOSImageChanged,
			Details: details,
		})
	}

	if hasStatusChanged(oldDevice, newDevice, domain.DeviceSummaryStatusUnknown, func(d *domain.Device) domain.DeviceSummaryStatusType { return d.Status.Summary.Status }) {
		if newDevice.Status.Summary.Status == domain.DeviceSummaryStatusUnknown {
			resourceUpdates = append(resourceUpdates, ResourceUpdate{Reason: domain.EventReasonDeviceDisconnected, Details: lo.FromPtr(newDevice.Status.Summary.Info)})
		} else if newDevice.Status.Summary.Status == domain.DeviceSummaryStatusRebooting {
			resourceUpdates = append(resourceUpdates, ResourceUpdate{Reason: domain.EventReasonDeviceIsRebooting, Details: lo.FromPtr(newDevice.Status.Summary.Info)})
		} else if newDevice.Status.Summary.Status == domain.DeviceSummaryStatusOnline {
			resourceUpdates = append(resourceUpdates, ResourceUpdate{Reason: domain.EventReasonDeviceConnected, Details: lo.FromPtr(newDevice.Status.Summary.Info)})
		} else if newDevice.Status.Summary.Status == domain.DeviceSummaryStatusConflictPaused {
			resourceUpdates = append(resourceUpdates, ResourceUpdate{Reason: domain.EventReasonDeviceConflictPaused, Details: lo.FromPtr(newDevice.Status.Summary.Info)})
		}
	}

	if hasStatusChanged(oldDevice, newDevice, domain.DeviceUpdatedStatusUnknown, func(d *domain.Device) domain.DeviceUpdatedStatusType { return d.Status.Updated.Status }) {
		var status domain.EventReason
		oldStatus := domain.DeviceUpdatedStatusUnknown
		if oldDevice.Status != nil {
			oldStatus = oldDevice.Status.Updated.Status
		}
		switch {
		case newDevice.Status.Updated.Status == domain.DeviceUpdatedStatusUnknown:
			status = domain.EventReasonDeviceDisconnected
		case newDevice.Status.Updated.Status == domain.DeviceUpdatedStatusUpdating:
			status = domain.EventReasonDeviceContentUpdating
		case newDevice.Status.Updated.Status == domain.DeviceUpdatedStatusOutOfDate:
			// Check if there's an update error condition
			if updateCondition := domain.FindStatusCondition(newDevice.Status.Conditions, domain.ConditionTypeDeviceUpdating); updateCondition != nil {
				if updateCondition.Reason == string(domain.UpdateStateError) && updateCondition.Message != "" {
					status = domain.EventReasonDeviceUpdateFailed
				} else {
					status = domain.EventReasonDeviceContentOutOfDate
				}
			} else {
				status = domain.EventReasonDeviceContentOutOfDate
			}
		case newDevice.Status.Updated.Status == domain.DeviceUpdatedStatusUpToDate && oldStatus != domain.DeviceUpdatedStatusUnknown:
			status = domain.EventReasonDeviceContentUpToDate
		}
		if !lo.IsEmpty(status) {
			resourceUpdates = append(resourceUpdates, ResourceUpdate{Reason: status, Details: lo.FromPtr(newDevice.Status.Updated.Info)})
		}
	}

	if hasStatusChanged(oldDevice, newDevice, domain.ApplicationsSummaryStatusUnknown, func(d *domain.Device) domain.ApplicationsSummaryStatusType {
		return d.Status.ApplicationsSummary.Status
	}) {
		var status domain.EventReason
		switch newDevice.Status.ApplicationsSummary.Status {
		case domain.ApplicationsSummaryStatusUnknown:
			status = domain.EventReasonDeviceDisconnected
		case domain.ApplicationsSummaryStatusError:
			status = domain.EventReasonDeviceApplicationError
		case domain.ApplicationsSummaryStatusDegraded:
			status = domain.EventReasonDeviceApplicationDegraded
		case domain.ApplicationsSummaryStatusHealthy, domain.ApplicationsSummaryStatusNoApplications:
			status = domain.EventReasonDeviceApplicationHealthy
		}
		if !lo.IsEmpty(status) {
			resourceUpdates = append(resourceUpdates, ResourceUpdate{Reason: status, Details: lo.FromPtr(newDevice.Status.ApplicationsSummary.Info)})
		}
	}

	resourceChecks := []struct {
		statusMap statusType
		getter    func(*domain.Device) domain.DeviceResourceStatusType
	}{
		{cpuStatus, func(d *domain.Device) domain.DeviceResourceStatusType { return d.Status.Resources.Cpu }},
		{memoryStatus, func(d *domain.Device) domain.DeviceResourceStatusType { return d.Status.Resources.Memory }},
		{diskStatus, func(d *domain.Device) domain.DeviceResourceStatusType { return d.Status.Resources.Disk }},
	}
	for _, check := range resourceChecks {
		checkResourceStatus(oldDevice, newDevice, check.statusMap, check.getter, &resourceUpdates)
	}

	return resourceUpdates
}

func hasStatusChanged[T comparable](oldDevice *domain.Device, newDevice *domain.Device, defaultValue T, getter func(*domain.Device) T) bool {
	newStatus := getter(newDevice)
	if oldDevice != nil && oldDevice.Status != nil {
		return getter(oldDevice) != newStatus
	}
	return defaultValue != newStatus
}

// Generate events for all transitions except Unknown -> Healthy (normal startup)
func checkResourceStatus(oldDevice, newDevice *domain.Device, statusMap statusType, getter func(*domain.Device) domain.DeviceResourceStatusType, resourceUpdates *ResourceUpdates) {
	oldStatus := domain.DeviceResourceStatusUnknown
	if oldDevice != nil && oldDevice.Status != nil {
		oldStatus = getter(oldDevice)
	}

	newStatus := getter(newDevice)
	if oldStatus == newStatus ||
		(oldStatus == domain.DeviceResourceStatusUnknown && newStatus == domain.DeviceResourceStatusHealthy) {
		return
	}
	if update, ok := statusMap[newStatus]; ok {
		*resourceUpdates = append(*resourceUpdates, update)
	} else if update, ok = statusMap[domain.DeviceResourceStatusHealthy]; ok {
		*resourceUpdates = append(*resourceUpdates, update)
	}
}

// EmitMultipleOwnersEvents emits events for MultipleOwners condition changes
func EmitMultipleOwnersEvents(ctx context.Context, device *domain.Device, oldCondition, newCondition *domain.Condition,
	createEvent func(context.Context, *domain.Event),
	getDeviceMultipleOwnersDetectedEvent func(context.Context, string, []string, logrus.FieldLogger) *domain.Event,
	getDeviceMultipleOwnersResolvedEvent func(context.Context, string, domain.DeviceMultipleOwnersResolvedDetailsResolutionType, *string, []string, logrus.FieldLogger) *domain.Event,
	log logrus.FieldLogger) {

	deviceName := *device.Metadata.Name
	wasMultipleOwners := oldCondition != nil && oldCondition.Status == domain.ConditionStatusTrue
	isMultipleOwners := newCondition != nil && newCondition.Status == domain.ConditionStatusTrue

	log.Infof("Device %s: MultipleOwners transition: was=%v, is=%v", deviceName, wasMultipleOwners, isMultipleOwners)

	if !wasMultipleOwners && isMultipleOwners {
		// Multiple owners detected
		var matchingFleets []string
		if newCondition.Message != "" {
			matchingFleets = strings.Split(newCondition.Message, ",")
		}
		log.Infof("Device %s: Emitting DeviceMultipleOwnersDetectedEvent", deviceName)
		createEvent(ctx, getDeviceMultipleOwnersDetectedEvent(ctx, deviceName, matchingFleets, log))
	} else if wasMultipleOwners && !isMultipleOwners {
		// Multiple owners resolved
		log.Infof("Device %s: Emitting DeviceMultipleOwnersResolvedEvent", deviceName)
		// Determine resolution type and assigned owner
		resolutionType := domain.NoMatch
		var assignedOwner *string

		if device.Metadata.Owner != nil {
			ownerFleet, isOwnerAFleet, err := getOwnerFleet(device)
			if err == nil && isOwnerAFleet && ownerFleet != "" {
				resolutionType = domain.SingleMatch
				assignedOwner = device.Metadata.Owner
			}
		}

		// Parse previous matching fleets from old condition message
		var previousMatchingFleets []string
		if oldCondition.Message != "" {
			previousMatchingFleets = strings.Split(oldCondition.Message, ",")
		}

		createEvent(ctx, getDeviceMultipleOwnersResolvedEvent(ctx, deviceName, resolutionType, assignedOwner, previousMatchingFleets, log))
	}
}

// getOwnerFleet extracts the fleet name from a device's owner reference
func getOwnerFleet(device *domain.Device) (string, bool, error) {
	if device.Metadata.Owner == nil {
		return "", true, nil
	}

	ownerType, ownerName, err := util.GetResourceOwner(device.Metadata.Owner)
	if err != nil {
		return "", false, err
	}

	if ownerType != domain.FleetKind {
		return "", false, nil
	}

	return ownerName, true, nil
}

// getOSImageDigest extracts the OS image digest from a device's status.
// Returns empty string if the device or status is nil or digest is not set.
func getOSImageDigest(device *domain.Device) string {
	if device == nil || device.Status == nil {
		return ""
	}
	return device.Status.Os.ImageDigest
}

// EmitSpecValidEvents emits events for SpecValid condition changes
func EmitSpecValidEvents(ctx context.Context, device *domain.Device, oldCondition, newCondition *domain.Condition,
	createEvent func(context.Context, *domain.Event),
	getDeviceSpecValidEvent func(ctx context.Context, deviceName string) *domain.Event,
	getDeviceSpecInvalidEvent func(ctx context.Context, deviceName string, message string) *domain.Event,
	log logrus.FieldLogger,
) {

	deviceName := *device.Metadata.Name
	wasSpecValid := oldCondition != nil && oldCondition.Status == domain.ConditionStatusTrue
	isSpecValid := newCondition != nil && newCondition.Status == domain.ConditionStatusTrue

	log.Infof("Device %s: SpecValid transition: was=%v, is=%v", deviceName, wasSpecValid, isSpecValid)

	if !wasSpecValid && isSpecValid {
		// Spec became valid (or was valid from the start)
		log.Infof("Device %s: Emitting DeviceSpecValidEvent", deviceName)
		createEvent(ctx, getDeviceSpecValidEvent(ctx, deviceName))
	} else if wasSpecValid && !isSpecValid {
		// Spec became invalid (was valid before)
		log.Infof("Device %s: Emitting DeviceSpecInvalidEvent", deviceName)
		// Get the message from the new condition if available
		message := "Unknown"
		if newCondition != nil && newCondition.Message != "" {
			message = newCondition.Message
		}
		createEvent(ctx, getDeviceSpecInvalidEvent(ctx, deviceName, message))
	} else if oldCondition == nil && newCondition != nil {
		// Special case: device created with invalid spec (no previous condition, but new condition is invalid)
		log.Infof("Device %s: Emitting DeviceSpecInvalidEvent for initial invalid spec", deviceName)
		// Get the message from the new condition if available
		message := "Unknown"
		if newCondition.Message != "" {
			message = newCondition.Message
		}
		createEvent(ctx, getDeviceSpecInvalidEvent(ctx, deviceName, message))
	}
}
