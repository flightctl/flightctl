package imagebuilder

import (
	"time"
)

// int64PtrEqual returns true when both pointers are nil or when both are non-nil and their values are equal.
func int64PtrEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// FindImageBuildStatusCondition finds the conditionType in conditions.
func FindImageBuildStatusCondition(conditions []ImageBuildCondition, conditionType ImageBuildConditionType) *ImageBuildCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// SetImageBuildStatusCondition sets the corresponding condition in conditions to newCondition and returns true
// if the conditions are changed by this call.
// conditions must be non-nil.
//  1. if the condition of the specified type already exists (all fields of the existing condition are updated to
//     newCondition, LastTransitionTime is set to now if the new status differs from the old status)
//  2. if a condition of the specified type does not exist (LastTransitionTime is set to now() if unset, and newCondition is appended)
func SetImageBuildStatusCondition(conditions *[]ImageBuildCondition, newCondition ImageBuildCondition) (changed bool) {
	if conditions == nil {
		return false
	}
	existingCondition := FindImageBuildStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		if newCondition.LastTransitionTime.IsZero() {
			newCondition.LastTransitionTime = time.Now()
		}
		*conditions = append(*conditions, newCondition)
		return true
	}

	if existingCondition.Status != newCondition.Status {
		existingCondition.Status = newCondition.Status
		if !newCondition.LastTransitionTime.IsZero() {
			existingCondition.LastTransitionTime = newCondition.LastTransitionTime
		} else {
			existingCondition.LastTransitionTime = time.Now()
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
	// Compare ObservedGeneration values, not pointer addresses
	if !int64PtrEqual(existingCondition.ObservedGeneration, newCondition.ObservedGeneration) {
		existingCondition.ObservedGeneration = newCondition.ObservedGeneration
		changed = true
	}

	return changed
}

// FindImageExportStatusCondition finds the conditionType in conditions.
func FindImageExportStatusCondition(conditions []ImageExportCondition, conditionType ImageExportConditionType) *ImageExportCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// SetImageExportStatusCondition sets the corresponding condition in conditions to newCondition and returns true
// if the conditions are changed by this call.
// conditions must be non-nil.
//  1. if the condition of the specified type already exists (all fields of the existing condition are updated to
//     newCondition, LastTransitionTime is set to now if the new status differs from the old status)
//  2. if a condition of the specified type does not exist (LastTransitionTime is set to now() if unset, and newCondition is appended)
func SetImageExportStatusCondition(conditions *[]ImageExportCondition, newCondition ImageExportCondition) (changed bool) {
	if conditions == nil {
		return false
	}
	existingCondition := FindImageExportStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		if newCondition.LastTransitionTime.IsZero() {
			newCondition.LastTransitionTime = time.Now()
		}
		*conditions = append(*conditions, newCondition)
		return true
	}

	if existingCondition.Status != newCondition.Status {
		existingCondition.Status = newCondition.Status
		if !newCondition.LastTransitionTime.IsZero() {
			existingCondition.LastTransitionTime = newCondition.LastTransitionTime
		} else {
			existingCondition.LastTransitionTime = time.Now()
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
	// Compare ObservedGeneration values, not pointer addresses
	if !int64PtrEqual(existingCondition.ObservedGeneration, newCondition.ObservedGeneration) {
		existingCondition.ObservedGeneration = newCondition.ObservedGeneration
		changed = true
	}

	return changed
}
