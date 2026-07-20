package restore

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
)

// DeviceAwaitingReconnectPrepareParams carries product outcomes for bulk
// post-restore device awaiting-reconnect preparation.
type DeviceAwaitingReconnectPrepareParams struct {
	AnnotationKey             string
	SummaryStatus             string
	SummaryInfo               string
	UpdatedStatus             string
	ExcludedLifecycleStatuses []string
}

// EnrollmentAwaitingReconnectPrepareParams carries product outcomes for bulk
// post-restore enrollment-request awaiting-reconnect annotation.
type EnrollmentAwaitingReconnectPrepareParams struct {
	AnnotationKey string
}

// NewDeviceAwaitingReconnectPrepareParams returns the product constants used
// when marking devices awaiting reconnect after restore.
func NewDeviceAwaitingReconnectPrepareParams() DeviceAwaitingReconnectPrepareParams {
	return DeviceAwaitingReconnectPrepareParams{
		AnnotationKey: domain.DeviceAnnotationAwaitingReconnect,
		SummaryStatus: string(domain.DeviceSummaryStatusAwaitingReconnect),
		SummaryInfo:   common.DeviceStatusInfoAwaitingReconnect,
		UpdatedStatus: string(domain.DeviceUpdatedStatusUnknown),
		ExcludedLifecycleStatuses: []string{
			string(domain.DeviceLifecycleStatusDecommissioned),
			string(domain.DeviceLifecycleStatusDecommissioning),
		},
	}
}

// NewEnrollmentAwaitingReconnectPrepareParams returns the product constants used
// when marking enrollment requests awaiting reconnect after restore.
func NewEnrollmentAwaitingReconnectPrepareParams() EnrollmentAwaitingReconnectPrepareParams {
	return EnrollmentAwaitingReconnectPrepareParams{
		AnnotationKey: domain.DeviceAnnotationAwaitingReconnect,
	}
}
