package common

import (
	"errors"
	"strings"

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

func shouldRetryConditionUpdate(err error) bool {
	if errors.Is(err, flterrors.ErrNoRowsUpdated) || errors.Is(err, flterrors.ErrResourceVersionConflict) {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "deadlock")
}

// RetryOnNoRowsUpdated runs fn until it succeeds or returns a non-retryable error.
func RetryOnNoRowsUpdated(fn func() error) error {
	var err error
	for i := 0; i < ConditionUpdateRetryIterations; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !shouldRetryConditionUpdate(err) {
			return err
		}
	}
	return err
}
