package domain

import (
	"fmt"
	"slices"
)

// ValidateConditions validates condition lists against business rules.
// It checks that:
// - All conditions are in the allowedConditions list
// - Conditions in trueConditions list are only set to 'true'
// - No duplicate condition types exist
// - Only one of the exclusiveConditions is set at a time
func ValidateConditions(conditions []Condition, allowedConditions, trueConditions, exclusiveConditions []ConditionType) []error {
	allErrs := []error{}
	seen := make(map[ConditionType]bool)
	seenExclusives := make(map[ConditionType]bool)
	for _, c := range conditions {
		if !slices.Contains(allowedConditions, c.Type) {
			allErrs = append(allErrs, fmt.Errorf("not allowed condition type %q", c.Type))
		}
		if slices.Contains(trueConditions, c.Type) && c.Status != ConditionStatusTrue {
			allErrs = append(allErrs, fmt.Errorf("condition %q may only be set to 'true'", c.Type))
		}
		if _, exists := seen[c.Type]; exists {
			allErrs = append(allErrs, fmt.Errorf("duplicate condition type %q", c.Type))
		}
		seen[c.Type] = true
		if slices.Contains(exclusiveConditions, c.Type) {
			seenExclusives[c.Type] = true
		}
	}
	if len(seenExclusives) > 1 {
		allErrs = append(allErrs, fmt.Errorf("only one of %v may be set", exclusiveConditions))
	}
	return allErrs
}
