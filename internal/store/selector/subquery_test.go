package selector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type subqueryTestResolver struct {
	field *SelectorField
}

func (subqueryTestResolver) ResolveNames(SelectorName) ([]string, error) {
	return []string{"item_id"}, nil
}

func (resolver subqueryTestResolver) ResolveFields(SelectorName) ([]*SelectorField, error) {
	if resolver.field != nil {
		return []*SelectorField{resolver.field}, nil
	}
	return []*SelectorField{{
		Type:     String,
		Subquery: &SubquerySelector{Template: "item_id IN (SELECT id FROM other_items WHERE tenant_id = {tenant_id} AND name IN ({values}))", Args: map[string]any{"tenant_id": 42}, MaxValues: 2, MaxValueLength: 10},
	}}, nil
}

func TestSubqueryNamedParameters(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		template string
		wantArgs []any
		wantErr  bool
	}{
		{
			name:     "When values precede named parameters it should bind in SQL order",
			template: "item_id IN (SELECT id FROM other_items WHERE name IN ({values}) AND tenant_id = {tenant_id})",
			wantArgs: []any{"one", "two", 42},
		},
		{
			name:     "When parameters surround values it should bind repeated names in SQL order",
			template: "item_id IN (SELECT id FROM other_items WHERE tenant_id = {tenant_id} AND name IN ({values}) AND owner_id = {owner_id} AND tenant_id = {tenant_id})",
			wantArgs: []any{42, "one", "two", 7, 42},
		},
		{
			name:     "When a named parameter is missing it should reject the template",
			template: "item_id IN (SELECT id FROM other_items WHERE tenant_id = {missing} AND name IN ({values}))",
			wantErr:  true,
		},
		{
			name:     "When positional parameters are used it should reject the template",
			template: "item_id IN (SELECT id FROM other_items WHERE tenant_id = ? AND name IN ({values}))",
			wantErr:  true,
		},
		{
			name:     "When values are missing it should reject the template",
			template: "item_id IN (SELECT id FROM other_items WHERE tenant_id = {tenant_id})",
			wantErr:  true,
		},
		{
			name:     "When values are repeated it should reject the template",
			template: "item_id IN (SELECT id FROM other_items WHERE name IN ({values}) OR name IN ({values}))",
			wantErr:  true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := subqueryTestResolver{field: &SelectorField{
				Type: String,
				Subquery: &SubquerySelector{
					Template:  testCase.template,
					Args:      map[string]any{"owner_id": 7, "tenant_id": 42},
					MaxValues: 2,
				},
			}}
			fieldSelector, err := NewFieldSelector("other in (one,two)")
			require.NoError(t, err)
			query, args, err := fieldSelector.Parse(context.Background(), resolver)
			if testCase.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotContains(t, query, "{")
			require.Equal(t, testCase.wantArgs, args)
		})
	}
}

func TestSubqueryMaxValues(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		maxValues int
		wantErr   bool
	}{
		{name: "When MaxValues is zero it should reject the selector", maxValues: 0, wantErr: true},
		{name: "When MaxValues is negative it should reject the selector", maxValues: -1, wantErr: true},
		{name: "When MaxValues is positive it should accept values within the limit", maxValues: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := subqueryTestResolver{field: &SelectorField{
				Type: String,
				Subquery: &SubquerySelector{
					Template:  "item_id IN (SELECT id FROM other_items WHERE name IN ({values}))",
					MaxValues: testCase.maxValues,
				},
			}}
			fieldSelector, err := NewFieldSelector("other in (one)")
			require.NoError(t, err)
			_, _, err = fieldSelector.Parse(context.Background(), resolver)
			if testCase.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
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
