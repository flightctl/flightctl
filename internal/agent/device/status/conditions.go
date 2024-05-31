package status

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
)

var (
	DeviceConditionBootstrapReason string = "Bootstrap"
	DeviceConditionExpectedReason  string = "AsExpected"
)

func DefaultConditions() *[]v1alpha1.Condition {
	return &[]v1alpha1.Condition{
		{
			Type:   v1alpha1.ApplicationsCondition,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceCondition,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceCPUPressure,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceMemoryPressure,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceDiskPressure,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceDiskHealth,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.SystemIntegrityVerification,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceSystemdUnitsRunning,
			Status: v1alpha1.ConditionStatusUnknown,
			Reason: &DeviceConditionBootstrapReason,
		},
	}

}

// SetProgressingConditionByError sets the degraded condition based on the error.
func SetDegradedConditionByError(conditions *[]v1alpha1.Condition, reason string, err error) bool {
	condition := v1alpha1.Condition{Type: v1alpha1.DeviceDegraded}
	if err != nil {
		condition.Status = v1alpha1.ConditionStatusTrue
		condition.Reason = util.StrToPtr(reason)
		condition.Message = util.StrToPtr(err.Error())
	} else {
		condition.Status = v1alpha1.ConditionStatusFalse
		condition.Reason = &DeviceConditionExpectedReason
		condition.Message = util.StrToPtr("All is well")
	}

	return v1alpha1.SetStatusCondition(conditions, condition)
}

// SetProgressingCondition sets the progressing condition to true and adds the reason and message.
func SetProgressingCondition(conditions *[]v1alpha1.Condition, conditionType v1alpha1.ConditionType, conditionStatus v1alpha1.ConditionStatus, reason string, message string) bool {
	// TODO: ensure condition exists.
	condition := v1alpha1.Condition{Type: conditionType}
	condition.Status = conditionStatus
	condition.Reason = util.StrToPtr(reason)
	condition.Message = util.StrToPtr(message)

	return v1alpha1.SetStatusCondition(conditions, condition)
}
