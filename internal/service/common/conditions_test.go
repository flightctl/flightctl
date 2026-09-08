package common

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMergeStatusConditions(t *testing.T) {
	t.Run("When updates are empty it should report unchanged", func(t *testing.T) {
		existing := []domain.Condition{{Type: "A", Status: domain.ConditionStatusTrue}}
		merged, changed := MergeStatusConditions(existing, nil)
		require.False(t, changed)
		require.Equal(t, existing, merged)
		require.NotSame(t, &existing[0], &merged[0])
	})

	t.Run("When a new condition type is added it should report changed", func(t *testing.T) {
		existing := []domain.Condition{{Type: "A", Status: domain.ConditionStatusTrue}}
		merged, changed := MergeStatusConditions(existing, []domain.Condition{
			{Type: "B", Status: domain.ConditionStatusFalse, Reason: "r"},
		})
		require.True(t, changed)
		require.Len(t, merged, 2)
	})

	t.Run("When the same condition is reapplied it should report unchanged", func(t *testing.T) {
		existing := []domain.Condition{{Type: "A", Status: domain.ConditionStatusTrue, Reason: "ok", Message: "ok"}}
		merged, changed := MergeStatusConditions(existing, []domain.Condition{
			{Type: "A", Status: domain.ConditionStatusTrue, Reason: "ok", Message: "ok"},
		})
		require.False(t, changed)
		require.Equal(t, existing, merged)
	})

	t.Run("When one of multiple updates changes it should report changed", func(t *testing.T) {
		existing := []domain.Condition{
			{Type: "A", Status: domain.ConditionStatusTrue, Reason: "ok"},
			{Type: "B", Status: domain.ConditionStatusFalse, Reason: "old"},
		}
		merged, changed := MergeStatusConditions(existing, []domain.Condition{
			{Type: "A", Status: domain.ConditionStatusTrue, Reason: "ok"},
			{Type: "B", Status: domain.ConditionStatusFalse, Reason: "new"},
		})
		require.True(t, changed)
		require.Equal(t, "new", domain.FindStatusCondition(merged, "B").Reason)
	})
}
