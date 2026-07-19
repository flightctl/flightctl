package common

import (
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
)

const ConditionUpdateRetryIterations = 10

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

// RetryOnNoRowsUpdated runs fn until it succeeds or returns a non-CAS error.
func RetryOnNoRowsUpdated(fn func() error) error {
	var err error
	for i := 0; i < ConditionUpdateRetryIterations; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !errors.Is(err, flterrors.ErrNoRowsUpdated) {
			return err
		}
	}
	return err
}
