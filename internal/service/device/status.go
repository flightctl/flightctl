package device

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

func UpdateServiceSideStatus(ctx context.Context, orgId uuid.UUID, device *domain.Device, fleets fleet.Service, log logrus.FieldLogger) bool {
	if device == nil {
		return false
	}
	if device.Status == nil {
		device.Status = lo.ToPtr(domain.NewDeviceStatus())
	}

	deviceStatusChanged := updateServerSideDeviceStatus(device)
	updatedStatusChanged := updateServerSideDeviceUpdatedStatus(device, ctx, fleets, log, orgId)
	applicationStatusChanged := updateServerSideApplicationStatus(device)
	lifecycleStatusChanged := updateServerSideLifecycleStatus(device)

	return deviceStatusChanged || updatedStatusChanged || applicationStatusChanged || lifecycleStatusChanged
}

func resourcesCpu(cpu domain.DeviceResourceStatusType, resourceErrors *[]string, resourceDegradations *[]string) {
	switch cpu {
	case domain.DeviceResourceStatusCritical:
		*resourceErrors = append(*resourceErrors, common.CPUIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
	case domain.DeviceResourceStatusWarning:
		*resourceDegradations = append(*resourceDegradations, common.CPUIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
	}
}

func resourcesMemory(memory domain.DeviceResourceStatusType, resourceErrors *[]string, resourceDegradations *[]string) {
	switch memory {
	case domain.DeviceResourceStatusCritical:
		*resourceErrors = append(*resourceErrors, common.MemoryIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
	case domain.DeviceResourceStatusWarning:
		*resourceDegradations = append(*resourceDegradations, common.MemoryIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
	}
}

func resourcesDisk(disk domain.DeviceResourceStatusType, resourceErrors *[]string, resourceDegradations *[]string) {
	switch disk {
	case domain.DeviceResourceStatusCritical:
		*resourceErrors = append(*resourceErrors, common.DiskIsCritical) // TODO: add current threshold (>X% for more than Y minutes)
	case domain.DeviceResourceStatusWarning:
		*resourceDegradations = append(*resourceDegradations, common.DiskIsWarning) // TODO: add current threshold (>X% for more than Y minutes)
	}
}

func updateServerSideDeviceStatus(device *domain.Device) bool {
	lastDeviceStatus := device.Status.Summary.Status

	// Check for special annotations first - these take precedence over ALL other status checks
	annotations := lo.FromPtr(device.Metadata.Annotations)

	// AwaitingReconnect annotation takes highest precedence - overrides everything
	if annotations[domain.DeviceAnnotationAwaitingReconnect] == "true" {
		device.Status.Summary.Status = domain.DeviceSummaryStatusAwaitingReconnect
		device.Status.Summary.Info = lo.ToPtr(common.DeviceStatusInfoAwaitingReconnect)
		return device.Status.Summary.Status != lastDeviceStatus
	}

	// ConflictPaused annotation takes second highest precedence
	if annotations[domain.DeviceAnnotationConflictPaused] == "true" {
		device.Status.Summary.Status = domain.DeviceSummaryStatusConflictPaused
		device.Status.Summary.Info = lo.ToPtr(common.DeviceStatusInfoConflictPaused)
		return device.Status.Summary.Status != lastDeviceStatus
	}

	// Standard status checks follow normal priority order
	if device.IsDisconnected(domain.DeviceDisconnectedTimeout) {
		device.Status.Summary.Status = domain.DeviceSummaryStatusUnknown
		device.Status.Summary.Info = lo.ToPtr(fmt.Sprintf("Device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-domain.DeviceDisconnectedTimeout))))
		return device.Status.Summary.Status != lastDeviceStatus
	}
	if device.IsRebooting() {
		device.Status.Summary.Status = domain.DeviceSummaryStatusRebooting
		device.Status.Summary.Info = lo.ToPtr(common.DeviceStatusInfoRebooting)
		return device.Status.Summary.Status != lastDeviceStatus
	}

	resourceErrors := []string{}
	resourceDegradations := []string{}
	resourcesCpu(device.Status.Resources.Cpu, &resourceErrors, &resourceDegradations)
	resourcesMemory(device.Status.Resources.Memory, &resourceErrors, &resourceDegradations)
	resourcesDisk(device.Status.Resources.Disk, &resourceErrors, &resourceDegradations)

	switch {
	case len(resourceErrors) > 0:
		device.Status.Summary.Status = domain.DeviceSummaryStatusError
		device.Status.Summary.Info = lo.ToPtr(strings.Join(resourceErrors, ", "))
	case len(resourceDegradations) > 0:
		device.Status.Summary.Status = domain.DeviceSummaryStatusDegraded
		device.Status.Summary.Info = lo.ToPtr(strings.Join(resourceDegradations, ", "))
	default:
		device.Status.Summary.Status = domain.DeviceSummaryStatusOnline
		device.Status.Summary.Info = lo.ToPtr(common.DeviceStatusInfoHealthy)
	}
	return device.Status.Summary.Status != lastDeviceStatus
}

func updateServerSideLifecycleStatus(device *domain.Device) bool {
	lastLifecycleStatus := device.Status.Lifecycle.Status
	lastLifecycleInfo := device.Status.Lifecycle.Info

	// check device-reported Conditions to see if lifecycle status needs update
	condition := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceDecommissioning)
	if condition == nil {
		return false
	}

	if condition.IsDecomError() {
		device.Status.Lifecycle = domain.DeviceLifecycleStatus{
			Info:   lo.ToPtr("Device has errored while decommissioning"),
			Status: domain.DeviceLifecycleStatusDecommissioned,
		}
	}

	if condition.IsDecomComplete() {
		device.Status.Lifecycle = domain.DeviceLifecycleStatus{
			Info:   lo.ToPtr("Device has completed decommissioning"),
			Status: domain.DeviceLifecycleStatusDecommissioned,
		}
	}

	if condition.IsDecomStarted() {
		device.Status.Lifecycle = domain.DeviceLifecycleStatus{
			Info:   lo.ToPtr("Device has acknowledged decommissioning request"),
			Status: domain.DeviceLifecycleStatusDecommissioning,
		}
	}
	return device.Status.Lifecycle.Status != lastLifecycleStatus && device.Status.Lifecycle.Info != lastLifecycleInfo
}

func updateServerSideDeviceUpdatedStatus(device *domain.Device, ctx context.Context, fleets fleet.Service, log logrus.FieldLogger, orgId uuid.UUID) bool {
	lastUpdateStatus := device.Status.Updated.Status
	if device.IsDisconnected(domain.DeviceDisconnectedTimeout) && device.Status != nil && device.Status.LastSeen != nil && !device.Status.LastSeen.IsZero() {
		device.Status.Updated.Status = domain.DeviceUpdatedStatusUnknown
		device.Status.Updated.Info = lo.ToPtr(fmt.Sprintf("Device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-domain.DeviceDisconnectedTimeout))))
		return device.Status.Updated.Status != lastUpdateStatus
	}
	if device.IsUpdating() {
		var agentInfoMessage string
		if updateCondition := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceUpdating); updateCondition != nil {
			agentInfoMessage = updateCondition.Message
		}
		device.Status.Updated.Status = domain.DeviceUpdatedStatusUpdating
		device.Status.Updated.Info = lo.ToPtr(util.DefaultString(agentInfoMessage, "Device is updating to the latest device spec."))
		return device.Status.Updated.Status != lastUpdateStatus
	}
	if !device.IsManaged() && !device.IsUpdatedToDeviceSpec() {
		device.Status.Updated.Status = domain.DeviceUpdatedStatusOutOfDate
		baseMessage := domain.DeviceOutOfDateText
		var errorMessage string

		// Prefer update condition error if available
		if updateCondition := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceUpdating); updateCondition != nil {
			if updateCondition.Reason == string(domain.UpdateStateError) && updateCondition.Message != "" {
				errorMessage = fmt.Sprintf("%s: %s", baseMessage, updateCondition.Message)
			}
		}

		// Final fallback to base message (skip rollout error check since unmanaged devices don't have rollout errors)
		if errorMessage == "" {
			errorMessage = baseMessage + "."
		}

		device.Status.Updated.Info = lo.ToPtr(errorMessage)
		return device.Status.Updated.Status != lastUpdateStatus
	}
	if device.IsManaged() {
		_, fleetName, err := util.GetResourceOwner(device.Metadata.Owner)
		if err != nil {
			log.Errorf("Failed to determine owner for device %q: %v", *device.Metadata.Name, err)
			return false
		}
		f, status := fleets.GetFleet(ctx, orgId, fleetName, domain.GetFleetParams{})
		if status.Code != http.StatusOK {
			log.Errorf("Failed to get fleet for device %q: %v", *device.Metadata.Name, status.Message)
			return false
		}
		if device.IsUpdatedToFleetSpec(f) {
			device.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
			device.Status.Updated.Info = lo.ToPtr("Device was updated to the fleet's latest device spec.")
		} else {
			device.Status.Updated.Status = domain.DeviceUpdatedStatusOutOfDate

			var errorMessage string
			baseMessage := "Device could not be updated to the fleet's latest device spec"
			if updateCondition := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceUpdating); updateCondition != nil {
				if updateCondition.Reason == string(domain.UpdateStateError) {
					errorMessage = fmt.Sprintf("%s: %s", baseMessage, updateCondition.Message)
				}
			} else if device.Metadata.Annotations != nil {
				if lastRolloutError, ok := (*device.Metadata.Annotations)[domain.DeviceAnnotationLastRolloutError]; ok && lastRolloutError != "" {
					errorMessage = fmt.Sprintf("%s: %s", baseMessage, lastRolloutError)
				}
			}
			if errorMessage == "" {
				errorMessage = domain.DeviceOutOfSyncWithFleetText
			}
			device.Status.Updated.Info = lo.ToPtr(errorMessage)
		}
	} else {
		device.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
		device.Status.Updated.Info = lo.ToPtr("Device was updated to the latest device spec.")
	}

	// Override UpToDate if the actual booted OS image doesn't match the desired spec.
	if device.Status.Updated.Status == domain.DeviceUpdatedStatusUpToDate &&
		device.Spec != nil && device.Spec.Os != nil && device.Spec.Os.Image != "" &&
		device.Status.Os.Image != "" && device.Status.Os.Image != device.Spec.Os.Image {
		device.Status.Updated.Status = domain.DeviceUpdatedStatusOutOfDate
		device.Status.Updated.Info = lo.ToPtr(fmt.Sprintf("Device OS image mismatch: running %q, expected %q.", device.Status.Os.Image, device.Spec.Os.Image))
	}

	return device.Status.Updated.Status != lastUpdateStatus
}

func updateServerSideApplicationStatus(device *domain.Device) bool {
	lastApplicationSummaryStatus := device.Status.ApplicationsSummary.Status
	if device.IsDisconnected(domain.DeviceDisconnectedTimeout) {
		device.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusUnknown
		device.Status.ApplicationsSummary.Info = lo.ToPtr(fmt.Sprintf("Device is disconnected (last seen more than %s).", humanize.Time(time.Now().Add(-domain.DeviceDisconnectedTimeout))))
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus
	}
	if device.IsRebooting() && len(device.Status.Applications) > 0 {
		device.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusDegraded
		device.Status.ApplicationsSummary.Info = lo.ToPtr(common.DeviceStatusInfoRebooting)
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus
	}
	if len(device.Status.Applications) == 0 {
		device.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusNoApplications
		device.Status.ApplicationsSummary.Info = lo.ToPtr(common.ApplicationStatusInfoUndefined)
		return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus
	}
	if device.Status.ApplicationsSummary.Status == domain.ApplicationsSummaryStatusHealthy &&
		device.Status.ApplicationsSummary.Info == nil {
		device.Status.ApplicationsSummary.Info = lo.ToPtr(common.ApplicationStatusInfoHealthy)
	}

	return device.Status.ApplicationsSummary.Status != lastApplicationSummaryStatus
}
