package device

import (
	"fmt"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type awaitingReconnectOutcome struct {
	ConflictPaused        bool
	SummaryStatus         string
	SummaryInfo           string
	UpdatedStatus         string
	ConfigRenderedVersion string
}

// decideAwaitingReconnect computes whether and how to clear AwaitingReconnect
// based on the device's annotations and the agent-reported rendered version.
// Parse failures are treated as version 0 to preserve historical behavior.
// When apply is false, outcome is zero-valued and must not be persisted.
func decideAwaitingReconnect(device *domain.Device, deviceReportedVersion *string) (apply bool, outcome awaitingReconnectOutcome) {
	annotations := util.EnsureMap(lo.FromPtr(device.Metadata.Annotations))
	waitingAnnotation, hasWaitingAnnotation := annotations[domain.DeviceAnnotationAwaitingReconnect]
	if !hasWaitingAnnotation || waitingAnnotation != "true" {
		return false, awaitingReconnectOutcome{}
	}

	deviceVersion := parseVersionOrZero(deviceReportedVersion)

	serviceVersion := int64(0)
	if serviceVersionStr, ok := annotations[domain.DeviceAnnotationRenderedVersion]; ok {
		serviceVersion = parseIntOrZero(serviceVersionStr)
	}

	willBeConflictPaused := deviceVersion > serviceVersion

	infoMessage := "Device is up to date"
	summaryStatus := string(domain.DeviceSummaryStatusOnline)
	if willBeConflictPaused {
		deviceVersionDisplay := "unknown"
		if deviceReportedVersion != nil && *deviceReportedVersion != "" {
			deviceVersionDisplay = *deviceReportedVersion
		}
		infoMessage = fmt.Sprintf("%s (device reported version %s > device version known to service %d)", common.DeviceStatusInfoConflictPaused, deviceVersionDisplay, serviceVersion)
		summaryStatus = string(domain.DeviceSummaryStatusConflictPaused)
	}

	updatedStatus := string(domain.DeviceUpdatedStatusOutOfDate)
	if deviceVersion == serviceVersion {
		updatedStatus = string(domain.DeviceUpdatedStatusUpToDate)
	}

	configRenderedVersion := "0"
	if deviceReportedVersion != nil && *deviceReportedVersion != "" {
		configRenderedVersion = *deviceReportedVersion
	}

	return true, awaitingReconnectOutcome{
		ConflictPaused:        willBeConflictPaused,
		SummaryStatus:         summaryStatus,
		SummaryInfo:           infoMessage,
		UpdatedStatus:         updatedStatus,
		ConfigRenderedVersion: configRenderedVersion,
	}
}

func applyAwaitingReconnectOutcome(device *domain.Device, outcome awaitingReconnectOutcome) {
	annotations := util.EnsureMap(lo.FromPtr(device.Metadata.Annotations))
	delete(annotations, domain.DeviceAnnotationAwaitingReconnect)
	if outcome.ConflictPaused {
		annotations[domain.DeviceAnnotationConflictPaused] = "true"
	}
	device.Metadata.Annotations = &annotations

	if device.Status == nil {
		status := domain.NewDeviceStatus()
		device.Status = &status
	}
	device.Status.Summary.Status = domain.DeviceSummaryStatusType(outcome.SummaryStatus)
	device.Status.Summary.Info = lo.ToPtr(outcome.SummaryInfo)
	device.Status.Updated.Status = domain.DeviceUpdatedStatusType(outcome.UpdatedStatus)
	device.Status.Config.RenderedVersion = outcome.ConfigRenderedVersion
}

func parseVersionOrZero(version *string) int64 {
	if version == nil || *version == "" {
		return 0
	}
	return parseIntOrZero(*version)
}

func parseIntOrZero(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
