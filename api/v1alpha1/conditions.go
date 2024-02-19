package v1alpha1

import (
	"github.com/flightctl/flightctl/internal/util"
)

// Adapted from apimachinery

func SetStatusConditionByError(conditions *[]Condition, conditionType ConditionType, okReason string, failReason string, err error) (changed bool) {
	newCondition := Condition{
		Type: conditionType,
	}
	if err == nil {
		newCondition.Status = ConditionStatusTrue
		newCondition.Reason = util.StrToPtr(okReason)
		newCondition.Message = util.StrToPtr(okReason)
	} else {
		newCondition.Status = ConditionStatusFalse
		newCondition.Reason = util.StrToPtr(failReason)
		newCondition.Message = util.StrToPtr(err.Error())
	}
	return SetStatusCondition(conditions, newCondition)
}

// SetStatusCondition sets the corresponding condition in conditions to newCondition and returns true
// if the conditions are changed by this call.
// conditions must be non-nil.
//  1. if the condition of the specified type already exists (all fields of the existing condition are updated to
//     newCondition, LastTransitionTime is set to now if the new status differs from the old status)
//  2. if a condition of the specified type does not exist (LastTransitionTime is set to now() if unset, and newCondition is appended)
func SetStatusCondition(conditions *[]Condition, newCondition Condition) (changed bool) {
	if conditions == nil {
		return false
	}
	existingCondition := FindStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		if newCondition.LastTransitionTime == nil {
			newCondition.LastTransitionTime = util.TimeStampStringPtr()
		}
		*conditions = append(*conditions, newCondition)
		return true
	}

	if existingCondition.Status != newCondition.Status {
		existingCondition.Status = newCondition.Status
		if newCondition.LastTransitionTime != nil {
			existingCondition.LastTransitionTime = newCondition.LastTransitionTime
		} else {
			existingCondition.LastTransitionTime = util.TimeStampStringPtr()
		}
		changed = true
	}

	if existingCondition.Reason != newCondition.Reason {
		existingCondition.Reason = newCondition.Reason
		changed = true
	}
	if existingCondition.Message != newCondition.Message {
		existingCondition.Message = newCondition.Message
		changed = true
	}
	if existingCondition.ObservedGeneration != newCondition.ObservedGeneration {
		existingCondition.ObservedGeneration = newCondition.ObservedGeneration
		changed = true
	}

	return changed
}

// RemoveStatusCondition removes the corresponding conditionType from conditions if present. Returns
// true if it was present and got removed.
// conditions must be non-nil.
func RemoveStatusCondition(conditions *[]Condition, conditionType ConditionType) (removed bool) {
	if conditions == nil || len(*conditions) == 0 {
		return false
	}
	newConditions := make([]Condition, 0, len(*conditions)-1)
	for _, condition := range *conditions {
		if condition.Type != conditionType {
			newConditions = append(newConditions, condition)
		}
	}

	removed = len(*conditions) != len(newConditions)
	*conditions = newConditions

	return removed
}

// FindStatusCondition finds the conditionType in conditions.
func FindStatusCondition(conditions []Condition, conditionType ConditionType) *Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}

	return nil
}

// IsStatusConditionTrue returns true when the conditionType is present and set to `ConditionTrue`
func IsStatusConditionTrue(conditions []Condition, conditionType ConditionType) bool {
	return IsStatusConditionPresentAndEqual(conditions, conditionType, ConditionStatusTrue)
}

// IsStatusConditionFalse returns true when the conditionType is present and set to `ConditionFalse`
func IsStatusConditionFalse(conditions []Condition, conditionType ConditionType) bool {
	return IsStatusConditionPresentAndEqual(conditions, conditionType, ConditionStatusFalse)
}

// IsStatusConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsStatusConditionPresentAndEqual(conditions []Condition, conditionType ConditionType, status ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}
