package v1alpha1

func NewDeviceStatus() DeviceStatus {
	return DeviceStatus{
		Conditions:   []Condition{},
		Applications: []DeviceApplicationStatus{},
		ApplicationsSummary: DeviceApplicationsSummaryStatus{
			Status: ApplicationsSummaryStatusUnknown,
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
