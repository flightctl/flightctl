package common

import "github.com/flightctl/flightctl/internal/domain"

// MergeStatusConditions copies existing conditions and applies each update via
// domain.SetStatusCondition, OR-aggregating whether anything changed.
func MergeStatusConditions(existing []domain.Condition, updates []domain.Condition) (merged []domain.Condition, changed bool) {
	merged = make([]domain.Condition, len(existing))
	copy(merged, existing)
	for _, update := range updates {
		if domain.SetStatusCondition(&merged, update) {
			changed = true
		}
	}
	return merged, changed
}
