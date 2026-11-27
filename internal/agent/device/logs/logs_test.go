package logs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitRenderedLines(t *testing.T) {
	testCases := []struct {
		name     string
		out      string
		expected []string
	}{
		{
			name:     "When the output is empty it should yield no lines",
			out:      "",
			expected: nil,
		},
		{
			name:     "When the output is a single terminated line it should yield one line",
			out:      "alpha\n",
			expected: []string{"alpha"},
		},
		{
			name:     "When the final line is unterminated it should still count as one line",
			out:      "alpha\nbeta",
			expected: []string{"alpha", "beta"},
		},
		{
			name:     "When an entry's rendered block contains a blank line it should count that line",
			out:      "alpha\n\nbeta\n",
			expected: []string{"alpha", "", "beta"},
		},
		{
			name:     "When the output is a lone newline it should yield one empty line",
			out:      "\n",
			expected: []string{""},
		},
		{
			name:     "When a line is indented it should not be trimmed",
			out:      "  alpha  \n",
			expected: []string{"  alpha  "},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.New(t).Equal(tc.expected, SplitRenderedLines(tc.out))
		})
	}
}

func TestTailWindow(t *testing.T) {
	lines := []string{"1", "2", "3", "4", "5"}

	testCases := []struct {
		name       string
		tailLines  int64
		lineOffset int64
		expected   []string
	}{
		{
			name:      "When no offset is given it should return the most recent lines",
			tailLines: 2,
			expected:  []string{"4", "5"},
		},
		{
			name:       "When the offset equals the window it should return the page before",
			tailLines:  2,
			lineOffset: 2,
			expected:   []string{"2", "3"},
		},
		{
			name:       "When the window runs past the start it should return what exists rather than pad",
			tailLines:  4,
			lineOffset: 3,
			expected:   []string{"1", "2"},
		},
		{
			name:      "When the window is larger than the result it should return the whole result",
			tailLines: 50,
			expected:  lines,
		},
		{
			name:       "When the offset is at the end of the result it should return nothing",
			tailLines:  2,
			lineOffset: 5,
			expected:   nil,
		},
		{
			name:       "When the offset is beyond the end of the result it should return nothing",
			tailLines:  2,
			lineOffset: 99,
			expected:   nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.New(t).Equal(tc.expected, TailWindow(lines, tc.tailLines, tc.lineOffset))
		})
	}
}

func TestHeadWindow(t *testing.T) {
	lines := []string{"1", "2", "3"}

	testCases := []struct {
		name       string
		lineOffset int64
		expected   []string
	}{
		{
			name:       "When the offset is zero it should return the whole result",
			lineOffset: 0,
			expected:   lines,
		},
		{
			name:       "When an offset is given it should skip that many lines from the head",
			lineOffset: 2,
			expected:   []string{"3"},
		},
		{
			name:       "When the offset is at the end it should return nothing",
			lineOffset: 3,
			expected:   nil,
		},
		{
			name:       "When the offset is beyond the end it should return nothing rather than fail",
			lineOffset: 10,
			expected:   nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.New(t).Equal(tc.expected, HeadWindow(lines, tc.lineOffset))
		})
	}
}

func TestReachedStart(t *testing.T) {
	testCases := []struct {
		name     string
		previous []string
		current  []string
		expected bool
	}{
		{
			name:     "When both rounds are empty it should report the result exhausted",
			previous: nil,
			current:  nil,
			expected: true,
		},
		{
			name:     "When a wider budget reached further back it should not report exhaustion",
			previous: []string{"b", "c"},
			current:  []string{"a", "b", "c"},
			expected: false,
		},
		{
			name:     "When the oldest line stopped moving it should report the result exhausted",
			previous: []string{"a", "b"},
			current:  []string{"a", "b"},
			expected: true,
		},
		{
			name:     "When the oldest line matches but count differs it should not report exhaustion",
			previous: []string{"a", "b"},
			current:  []string{"a", "b", "c"},
			expected: false,
		},
		{
			name:     "When the previous round was empty and this one is not it should not report exhaustion",
			previous: nil,
			current:  []string{"a"},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.New(t).Equal(tc.expected, reachedStart(tc.previous, tc.current))
		})
	}
}

func TestWiden(t *testing.T) {
	require := require.New(t)

	// A budget that produced nothing still has to make progress.
	require.Equal(int64(20), widen(10, 0, 100, 1000))
	// One line per entry: the projection and the doubling floor agree.
	require.Equal(int64(20), widen(10, 10, 20, 1000))
	// Three lines per entry: the projection overtakes the doubling floor only
	// once the shortfall is worse than a factor of two.
	require.Equal(int64(30), widen(10, 10, 30, 1000))
	// The cap is never exceeded.
	require.Equal(int64(15), widen(10, 1, 1000, 15))
	// Every step strictly increases, so the loop cannot spin.
	for _, budget := range []int64{1, 2, 7, 1000} {
		require.Greater(widen(budget, budget, budget+1, 1<<40), budget)
	}
}
