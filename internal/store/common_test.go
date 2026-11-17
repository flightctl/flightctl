package store

import (
	"errors"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRetryUpdateBudget(testingT *testing.T) {
	sentinel := errors.New("update failed")
	for _, budget := range []int{-2, -1} {
		testingT.Run("When the retry budget is negative it should reject it before updating", func(testingT *testing.T) {
			attempts := 0
			err := RetryUpdate(func() (bool, error) {
				attempts++
				return false, sentinel
			}, budget)
			require.ErrorContains(testingT, err, "maxRetries must not be negative")
			require.Zero(testingT, attempts)
		})
	}

	for _, testCase := range []struct {
		name         string
		budget       []int
		retry        bool
		updateError  error
		wantAttempts int
	}{
		{name: "When zero retries are allowed it should return the first error", budget: []int{0}, retry: true, updateError: sentinel, wantAttempts: 1},
		{name: "When a zero-budget update succeeds it should return success", budget: []int{0}, wantAttempts: 1},
		{name: "When the default budget is exhausted it should return the last error", retry: true, updateError: sentinel, wantAttempts: 10},
		{name: "When retries are not requested it should return the error immediately", updateError: sentinel, wantAttempts: 1},
		{name: "When an explicit budget is exhausted it should return the last error", budget: []int{2}, retry: true, updateError: sentinel, wantAttempts: 3},
	} {
		testingT.Run(testCase.name, func(testingT *testing.T) {
			attempts := 0
			err := RetryUpdate(func() (bool, error) {
				attempts++
				return testCase.retry, testCase.updateError
			}, testCase.budget...)
			if testCase.updateError == nil {
				require.NoError(testingT, err)
			} else {
				require.ErrorIs(testingT, err, sentinel)
			}
			require.Equal(testingT, testCase.wantAttempts, attempts)
		})
	}
}

func TestCreateParamsFromKey(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		want   string
		wantOk bool
	}{
		{
			name:   "When key is a nested status path it should map to its JSONB text accessor",
			key:    "status.summary.status",
			want:   "status -> 'summary' ->> 'status'",
			wantOk: true,
		},
		{
			name:   "When key is the update status path it should map to its JSONB text accessor",
			key:    "status.updated.status",
			want:   "status -> 'updated' ->> 'status'",
			wantOk: true,
		},
		{
			name:   "When key contains a coalesce separator it should map to a COALESCE of NULLIF paths preferring the first",
			key:    OsModeStatusSelector,
			want:   "COALESCE(NULLIF(status -> 'systemInfo' ->> 'osMode', ''), NULLIF(status -> 'capabilities' ->> 'osMode', ''))",
			wantOk: true,
		},
		{
			name:   "When key is not allowlisted it should not resolve to any SQL",
			key:    "status.arbitrary.selector",
			want:   "",
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := createParamsFromKey(tt.key)
			assert.Equal(t, tt.wantOk, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNullableSortValue(t *testing.T) {
	tests := []struct {
		name     string
		value    *string
		expected string
	}{
		{name: "When the value is nil it should return the NULL marker", value: nil, expected: nullSortValue},
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
			op:                ">=",
			expectedOK:        true,
			expectedPredicate: "alias IS NULL AND name >= ?",
			expectedArgs:      []any{"dev-1"},
		},
		{
			name:              "When sorting descending with an alias set it should still include NULL aliases because they sort last",
			columns:           aliasName,
			values:            []string{"alpha", "dev-1"},
			op:                "<=",
			expectedOK:        true,
			expectedPredicate: "((alias, name) <= (?, ?) OR alias IS NULL)",
			expectedArgs:      []any{"alpha", "dev-1"},
		},
		{
			name:              "When sorting descending with a NULL alias it should only page within NULL aliases",
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
			name:       "When alias is the only column it should not apply",
			columns:    []SortColumn{SortByAlias},
			values:     []string{"alpha"},
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

func TestCountRemainingItems(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:count-remaining-items?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// The shared in-memory database lives until its last connection closes, so
	// close it to give each run of this test a fresh table.
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("CREATE TABLE devices (name TEXT NOT NULL, alias TEXT)").Error)
	require.NoError(t, db.Exec(`INSERT INTO devices (name, alias) VALUES
		('dev-1', 'alpha'), ('dev-2', ''), ('dev-3', NULL), ('dev-4', NULL), ('dev-5', 'alpha')`).Error)

	aliasName := ListParams{SortColumns: []SortColumn{SortByAlias, SortByName}}

	tests := []struct {
		name          string
		table         string
		nextValues    []string
		listParams    ListParams
		expectedCount int64
		expectError   bool
	}{
		{
			name:          "When sorting by name it should count the rows from the next value on",
			table:         "devices",
			nextValues:    []string{"dev-3"},
			expectedCount: 3,
		},
		{
			name:          "When sorting by alias from an aliased row it should count later aliases and every NULL alias",
			table:         "devices",
			nextValues:    []string{"alpha", "dev-5"},
			listParams:    aliasName,
			expectedCount: 3,
		},
		{
			name:          "When sorting by alias from a NULL alias it should count only the remaining NULL aliases",
			table:         "devices",
			nextValues:    []string{nullSortValue, "dev-4"},
			listParams:    aliasName,
			expectedCount: 1,
		},
		{
			name:          "When the value count does not match the sort columns it should return zero",
			table:         "devices",
			nextValues:    []string{"dev-3"},
			listParams:    aliasName,
			expectedCount: 0,
		},
		{
			name:        "When the name count query fails it should return the error",
			table:       "missing_table",
			nextValues:  []string{"dev-3"},
			expectError: true,
		},
		{
			name:        "When the alias count query fails it should return the error",
			table:       "missing_table",
			nextValues:  []string{"alpha", "dev-1"},
			listParams:  aliasName,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := CountRemainingItems(db.Table(tt.table), tt.nextValues, tt.listParams)
			if tt.expectError {
				require.Error(t, err)
				require.Zero(t, count)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expectedCount, count)
		})
	}
}
