package store

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

func TestNullableSortValue(t *testing.T) {
	tests := []struct {
		name     string
		value    *string
		expected string
	}{
		{name: "When the value is nil it should return the NULL sentinel", value: nil, expected: nullSortValue},
		{name: "When the value is empty it should return an empty string", value: lo.ToPtr(""), expected: ""},
		{name: "When the value is set it should return the value", value: lo.ToPtr("alpha"), expected: "alpha"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NullableSortValue(tt.value))
		})
	}
}

func TestNullableAliasPredicate(t *testing.T) {
	aliasName := []SortColumn{SortByAlias, SortByName}

	tests := []struct {
		name              string
		columns           []SortColumn
		values            []string
		op                string
		expectedOK        bool
		expectedPredicate string
		expectedArgs      []any
	}{
		{
			name:              "When the alias is set it should include NULL aliases after the tuple",
			columns:           aliasName,
			values:            []string{"alpha", "dev-1"},
			op:                ">=",
			expectedOK:        true,
			expectedPredicate: "((alias, name) >= (?, ?) OR alias IS NULL)",
			expectedArgs:      []any{"alpha", "dev-1"},
		},
		{
			name:              "When the alias is empty it should compare it as a value",
			columns:           aliasName,
			values:            []string{"", "dev-1"},
			op:                ">=",
			expectedOK:        true,
			expectedPredicate: "((alias, name) >= (?, ?) OR alias IS NULL)",
			expectedArgs:      []any{"", "dev-1"},
		},
		{
			name:              "When the alias is NULL it should only page within NULL aliases",
			columns:           aliasName,
			values:            []string{nullSortValue, "dev-1"},
			op:                "<=",
			expectedOK:        true,
			expectedPredicate: "alias IS NULL AND name <= ?",
			expectedArgs:      []any{"dev-1"},
		},
		{
			name:       "When the columns are not alias and name it should not apply",
			columns:    []SortColumn{SortByCreatedAt, SortByName},
			values:     []string{"2024-01-01T00:00:00Z", "dev-1"},
			op:         ">=",
			expectedOK: false,
		},
		{
			name:       "When the value count does not match it should not apply",
			columns:    aliasName,
			values:     []string{"dev-1"},
			op:         ">=",
			expectedOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predicate, args, ok := nullableAliasPredicate(tt.columns, tt.values, tt.op)
			assert.Equal(t, tt.expectedOK, ok)
			if tt.expectedOK {
				assert.Equal(t, tt.expectedPredicate, predicate)
				assert.Equal(t, tt.expectedArgs, args)
			}
		})
	}
}
