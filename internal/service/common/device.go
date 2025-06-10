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
	CPUIsCritical                  = "CPU utilization reached critical level"
	CPUIsWarning                   = "CPU utilization reached warning level"
	CPUIsNormal                    = "CPU utilization returned to a normal level"
	MemoryIsCritical               = "Memory utilization reached critical level"
	MemoryIsWarning                = "Memory utilization reached warning level"
	MemoryIsNormal                 = "Memory utilization returned to a normal level"
	DiskIsCritical                 = "Disk utilization reached critical level"
	DiskIsWarning                  = "Disk utilization reached warning level"
	DiskIsNormal                   = "Disk utilization returned to a normal level"
)

type ResourceUpdate struct {
	Reason        api.EventReason
	UpdateDetails api.ResourceUpdatedDetails
}
type ResourceUpdates []ResourceUpdate

func UpdateServiceSideStatus(ctx context.Context, st store.Store, log logrus.FieldLogger, orgId uuid.UUID, device *api.Device) ResourceUpdates {
	if device == nil {
		return nil
	}
	if device.Status == nil {
		device.Status = lo.ToPtr(api.NewDeviceStatus())
	}

	updates := ResourceUpdates{}

	if updated, deviceUpdate := updateServerSideDeviceUpdatedStatus(ctx, st, log, orgId, device); updated {
		updates = append(updates, deviceUpdate)
	}

	if updated, deviceUpdates := updateServerSideDeviceStatus(device); updated {
		updates = append(updates, deviceUpdates...)
	}

	if updated, deviceUpdate := updateServerSideApplicationStatus(device); updated {
		updates = append(updates, deviceUpdate)
	}

	if updated, deviceUpdate := updateServerSideLifecycleStatus(device); updated {
		updates = append(updates, deviceUpdate)
	}

	return updates
}

func updateServerSideDeviceStatus(device *api.Device) (bool, ResourceUpdates) {
	var deviceUpdates ResourceUpdates
	lastDeviceStatus := device.Status.Summary.Status
	if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
		device.Status.Summary.Status = api.DeviceSummaryStatusUnknown
		device.Status.Summary.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceDisconnected,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Summary.Info)}},
		})
		return device.Status.Summary.Status != lastDeviceStatus, deviceUpdates
	}
	if device.IsRebooting() {
		device.Status.Summary.Status = api.DeviceSummaryStatusRebooting
		device.Status.Summary.Info = lo.ToPtr(DeviceStatusInfoRebooting)
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceDisconnected,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Summary.Info)}},
		})
		return device.Status.Summary.Status != lastDeviceStatus, deviceUpdates
	}

	resourceErrors := []string{}
	resourceDegradations := []string{}

	switch device.Status.Resources.Cpu {
	case api.DeviceResourceStatusCritical:
		resourceErrors = append(resourceErrors, CPUIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceCPUCritical,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{CPUIsCritical}},
		})
	case api.DeviceResourceStatusWarning:
		resourceDegradations = append(resourceDegradations, CPUIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceCPUWarning,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{CPUIsWarning}},
		})
	default:
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceCPUNormal,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{CPUIsNormal}},
		})
	}
	switch device.Status.Resources.Memory {
	case api.DeviceResourceStatusCritical:
		resourceErrors = append(resourceErrors, MemoryIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceMemoryCritical,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{MemoryIsCritical}},
		})
	case api.DeviceResourceStatusWarning:
		resourceDegradations = append(resourceDegradations, MemoryIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceMemoryWarning,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{MemoryIsWarning}},
		})
	default:
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceMemoryNormal,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{MemoryIsNormal}},
		})
	}
	switch device.Status.Resources.Disk {
	case api.DeviceResourceStatusCritical:
		resourceErrors = append(resourceErrors, DiskIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceDiskCritical,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{DiskIsCritical}},
		})
	case api.DeviceResourceStatusWarning:
		resourceDegradations = append(resourceDegradations, DiskIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceDiskWarning,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{DiskIsWarning}},
		})
	default:
		deviceUpdates = append(deviceUpdates, ResourceUpdate{
			Reason:        api.DeviceDiskNormal,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{DiskIsNormal}},
		})
	}

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

func updateServerSideLifecycleStatus(device *api.Device) (bool, ResourceUpdate) {
	var deviceUpdate ResourceUpdate
	lastLifecycleStatus := device.Status.Lifecycle.Status
	lastLifecycleInfo := device.Status.Lifecycle.Info

	// check device-reported Conditions to see if lifecycle status needs update
	condition := api.FindStatusCondition(device.Status.Conditions, api.DeviceDecommissioning)
	if condition == nil {
		return false, deviceUpdate
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
	return device.Status.Lifecycle.Status != lastLifecycleStatus && device.Status.Lifecycle.Info != lastLifecycleInfo, deviceUpdate
}

func updateServerSideDeviceUpdatedStatus(ctx context.Context, st store.Store, log logrus.FieldLogger, orgId uuid.UUID, device *api.Device) (bool, ResourceUpdate) {
	var deviceUpdate ResourceUpdate
	lastUpdateStatus := device.Status.Updated.Status
	if device.IsUpdating() {
		if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
			device.Status.Updated.Status = api.DeviceUpdatedStatusUnknown
			device.Status.Updated.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s) and had an update in progress at that time.", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
			deviceUpdate = ResourceUpdate{
				Reason:        api.DeviceDisconnected,
				UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Updated.Info)}},
			}
		} else {
			var agentInfoMessage string
			if updateCondition := api.FindStatusCondition(device.Status.Conditions, api.DeviceUpdating); updateCondition != nil {
				agentInfoMessage = updateCondition.Message
			}
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpdating
			device.Status.Updated.Info = lo.ToPtr(util.DefaultString(agentInfoMessage, "The device is updating to the latest device spec."))
			deviceUpdate = ResourceUpdate{
				Reason:        api.DeviceContentUpdating,
				UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Updated.Info)}},
			}
		}
		return device.Status.Updated.Status != lastUpdateStatus, deviceUpdate
	}
	if !device.IsManaged() && !device.IsUpdatedToDeviceSpec() {
		device.Status.Updated.Status = api.DeviceUpdatedStatusOutOfDate
		device.Status.Updated.Info = lo.ToPtr("There is a newer device spec for this device.")
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceContentOutOfDate,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Updated.Info)}},
		}
		return device.Status.Updated.Status != lastUpdateStatus, deviceUpdate
	}
	if device.IsManaged() {
		_, fleetName, err := util.GetResourceOwner(device.Metadata.Owner)
		if err != nil {
			log.Errorf("Failed to determine owner for device %q: %v", *device.Metadata.Name, err)
			return false, deviceUpdate
		}
		f, err := st.Fleet().Get(ctx, orgId, fleetName, store.GetWithDeviceSummary(false))
		if err != nil {
			log.Errorf("Failed to get fleet for device %q: %v", *device.Metadata.Name, err)
			return false, deviceUpdate
		}
		if device.IsUpdatedToFleetSpec(f) {
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
			device.Status.Updated.Info = lo.ToPtr("The device has been updated to the fleet's latest device spec.")
			deviceUpdate = ResourceUpdate{
				Reason:        api.DeviceContentUpToDate,
				UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Updated.Info)}},
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
			deviceUpdate = ResourceUpdate{
				Reason:        api.DeviceContentOutOfDate,
				UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Updated.Info)}},
			}
		}
	} else {
		device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
		device.Status.Updated.Info = lo.ToPtr("The device has been updated to the latest device spec.")
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceContentUpToDate,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.Updated.Info)}},
		}
	}
	return device.Status.Updated.Status != lastUpdateStatus, deviceUpdate
}

func updateServerSideApplicationStatus(device *api.Device) (bool, ResourceUpdate) {
	var deviceUpdate ResourceUpdate
	lastApplicationSummaryStatus := device.Status.ApplicationsSummary.Status
	if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusUnknown
		device.Status.ApplicationsSummary.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceDisconnected,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.ApplicationsSummary.Info)}},
		}
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus, deviceUpdate
	}
	if device.IsRebooting() && len(device.Status.Applications) > 0 {
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
		device.Status.ApplicationsSummary.Info = lo.ToPtr(DeviceStatusInfoRebooting)
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceApplicationDegraded,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.ApplicationsSummary.Info)}},
		}
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus, deviceUpdate
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
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceApplicationHealthy,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.ApplicationsSummary.Info)}},
		}
	case len(appErrors) > 0:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusError
		device.Status.ApplicationsSummary.Info = lo.ToPtr(strings.Join(appErrors, ", "))
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceApplicationError,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.ApplicationsSummary.Info)}},
		}
	case len(appDegradations) > 0:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
		device.Status.ApplicationsSummary.Info = lo.ToPtr(strings.Join(appDegradations, ", "))
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceApplicationDegraded,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.ApplicationsSummary.Info)}},
		}
	default:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
		device.Status.ApplicationsSummary.Info = lo.ToPtr(ApplicationStatusInfoHealthy)
		deviceUpdate = ResourceUpdate{
			Reason:        api.DeviceApplicationHealthy,
			UpdateDetails: api.ResourceUpdatedDetails{UpdatedFields: []api.ResourceUpdatedDetailsUpdatedFields{api.ResourceUpdatedDetailsUpdatedFields(*device.Status.ApplicationsSummary.Info)}},
		}
	}
	return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus, deviceUpdate
}
