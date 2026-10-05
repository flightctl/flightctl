package selector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type subqueryTestResolver struct{}

func (subqueryTestResolver) ResolveNames(SelectorName) ([]string, error) {
	return []string{"item_id"}, nil
}

func (subqueryTestResolver) ResolveFields(SelectorName) ([]*SelectorField, error) {
	return []*SelectorField{{
		Type:     String,
		Subquery: &SubquerySelector{Template: "item_id IN (SELECT id FROM other_items WHERE tenant_id = ? AND name IN ({values}))", Args: []any{42}, MaxValues: 2, MaxValueLength: 10},
	}}, nil
}

func (subqueryTestResolver) List() []SelectorName {
	return []SelectorName{NewSelectorName("other")}
}

func TestSubqueryFieldSelector(t *testing.T) {
	fieldSelector, err := NewFieldSelector("other in (one,two)")
	require.NoError(t, err)
	query, args, err := fieldSelector.Parse(context.Background(), subqueryTestResolver{})
	require.NoError(t, err)
	require.Equal(t, "item_id IN (SELECT id FROM other_items WHERE tenant_id = ? AND name IN (?, ?))", query)
	require.Equal(t, []any{42, "one", "two"}, args)

	for _, invalid := range []string{"other=one", "other in (one,two,three)", "other in (abcdefghijk)"} {
		fieldSelector, err := NewFieldSelector(invalid)
		require.NoError(t, err)
		_, _, err = fieldSelector.Parse(context.Background(), subqueryTestResolver{})
		require.Error(t, err)
	}
}
