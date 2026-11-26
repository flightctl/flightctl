package enrollmentpolicy_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/enrollmentpolicy"
	"github.com/stretchr/testify/require"
)

func TestEvaluateCandidates(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	var spec domain.EnrollmentRequestSpec
	require.NoError(t, json.Unmarshal([]byte(`{
		"csr": "private-csr",
		"labels": {"site": "east"},
		"deviceStatus": {"systemInfo": {
			"productSerial": "SN-001",
			"customInfo": {"site": "west"}
		}}
	}`), &spec))
	serial, err := matcher.Compile(`systemInfo.productSerial == "SN-001"`)
	require.NoError(t, err)
	west, err := matcher.Compile(`systemInfo.customInfo["site"] == "west"`)
	require.NoError(t, err)
	east, err := matcher.Compile(`systemInfo.customInfo["site"] == "east"`)
	require.NoError(t, err)
	missing, err := matcher.Compile(`systemInfo.customInfo["private-key-name"] == "private-value"`)
	require.NoError(t, err)
	failureAt := func(index int) enrollmentpolicy.EvaluationFailure {
		return enrollmentpolicy.EvaluationFailure{Index: index, Err: enrollmentpolicy.ErrEvaluation}
	}

	for _, tc := range []struct {
		name       string
		candidates []*enrollmentpolicy.Policy
		outcome    enrollmentpolicy.Outcome
		indexes    []int
		failures   []enrollmentpolicy.EvaluationFailure
	}{
		{"When the group is empty it should report no match", nil, enrollmentpolicy.NoMatch, nil, nil},
		{"When every expression is false it should report no match",
			[]*enrollmentpolicy.Policy{east}, enrollmentpolicy.NoMatch, nil, nil},
		{"When one expression passes it should return its index",
			[]*enrollmentpolicy.Policy{east, west}, enrollmentpolicy.Matched, []int{1}, nil},
		{"When evaluation fails without a match it should report failure",
			[]*enrollmentpolicy.Policy{east, missing}, enrollmentpolicy.EvaluationFailed, nil, []enrollmentpolicy.EvaluationFailure{failureAt(1)}},
		{"When a candidate errors it should leave another candidate eligible to match",
			[]*enrollmentpolicy.Policy{missing, west, missing}, enrollmentpolicy.Matched, []int{1}, []enrollmentpolicy.EvaluationFailure{failureAt(0), failureAt(2)}},
		{"When the group has multiple matches it should report each matching index",
			[]*enrollmentpolicy.Policy{west, serial, missing}, enrollmentpolicy.MultipleMatches, []int{0, 1}, []enrollmentpolicy.EvaluationFailure{failureAt(2)}},
		{"When the group is reordered it should report the same conflict",
			[]*enrollmentpolicy.Policy{serial, east, west}, enrollmentpolicy.MultipleMatches, []int{0, 2}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := enrollmentpolicy.Evaluate(spec, tc.candidates)
			require.NoError(t, err)
			require.Equal(t, tc.outcome, result.Outcome)
			require.Equal(t, tc.indexes, result.MatchIndexes)
			require.Equal(t, tc.failures, result.Failures)
			for _, failure := range result.Failures {
				require.NotContains(t, failure.Err.Error(), "private-")
			}
		})
	}

	t.Run("When a candidate is uncompiled it should report a failure", func(t *testing.T) {
		result, err := enrollmentpolicy.Evaluate(spec, []*enrollmentpolicy.Policy{nil})
		require.NoError(t, err)
		require.Equal(t, enrollmentpolicy.EvaluationFailed, result.Outcome)
		require.Empty(t, result.MatchIndexes)
		require.Len(t, result.Failures, 1)
		require.ErrorContains(t, result.Failures[0].Err, "has not been compiled")
	})
}

func TestCandidateLimit(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	never, err := matcher.Compile(`false`)
	require.NoError(t, err)
	candidates := make([]*enrollmentpolicy.Policy, enrollmentpolicy.MaxCandidates)
	for i := range candidates {
		candidates[i] = never
	}

	for _, tc := range []struct {
		name       string
		candidates []*enrollmentpolicy.Policy
		outcome    enrollmentpolicy.Outcome
		err        error
	}{
		{"When the group is at the limit it should evaluate", candidates, enrollmentpolicy.NoMatch, nil},
		{"When the group is oversized it should fail before evaluating candidates", append(candidates, nil), "", enrollmentpolicy.ErrTooManyCandidates},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := enrollmentpolicy.Evaluate(domain.EnrollmentRequestSpec{}, tc.candidates)
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, enrollmentpolicy.Evaluation{Outcome: tc.outcome}, result)
		})
	}
}

func TestEvaluateReportedIdentifiersAtScale(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	var candidates []*enrollmentpolicy.Policy
	for _, expression := range []string{
		`systemInfo.customInfo["site"] in ["east", "north", "central"]`,
		`systemInfo.customInfo["site"] in ["west", "south", "central"]`,
	} {
		candidate, err := matcher.Compile(expression)
		require.NoError(t, err)
		candidates = append(candidates, candidate)
	}
	for _, tc := range []struct {
		name    string
		site    *domain.CustomDeviceInfo
		outcome enrollmentpolicy.Outcome
		indexes []int
	}{
		{"When the site is north it should match the first expression", &domain.CustomDeviceInfo{"site": "north"}, enrollmentpolicy.Matched, []int{0}},
		{"When the site is south it should match the second expression", &domain.CustomDeviceInfo{"site": "south"}, enrollmentpolicy.Matched, []int{1}},
		{"When the site is central it should report both matching expressions", &domain.CustomDeviceInfo{"site": "central"}, enrollmentpolicy.MultipleMatches, []int{0, 1}},
		{"When the site is outside both groups it should report no match", &domain.CustomDeviceInfo{"site": "other"}, enrollmentpolicy.NoMatch, nil},
		{"When the site is missing it should report failed evaluation", nil, enrollmentpolicy.EvaluationFailed, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range 2_000 {
				spec := domain.EnrollmentRequestSpec{DeviceStatus: &domain.DeviceStatus{
					SystemInfo: domain.DeviceSystemInfo{CustomInfo: tc.site},
				}}
				result, err := enrollmentpolicy.Evaluate(spec, candidates)
				require.NoError(t, err)
				require.Equal(t, tc.outcome, result.Outcome)
				require.Equal(t, tc.indexes, result.MatchIndexes)
			}
		})
	}
}
