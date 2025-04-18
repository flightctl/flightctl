package v1alpha1

func NewDeviceStatus() DeviceStatus {
	return DeviceStatus{
		Conditions: []Condition{
			{
				Type:   ConditionTypeDeviceUpdating,
				Status: ConditionStatusUnknown,
			},
		},
		Applications: []DeviceApplicationStatus{},
		ApplicationsSummary: DeviceApplicationsSummaryStatus{
			Status: ApplicationsSummaryStatusUnknown,
		},
		Integrity: DeviceIntegrityStatus{
			Status: DeviceIntegrityStatusUnknown,
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
		Lifecycle: DeviceLifecycleStatus{
			Status: DeviceLifecycleStatusUnknown,
		},
	}
}
