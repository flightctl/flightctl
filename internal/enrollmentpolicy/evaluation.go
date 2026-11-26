package enrollmentpolicy

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
)

// MaxCandidates bounds the number of expressions evaluated against one request.
const MaxCandidates = 32

// ErrTooManyCandidates indicates that a group exceeds MaxCandidates.
var ErrTooManyCandidates = fmt.Errorf("an expression group can contain at most %d candidates", MaxCandidates)

// Outcome summarizes the evaluation of a set of enrollment expressions.
type Outcome string

const (
	NoMatch          Outcome = "no_match"
	Matched          Outcome = "matched"
	MultipleMatches  Outcome = "multiple_matches"
	EvaluationFailed Outcome = "evaluation_failed"
)

// EvaluationFailure identifies a candidate that returned a sanitized error.
type EvaluationFailure struct {
	Index int
	Err   error
}

// Evaluation is the result of evaluating all candidates. Indexes refer to
// positions in the supplied group, including when multiple expressions match.
type Evaluation struct {
	Outcome      Outcome
	MatchIndexes []int
	Failures     []EvaluationFailure
}

// Evaluate evaluates every candidate against the same request. Candidate errors
// are recorded independently. An oversized group returns an error before evaluation.
func Evaluate(spec domain.EnrollmentRequestSpec, candidates []*Policy) (Evaluation, error) {
	if len(candidates) > MaxCandidates {
		return Evaluation{}, ErrTooManyCandidates
	}
	result := Evaluation{Outcome: NoMatch}
	for index, candidate := range candidates {
		matched, err := candidate.Match(spec)
		switch {
		case err != nil:
			result.Failures = append(result.Failures, EvaluationFailure{Index: index, Err: err})
		case matched:
			result.MatchIndexes = append(result.MatchIndexes, index)
		}
	}
	switch len(result.MatchIndexes) {
	case 0:
		if len(result.Failures) > 0 {
			result.Outcome = EvaluationFailed
		}
	case 1:
		result.Outcome = Matched
	default:
		result.Outcome = MultipleMatches
	}
	return result, nil
}
