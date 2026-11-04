package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
