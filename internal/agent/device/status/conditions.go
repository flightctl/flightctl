package status

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
)

var (
	DeviceConditionBootstrapReason string = "Bootstrap"
	DeviceConditionExpectedReason  string = "AsExpected"
)

func DefaultConditions() *[]v1alpha1.Condition {
	return &[]v1alpha1.Condition{
		{
			Type:   v1alpha1.DeviceProgressing,
			Status: v1alpha1.ConditionStatusTrue,
			Reason: DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceAvailable,
			Status: v1alpha1.ConditionStatusFalse,
			Reason: DeviceConditionBootstrapReason,
		},
		{
			Type:   v1alpha1.DeviceDegraded,
			Status: v1alpha1.ConditionStatusFalse,
			Reason: DeviceConditionExpectedReason,
		},
	}

}

// SetProgressingConditionByError sets the degraded condition based on the error.
func SetDegradedConditionByError(conditions *[]v1alpha1.Condition, reason string, err error) bool {
	condition := v1alpha1.Condition{Type: v1alpha1.DeviceDegraded}
	if err != nil {
		condition.Status = v1alpha1.ConditionStatusTrue
		condition.Reason = reason
		condition.Message = err.Error()
	} else {
		condition.Status = v1alpha1.ConditionStatusFalse
		condition.Reason = DeviceConditionExpectedReason
		condition.Message = "All is well"
	}

	return v1alpha1.SetStatusCondition(conditions, condition)
}

// SetProgressingCondition sets the progressing condition to true and adds the reason and message.
func SetProgressingCondition(conditions *[]v1alpha1.Condition, conditionType v1alpha1.ConditionType, conditionStatus v1alpha1.ConditionStatus, reason string, message string) bool {
	// TODO: ensure condition exists.
	condition := v1alpha1.Condition{Type: conditionType}
	condition.Status = conditionStatus
	condition.Reason = reason
	condition.Message = message

	return v1alpha1.SetStatusCondition(conditions, condition)
}
