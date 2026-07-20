package device

import (
	"fmt"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

// decideAwaitingReconnect computes whether and how to clear AwaitingReconnect
// based on the device's annotations and the agent-reported rendered version.
// Parse failures are treated as version 0 to preserve historical behavior.
// When apply is false, outcome is zero-valued and must not be persisted.
func decideAwaitingReconnect(device *domain.Device, deviceReportedVersion *string) (apply bool, outcome devicestore.AwaitingReconnectOutcome) {
	annotations := util.EnsureMap(lo.FromPtr(device.Metadata.Annotations))
	waitingAnnotation, hasWaitingAnnotation := annotations[domain.DeviceAnnotationAwaitingReconnect]
	if !hasWaitingAnnotation || waitingAnnotation != "true" {
		return false, devicestore.AwaitingReconnectOutcome{}
	}

	deviceVersion := parseVersionOrZero(deviceReportedVersion)

	serviceVersion := int64(0)
	if serviceVersionStr, ok := annotations[domain.DeviceAnnotationRenderedVersion]; ok {
		serviceVersion = parseIntOrZero(serviceVersionStr)
	}

	willBeConflictPaused := deviceVersion > serviceVersion

	infoMessage := "Device is up to date"
	if willBeConflictPaused {
		deviceVersionDisplay := "unknown"
		if deviceReportedVersion != nil && *deviceReportedVersion != "" {
			deviceVersionDisplay = *deviceReportedVersion
		}
		infoMessage = fmt.Sprintf("%s (device reported version %s > device version known to service %d)", common.DeviceStatusInfoConflictPaused, deviceVersionDisplay, serviceVersion)
	}

	summaryStatus := string(domain.DeviceSummaryStatusOnline)
	if willBeConflictPaused {
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

	return true, devicestore.AwaitingReconnectOutcome{
		WasConflictPaused:     willBeConflictPaused,
		SetConflictPaused:     willBeConflictPaused,
		SummaryStatus:         summaryStatus,
		SummaryInfo:           infoMessage,
		UpdatedStatus:         updatedStatus,
		ConfigRenderedVersion: configRenderedVersion,
	}
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
