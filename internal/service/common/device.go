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
)

func UpdateServiceSideStatus(ctx context.Context, st store.Store, log logrus.FieldLogger, orgId uuid.UUID, device *api.Device) bool {
	if device == nil {
		return false
	}
	if device.Status == nil {
		status := api.NewDeviceStatus()
		device.Status = &status
	}

	deviceStatusChanged := updateServerSideDeviceStatus(device)
	updatedStatusChanged := updateServerSideDeviceUpdatedStatus(ctx, st, log, orgId, device)
	applicationStatusChanged := updateServerSideApplicationStatus(device)
	lifecycleStatusChanged := updateServerSideLifecycleStatus(device)
	return deviceStatusChanged || updatedStatusChanged || applicationStatusChanged || lifecycleStatusChanged
}

func updateServerSideDeviceStatus(device *api.Device) bool {
	lastDeviceStatus := device.Status.Summary.Status
	if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
		device.Status.Summary.Status = api.DeviceSummaryStatusUnknown
		device.Status.Summary.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
		return device.Status.Summary.Status != lastDeviceStatus
	}
	if device.IsRebooting() {
		device.Status.Summary.Status = api.DeviceSummaryStatusRebooting
		device.Status.Summary.Info = lo.ToPtr(DeviceStatusInfoRebooting)
		return device.Status.Summary.Status != lastDeviceStatus
	}

	resourceErrors := []string{}
	resourceDegradations := []string{}
	switch device.Status.Resources.Cpu {
	case api.DeviceResourceStatusCritical:
		resourceErrors = append(resourceErrors, "CPU utilization reached critical level") // TODO: add current threshold (>X% for more than Y minutes)
	case api.DeviceResourceStatusWarning:
		resourceDegradations = append(resourceDegradations, "CPU utilization reached warning level") // TODO: add current threshold (>X% for more than Y minutes)
	}
	switch device.Status.Resources.Memory {
	case api.DeviceResourceStatusCritical:
		resourceErrors = append(resourceErrors, "Memory utilization reached critical level") // TODO: add current threshold (>X% for more than Y minutes)
	case api.DeviceResourceStatusWarning:
		resourceDegradations = append(resourceDegradations, "Memory utilization reached warning level") // TODO: add current threshold (>X% for more than Y minutes)
	}
	switch device.Status.Resources.Disk {
	case api.DeviceResourceStatusCritical:
		resourceErrors = append(resourceErrors, "Disk utilization reached critical level") // TODO: add current threshold (>X% for more than Y minutes)
	case api.DeviceResourceStatusWarning:
		resourceDegradations = append(resourceDegradations, "Disk utilization reached warning level") // TODO: add current threshold (>X% for more than Y minutes)
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
	return device.Status.Summary.Status != lastDeviceStatus
}

func updateServerSideLifecycleStatus(device *api.Device) bool {
	lastLifecycleStatus := device.Status.Lifecycle.Status
	lastLifecycleInfo := device.Status.Lifecycle.Info

	// check device-reported Conditions to see if lifecycle status needs update
	condition := api.FindStatusCondition(device.Status.Conditions, api.DeviceDecommissioning)
	if condition == nil {
		return false
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
	return device.Status.Lifecycle.Status != lastLifecycleStatus && device.Status.Lifecycle.Info != lastLifecycleInfo
}

func updateServerSideDeviceUpdatedStatus(ctx context.Context, st store.Store, log logrus.FieldLogger, orgId uuid.UUID, device *api.Device) bool {
	lastUpdateStatus := device.Status.Updated.Status
	if device.IsUpdating() {
		if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
			device.Status.Updated.Status = api.DeviceUpdatedStatusUnknown
			device.Status.Updated.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s) and had an update in progress at that time.", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
		} else {
			var agentInfoMessage string
			if updateCondition := api.FindStatusCondition(device.Status.Conditions, api.DeviceUpdating); updateCondition != nil {
				agentInfoMessage = updateCondition.Message
			}
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpdating
			device.Status.Updated.Info = lo.ToPtr(util.DefaultString(agentInfoMessage, "The device is updating to the latest device spec."))
		}
		return device.Status.Updated.Status != lastUpdateStatus
	}
	if !device.IsManaged() && !device.IsUpdatedToDeviceSpec() {
		device.Status.Updated.Status = api.DeviceUpdatedStatusOutOfDate
		device.Status.Updated.Info = lo.ToPtr("There is a newer device spec for this device.")
		return device.Status.Updated.Status != lastUpdateStatus
	}
	if device.IsManaged() {
		_, fleetName, err := util.GetResourceOwner(device.Metadata.Owner)
		if err != nil {
			log.Errorf("Failed to determine owner for device %q: %v", *device.Metadata.Name, err)
			return false
		}
		f, err := st.Fleet().Get(ctx, orgId, fleetName, store.GetWithDeviceSummary(false))
		if err != nil {
			log.Errorf("Failed to get fleet for device %q: %v", *device.Metadata.Name, err)
			return false
		}
		if device.IsUpdatedToFleetSpec(f) {
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
			device.Status.Updated.Info = lo.ToPtr("The device has been updated to the fleet's latest device spec.")
		} else {
			device.Status.Updated.Status = api.DeviceUpdatedStatusOutOfDate
			errorMessage := "The device has not yet been scheduled for update to the fleet's latest device spec."
			if device.Metadata.Annotations != nil {
				lastRolloutError, ok := (*device.Metadata.Annotations)[api.DeviceAnnotationLastRolloutError]
				if ok && lastRolloutError != "" {
					errorMessage = fmt.Sprintf("The device could not be updated to the fleet's latest device spec: %s", lastRolloutError)
				}
			}
			device.Status.Updated.Info = lo.ToPtr(errorMessage)
		}
	} else {
		device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
		device.Status.Updated.Info = lo.ToPtr("The device has been updated to the latest device spec.")
	}
	return device.Status.Updated.Status != lastUpdateStatus
}

func updateServerSideApplicationStatus(device *api.Device) bool {
	lastApplicationSummaryStatus := device.Status.ApplicationsSummary.Status
	if device.IsDisconnected(api.DeviceDisconnectedTimeout) {
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusUnknown
		device.Status.ApplicationsSummary.Info = lo.ToPtr(fmt.Sprintf("The device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-api.DeviceDisconnectedTimeout))))
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus
	}
	if device.IsRebooting() && len(device.Status.Applications) > 0 {
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
		device.Status.ApplicationsSummary.Info = lo.ToPtr(DeviceStatusInfoRebooting)
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus
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
	case len(appErrors) > 0:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusError
		device.Status.ApplicationsSummary.Info = lo.ToPtr(strings.Join(appErrors, ", "))
	case len(appDegradations) > 0:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
		device.Status.ApplicationsSummary.Info = lo.ToPtr(strings.Join(appDegradations, ", "))
	default:
		device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
		device.Status.ApplicationsSummary.Info = lo.ToPtr(ApplicationStatusInfoHealthy)
	}
	return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus
}
