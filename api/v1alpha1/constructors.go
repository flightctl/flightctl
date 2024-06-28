package v1alpha1

func NewDeviceStatus() DeviceStatus {
	return DeviceStatus{
		Conditions: make(map[string]Condition),
		Applications: DeviceApplicationsStatus{
			Data: make(map[string]ApplicationStatus),
			Summary: ApplicationsSummaryStatus{
				Status: ApplicationsSummaryStatusUnknown,
			},
		},
		Integrity: DeviceIntegrityStatus{
			Summary: DeviceIntegrityStatusSummary{
				Status: DeviceIntegrityStatusUnknown,
			},
		},
		Resources: DeviceResourceStatus{
			Cpu:    DeviceResourceStatusUnknown,
			Disk:   DeviceResourceStatusUnknown,
			Memory: DeviceResourceStatusUnknown,
		},
		Updated: DeviceUpdatedStatus{
			Status: DeviceUpdatedStatusUnknown,
		},
		Summary: DeviceSummaryStatus{
			Status: DeviceSummaryStatusUnknown,
		},
	}
}
