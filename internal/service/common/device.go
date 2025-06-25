package common

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	ApplicationStatusInfoHealthy   = "All application workloads are healthy."
	ApplicationStatusInfoUndefined = "No application workloads are defined."
	DeviceStatusInfoHealthy        = "All system resources healthy."
	DeviceStatusInfoRebooting      = "The device is rebooting."
	CPUIsCritical                  = "CPU utilization has reached a critical level"
	CPUIsWarning                   = "CPU utilization has reached a warning level"
	CPUIsNormal                    = "CPU utilization has returned to normal"
	MemoryIsCritical               = "Memory utilization has reached a critical level"
	MemoryIsWarning                = "Memory utilization has reached a warning level"
	MemoryIsNormal                 = "Memory utilization has returned to normal"
	DiskIsCritical                 = "Disk utilization has reached a critical level"
	DiskIsWarning                  = "Disk utilization has reached a warning level"
	DiskIsNormal                   = "Disk utilization has returned to normal"
)

type ResourceUpdate struct {
	Reason        api.EventReason
	UpdateDetails string
}
type ResourceUpdates []ResourceUpdate

type GetResourceEventFromUpdateDetailsFunc func(ctx context.Context, update ResourceUpdate) *api.Event

func UpdateServiceSideStatus(ctx context.Context, orgId uuid.UUID, newDevice, oldDevice *api.Device,
	st store.Store, log logrus.FieldLogger,
	createEvent func(ctx context.Context, event *api.Event), getResourceEventFromUpdateDetailsFunc GetResourceEventFromUpdateDetailsFunc) bool {

	if newDevice == nil {
		return false
	}
	if newDevice.Status == nil {
		newDevice.Status = lo.ToPtr(api.NewDeviceStatus())
	}

	if createEvent == nil {
		createEvent = func(ctx context.Context, event *api.Event) {}
	}

	if getResourceEventFromUpdateDetailsFunc == nil {
		getResourceEventFromUpdateDetailsFunc = func(ctx context.Context, update ResourceUpdate) *api.Event { return &api.Event{} }
	}

	var (
		deviceStatusChanged, updatedStatusChanged, applicationStatusChanged, lifecycleStatusChanged bool
		updates                                                                                     ResourceUpdates
	)

	deviceStatusChanged, updates = updateServerSideDeviceStatus(newDevice, oldDevice)
	for _, update := range updates {
		createEvent(ctx, getResourceEventFromUpdateDetailsFunc(ctx, update))
	}

	updatedStatusChanged, updates = updateServerSideDeviceUpdatedStatus(newDevice, oldDevice, ctx, st, log, orgId)
	for _, update := range updates {
		createEvent(ctx, getResourceEventFromUpdateDetailsFunc(ctx, update))
	}

	applicationStatusChanged, updates = updateServerSideApplicationStatus(newDevice, oldDevice)
	for _, update := range updates {
		createEvent(ctx, getResourceEventFromUpdateDetailsFunc(ctx, update))
	}

	lifecycleStatusChanged, updates = updateServerSideLifecycleStatus(newDevice, oldDevice)
	for _, update := range updates {
		createEvent(ctx, getResourceEventFromUpdateDetailsFunc(ctx, update))
	}

	return deviceStatusChanged || updatedStatusChanged || applicationStatusChanged || lifecycleStatusChanged
}

func isDisconnectedServerSideDeviceStatus(device *api.Device, oldStatus api.DeviceSummaryStatusType) ResourceUpdates {
	device.Status.Summary.Status = api.DeviceSummaryStatusUnknown
	device.Status.Summary.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
	if oldStatus != device.Status.Summary.Status {
		return ResourceUpdates{{
			Reason:        api.DeviceDisconnected,
			UpdateDetails: *device.Status.Summary.Info,
		}}
	}
	return ResourceUpdates{}
}

func isRebootingServerSideDeviceStatus(device *api.Device, oldStatus api.DeviceSummaryStatusType) ResourceUpdates {
	device.Status.Summary.Status = api.DeviceSummaryStatusRebooting
	device.Status.Summary.Info = lo.ToPtr(DeviceStatusInfoRebooting)
	if oldStatus != device.Status.Summary.Status {
		return ResourceUpdates{{
			Reason:        api.DeviceDisconnected,
			UpdateDetails: *device.Status.Summary.Info,
		}}
	}
	return ResourceUpdates{}
}

func resourcesCpu(cpu, oldCpu api.DeviceResourceStatusType, resourceErrors *[]string, resourceDegradations *[]string, allDevicesWereUnknown bool, deviceUpdates *ResourceUpdates) {
	var deviceUpdate ResourceUpdate
	switch cpu {
	case api.DeviceResourceStatusCritical:
		*resourceErrors = append(*resourceErrors, CPUIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceCPUCritical,
			UpdateDetails: CPUIsCritical,
		}
	case api.DeviceResourceStatusWarning:
		*resourceDegradations = append(*resourceDegradations, CPUIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceCPUWarning,
			UpdateDetails: CPUIsWarning,
		}
	default:
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceCPUNormal,
			UpdateDetails: CPUIsNormal,
		}
	}
	if !allDevicesWereUnknown && oldCpu != cpu {
		*deviceUpdates = append(*deviceUpdates, deviceUpdate)
	}
}

func resourcesMemory(memory, oldMemory api.DeviceResourceStatusType, resourceErrors *[]string, resourceDegradations *[]string, allDevicesWereUnknown bool, deviceUpdates *ResourceUpdates) {
	var deviceUpdate ResourceUpdate
	switch memory {
	case api.DeviceResourceStatusCritical:
		*resourceErrors = append(*resourceErrors, MemoryIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceMemoryCritical,
			UpdateDetails: MemoryIsCritical,
		}
	case api.DeviceResourceStatusWarning:
		*resourceDegradations = append(*resourceDegradations, MemoryIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceMemoryWarning,
			UpdateDetails: MemoryIsWarning,
		}
	default:
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceMemoryNormal,
			UpdateDetails: MemoryIsNormal,
		}
	}
	if !allDevicesWereUnknown && oldMemory != memory {
		*deviceUpdates = append(*deviceUpdates, deviceUpdate)
	}
}

func resourcesDisk(disk, oldDisk api.DeviceResourceStatusType, resourceErrors *[]string, resourceDegradations *[]string, allDevicesWereUnknown bool, deviceUpdates *ResourceUpdates) {
	var deviceUpdate ResourceUpdate
	switch disk {
	case api.DeviceResourceStatusCritical:
		*resourceErrors = append(*resourceErrors, DiskIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
		if !allDevicesWereUnknown && oldDisk != disk {
			deviceUpdate = ResourceUpdate{
				Reason:        api.DeviceDiskCritical,
				UpdateDetails: DiskIsCritical,
			}
		}
	case api.DeviceResourceStatusWarning:
		*resourceDegradations = append(*resourceDegradations, DiskIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceDiskWarning,
			UpdateDetails: DiskIsWarning,
		}
	default:
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceDiskNormal,
			UpdateDetails: DiskIsNormal,
		}
	}
	if !allDevicesWereUnknown && oldDisk != disk {
		*deviceUpdates = append(*deviceUpdates, deviceUpdate)
	}
}

func updateServerSideDeviceStatus(device, oldDevice *api.Device) (bool, ResourceUpdates) {
	lastDeviceStatus := device.Status.Summary.Status
	oldStatus := api.DeviceSummaryStatusUnknown
	if oldDevice != nil && oldDevice.Status != nil {
		oldStatus = oldDevice.Status.Summary.Status
	}
	if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
		return device.Status.Summary.Status != lastDeviceStatus, isDisconnectedServerSideDeviceStatus(device, oldStatus)
	}
	if device.IsRebooting() {
		return device.Status.Summary.Status != lastDeviceStatus, isRebootingServerSideDeviceStatus(device, oldStatus)
	}

	resourceErrors := []string{}
	resourceDegradations := []string{}
	var (
		allDevicesWereUnknown = false
		oldCpu                = api.DeviceResourceStatusUnknown
		oldMemory             = api.DeviceResourceStatusUnknown
		oldDisk               = api.DeviceResourceStatusUnknown
		deviceUpdates         ResourceUpdates
	)
	if oldDevice != nil && oldDevice.Status != nil {
		oldCpu = oldDevice.Status.Resources.Cpu
		oldMemory = oldDevice.Status.Resources.Memory
		oldDisk = oldDevice.Status.Resources.Disk
		allDevicesWereUnknown = oldCpu == api.DeviceResourceStatusUnknown && oldMemory == api.DeviceResourceStatusUnknown && oldDisk == api.DeviceResourceStatusUnknown
	}

	resourcesCpu(device.Status.Resources.Cpu, oldCpu, &resourceErrors, &resourceDegradations, allDevicesWereUnknown, &deviceUpdates)
	resourcesMemory(device.Status.Resources.Memory, oldMemory, &resourceErrors, &resourceDegradations, allDevicesWereUnknown, &deviceUpdates)
	resourcesDisk(device.Status.Resources.Disk, oldDisk, &resourceErrors, &resourceDegradations, allDevicesWereUnknown, &deviceUpdates)

	switch {
	case len(resourceErrors) > 0:
		device.Status.Summary.Status = api.DeviceSummaryStatusError
		device.Status.Summary.Info = lo.ToPtr(strings.Join(resourceErrors, ", "))
	case len(resourceDegradations) > 0:
		device.Status.Summary.Status = api.DeviceSummaryStatusDegraded
		device.Status.Summary.Info = lo.ToPtr(strings.Join(resourceDegradations, ", "))
	default:
		device.Status.Summary.Status = api.DeviceSummaryStatusOnline
		device.Status.Summary.Info = lo.ToPtr(DeviceStatusInfoHealthy)
	}
	return device.Status.Summary.Status != lastDeviceStatus, deviceUpdates
}

func updateServerSideLifecycleStatus(device, oldDevice *api.Device) (bool, ResourceUpdates) {
	lastLifecycleStatus := device.Status.Lifecycle.Status
	lastLifecycleInfo := device.Status.Lifecycle.Info

	// check device-reported Conditions to see if lifecycle status needs update
	condition := api.FindStatusCondition(device.Status.Conditions, api.DeviceDecommissioning)
	if condition == nil {
		return false, ResourceUpdates{}
	}

	if condition.IsDecomError() {
		device.Status.Lifecycle = api.DeviceLifecycleStatus{
			Info:   lo.ToPtr("Device has errored while decommissioning"),
			Status: api.DeviceLifecycleStatusDecommissioned,
		}
	}

	if condition.IsDecomComplete() {
		device.Status.Lifecycle = api.DeviceLifecycleStatus{
			Info:   lo.ToPtr("Device has completed decommissioning"),
			Status: api.DeviceLifecycleStatusDecommissioned,
		}
	}

	if condition.IsDecomStarted() {
		device.Status.Lifecycle = api.DeviceLifecycleStatus{
			Info:   lo.ToPtr("Device has acknowledged decommissioning request"),
			Status: api.DeviceLifecycleStatusDecommissioning,
		}
	}
	return device.Status.Lifecycle.Status != lastLifecycleStatus && device.Status.Lifecycle.Info != lastLifecycleInfo, ResourceUpdates{}
}

func updateServerSideDeviceUpdatedStatus(device, oldDevice *api.Device, ctx context.Context, st store.Store, log logrus.FieldLogger, orgId uuid.UUID) (bool, ResourceUpdates) {
	var deviceUpdates ResourceUpdates
	oldStatus := api.DeviceUpdatedStatusUnknown
	if oldDevice != nil && oldDevice.Status != nil {
		oldStatus = oldDevice.Status.Updated.Status
	}
	lastUpdateStatus := device.Status.Updated.Status
	if device.IsUpdating() {
		if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
			device.Status.Updated.Status = api.DeviceUpdatedStatusUnknown
			device.Status.Updated.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s) and had an update in progress at that time.", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
			if oldStatus != device.Status.Updated.Status {
				deviceUpdates = append(deviceUpdates, ResourceUpdate{
					Reason:        api.DeviceDisconnected,
					UpdateDetails: *device.Status.Updated.Info,
				})
			}
		} else {
			var agentInfoMessage string
			if updateCondition := api.FindStatusCondition(device.Status.Conditions, api.DeviceUpdating); updateCondition != nil {
				agentInfoMessage = updateCondition.Message
			}
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpdating
			device.Status.Updated.Info = lo.ToPtr(util.DefaultString(agentInfoMessage, "The device is updating to the latest device spec."))
			if oldStatus != device.Status.Updated.Status {
				deviceUpdates = append(deviceUpdates, ResourceUpdate{
					Reason:        api.DeviceContentUpdating,
					UpdateDetails: *device.Status.Updated.Info,
				})
			}
		}
		return device.Status.Updated.Status != lastUpdateStatus, deviceUpdates
	}
	if !device.IsManaged() && !device.IsUpdatedToDeviceSpec() {
		device.Status.Updated.Status = api.DeviceUpdatedStatusOutOfDate
		device.Status.Updated.Info = lo.ToPtr("There is a newer device spec for this device.")
		if oldStatus != device.Status.Updated.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceContentOutOfDate,
				UpdateDetails: *device.Status.Updated.Info,
			})
		}
		return device.Status.Updated.Status != lastUpdateStatus, deviceUpdates
	}
	if device.IsManaged() {
		_, fleetName, err := util.GetResourceOwner(device.Metadata.Owner)
		if err != nil {
			log.Errorf("Failed to determine owner for device %q: %v", *device.Metadata.Name, err)
			return false, nil
		}
		f, err := st.Fleet().Get(ctx, orgId, fleetName, store.GetWithDeviceSummary(false))
		if err != nil {
			log.Errorf("Failed to get fleet for device %q: %v", *device.Metadata.Name, err)
			return false, nil
		}
		if device.IsUpdatedToFleetSpec(f) {
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
			device.Status.Updated.Info = lo.ToPtr("The device has been updated to the fleet's latest device spec.")
			if oldStatus != device.Status.Updated.Status {
				deviceUpdates = append(deviceUpdates, ResourceUpdate{
					Reason:        api.DeviceContentUpToDate,
					UpdateDetails: *device.Status.Updated.Info,
				})
			}
		} else {
			device.Status.Updated.Status = api.DeviceUpdatedStatusOutOfDate

			var errorMessage string
			baseMessage := "The device could not be updated to the fleet's latest device spec"
			if updateCondition := api.FindStatusCondition(device.Status.Conditions, api.DeviceUpdating); updateCondition != nil {
				if updateCondition.Reason == string(api.UpdateStateError) {
					errorMessage = fmt.Sprintf("%s: %s", baseMessage, updateCondition.Message)
				}
			} else if device.Metadata.Annotations != nil {
				if lastRolloutError, ok := (*device.Metadata.Annotations)[api.DeviceAnnotationLastRolloutError]; ok && lastRolloutError != "" {
					errorMessage = fmt.Sprintf("%s: %s", baseMessage, lastRolloutError)
				}
			}
			if errorMessage == "" {
				errorMessage = "Device has not yet been scheduled for update to the fleet's latest spec."
			}
			device.Status.Updated.Info = lo.ToPtr(errorMessage)
			if oldStatus != device.Status.Updated.Status {
				deviceUpdates = append(deviceUpdates, ResourceUpdate{
					Reason:        api.DeviceContentOutOfDate,
					UpdateDetails: *device.Status.Updated.Info,
				})
			}
		}
	} else {
		device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
		device.Status.Updated.Info = lo.ToPtr("The device has been updated to the latest device spec.")
		if oldStatus != device.Status.Updated.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceContentUpToDate,
				UpdateDetails: *device.Status.Updated.Info,
			})
		}
	}
	return device.Status.Updated.Status != lastUpdateStatus, deviceUpdates
}

func updateServerSideApplicationStatus(device, oldDevice *api.Device) (bool, ResourceUpdates) {
	deviceUpdates := ResourceUpdates{}
	oldStatus := api.ApplicationsSummaryStatusUnknown
	if oldDevice != nil && oldDevice.Status != nil {
		oldStatus = oldDevice.Status.ApplicationsSummary.Status
	}
	lastApplicationSummaryStatus := device.Status.ApplicationsSummary.Status
	if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusUnknown
		device.Status.ApplicationsSummary.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
		if oldStatus != device.Status.ApplicationsSummary.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceDisconnected,
				UpdateDetails: *device.Status.ApplicationsSummary.Info,
			})
		}
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus, deviceUpdates
	}
	if device.IsRebooting() && len(device.Status.Applications) > 0 {
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
		device.Status.ApplicationsSummary.Info = lo.ToPtr(DeviceStatusInfoRebooting)
		if oldStatus != device.Status.ApplicationsSummary.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceApplicationDegraded,
				UpdateDetails: *device.Status.ApplicationsSummary.Info,
			})
		}
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus, deviceUpdates
	}

	appErrors := []string{}
	appDegradations := []string{}

	for _, app := range device.Status.Applications {
		switch app.Status {
		case api.ApplicationStatusError:
			appErrors = append(appErrors, fmt.Sprintf("%s is in status %s", app.Name, string(app.Status)))
		case api.ApplicationStatusPreparing, api.ApplicationStatusStarting:
			appDegradations = append(appDegradations, fmt.Sprintf("%s is in status %s", app.Name, string(app.Status)))
		}
	}
	switch {
	case len(device.Status.Applications) == 0:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
		device.Status.ApplicationsSummary.Info = lo.ToPtr(ApplicationStatusInfoUndefined)
		if oldStatus != device.Status.ApplicationsSummary.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceApplicationHealthy,
				UpdateDetails: *device.Status.ApplicationsSummary.Info,
			})
		}
	case len(appErrors) > 0:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusError
		device.Status.ApplicationsSummary.Info = lo.ToPtr(strings.Join(appErrors, ", "))
		if oldStatus != device.Status.ApplicationsSummary.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceApplicationError,
				UpdateDetails: *device.Status.ApplicationsSummary.Info,
			})
		}
	case len(appDegradations) > 0:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
		device.Status.ApplicationsSummary.Info = lo.ToPtr(strings.Join(appDegradations, ", "))
		if oldStatus != device.Status.ApplicationsSummary.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceApplicationDegraded,
				UpdateDetails: *device.Status.ApplicationsSummary.Info,
			})
		}
	default:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
		device.Status.ApplicationsSummary.Info = lo.ToPtr(ApplicationStatusInfoHealthy)
		if oldStatus != device.Status.ApplicationsSummary.Status {
			deviceUpdates = append(deviceUpdates, ResourceUpdate{
				Reason:        api.DeviceApplicationHealthy,
				UpdateDetails: *device.Status.ApplicationsSummary.Info,
			})
		}
	}
	return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus, deviceUpdates
}

// do not overwrite valid service-side statuses with placeholder device-side status
func KeepDBDeviceStatus(device, dbDevice *api.Device) {
	if device.Status.Summary.Status == api.DeviceSummaryStatusUnknown {
		device.Status.Summary.Status = dbDevice.Status.Summary.Status
	}
	if device.Status.Lifecycle.Status == api.DeviceLifecycleStatusUnknown {
		device.Status.Lifecycle.Status = dbDevice.Status.Lifecycle.Status
	}
	if device.Status.Updated.Status == api.DeviceUpdatedStatusUnknown {
		device.Status.Updated.Status = dbDevice.Status.Updated.Status
	}
	if device.Status.ApplicationsSummary.Status == api.ApplicationsSummaryStatusUnknown {
		device.Status.ApplicationsSummary.Status = dbDevice.Status.ApplicationsSummary.Status
	}
}
