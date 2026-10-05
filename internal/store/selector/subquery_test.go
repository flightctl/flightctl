package selector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	gormschema "gorm.io/gorm/schema"
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
					Template:       testCase.template,
					Args:           map[string]any{"owner_id": 7, "tenant_id": 42},
					MaxValues:      2,
					MaxValueLength: 10,
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
					Template:       "item_id IN (SELECT id FROM other_items WHERE name IN ({values}))",
					MaxValues:      testCase.maxValues,
					MaxValueLength: 10,
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

func TestSubqueryMaxValueLength(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		maxValueLength int
		wantErr        bool
	}{
		{name: "When MaxValueLength is zero it should reject the selector", maxValueLength: 0, wantErr: true},
		{name: "When MaxValueLength is negative it should reject the selector", maxValueLength: -1, wantErr: true},
		{name: "When MaxValueLength is shorter than the value it should reject the selector", maxValueLength: 2, wantErr: true},
		{name: "When MaxValueLength fits the value it should accept the selector", maxValueLength: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := subqueryTestResolver{field: &SelectorField{
				Type: String,
				Subquery: &SubquerySelector{
					Template:       "item_id IN (SELECT id FROM other_items WHERE name IN ({values}))",
					MaxValues:      2,
					MaxValueLength: testCase.maxValueLength,
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

func TestSubqueryRejectsJSONBCast(t *testing.T) {
	// A subquery template is a complete boolean predicate; a JSONB cast would wrap
	// it in CAST(... AS <type>) and emit malformed SQL. The combination must be
	// rejected up front rather than silently producing a broken query.
	for _, testCase := range []struct {
		name         string
		fieldType    gormschema.DataType
		selectorType SelectorType
		selector     string
		wantErr      bool
	}{
		{
			// Type != String is the case that would otherwise reach CAST(... AS <type>).
			name:         "When a JSONB cast would wrap the subquery it should reject the selector",
			fieldType:    "jsonb",
			selectorType: Int,
			selector:     "other in (1)",
			wantErr:      true,
		},
		{
			name:         "When the field is a JSONB cast to string it should reject the selector",
			fieldType:    "jsonb",
			selectorType: String,
			selector:     "other in (one)",
			wantErr:      true,
		},
		{
			name:         "When the field is JSONB without a cast it should accept the selector",
			fieldType:    "jsonb",
			selectorType: Jsonb,
			selector:     `other in ("one")`,
		},
		{
			name:         "When the field is not JSONB it should accept the selector",
			fieldType:    "text",
			selectorType: String,
			selector:     "other in (one)",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := subqueryTestResolver{field: &SelectorField{
				Type:      testCase.selectorType,
				FieldType: testCase.fieldType,
				Subquery: &SubquerySelector{
					Template:       "item_id IN (SELECT id FROM other_items WHERE name IN ({values}))",
					MaxValues:      2,
					MaxValueLength: 10,
				},
			}}
			fieldSelector, err := NewFieldSelector(testCase.selector)
			require.NoError(t, err)
			_, _, err = fieldSelector.Parse(context.Background(), resolver)
			if testCase.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "subquery selectors cannot be combined with JSONB cast")
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
