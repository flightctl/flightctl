package logs

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// fakeEntry is one journal entry. text is its rendered block, lines joined by
// "\n" without a trailing terminator; the fake adds the terminator, as
// journalctl does.
type fakeEntry struct {
	text  string
	match bool
}

// fakeJournal stands in for journalctl.
//
// budgetBeforeFilter selects which way round -n and the filters compose. The
// design deliberately does not depend on that ordering, so both are modelled:
// the default is journalctl's own filter-first behaviour, and the widening
// tests use the other ordering, which is the one that actually makes a round
// come back short.
type fakeJournal struct {
	entries            []fakeEntry
	budgetBeforeFilter bool
	// onCall, when set, runs before each render and may mutate entries.
	onCall func(f *fakeJournal, call int)

	budgets []int64
	args    [][]string
}

func (f *fakeJournal) render(args []string) string {
	f.args = append(f.args, args)
	budget := entryBudgetArg(args)
	f.budgets = append(f.budgets, budget)
	if f.onCall != nil {
		f.onCall(f, len(f.budgets))
	}

	entries := f.entries
	if f.budgetBeforeFilter {
		entries = matching(lastN(entries, budget))
	} else {
		entries = lastN(matching(entries), budget)
	}

	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(entry.text)
		b.WriteString("\n")
	}
	return b.String()
}

func lastN(entries []fakeEntry, budget int64) []fakeEntry {
	if budget <= 0 || int64(len(entries)) <= budget {
		return entries
	}
	return entries[int64(len(entries))-budget:]
}

func matching(entries []fakeEntry) []fakeEntry {
	kept := make([]fakeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.match {
			kept = append(kept, entry)
		}
	}
	return kept
}

func entryBudgetArg(args []string) int64 {
	for i, arg := range args {
		if arg == "-n" && i+1 < len(args) {
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

func newJournalUnderTest(t *testing.T, fake *fakeJournal, opts ...JournalOption) *Journal {
	t.Helper()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ context.Context, _, _ string, args []string, _ int, _ ...string) (string, string, int) {
			return fake.render(args), "", 0
		})

	return NewJournal(log.NewPrefixLogger("test"), client.NewJournalctl(mockExec, v1beta1.Username("")), opts...)
}

// singleLineEntries builds count matching entries that each render to exactly
// one line.
func singleLineEntries(count int) []fakeEntry {
	entries := make([]fakeEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, fakeEntry{text: fmt.Sprintf("line-%02d", i), match: true})
	}
	return entries
}

// multiLineEntries builds count matching entries that each render to linesPer
// lines, so the entry count and the rendered line count deliberately disagree.
func multiLineEntries(count, linesPer int) []fakeEntry {
	entries := make([]fakeEntry, 0, count)
	for i := 1; i <= count; i++ {
		block := make([]string, 0, linesPer)
		for l := 1; l <= linesPer; l++ {
			block = append(block, fmt.Sprintf("entry-%02d/line-%d", i, l))
		}
		entries = append(entries, fakeEntry{text: strings.Join(block, "\n"), match: true})
	}
	return entries
}

// sparseEntries builds total entries of which every nth matches, so an entry
// budget applied before the filters comes back far short of the window.
func sparseEntries(total, nth, linesPer int) []fakeEntry {
	entries := make([]fakeEntry, 0, total)
	for i := 1; i <= total; i++ {
		if i%nth != 0 {
			entries = append(entries, fakeEntry{text: fmt.Sprintf("noise-%03d", i)})
			continue
		}
		block := make([]string, 0, linesPer)
		for l := 1; l <= linesPer; l++ {
			block = append(block, fmt.Sprintf("match-%03d/line-%d", i, l))
		}
		entries = append(entries, fakeEntry{text: strings.Join(block, "\n"), match: true})
	}
	return entries
}

func TestJournalSnapshotWindow(t *testing.T) {
	testCases := []struct {
		name              string
		entries           []fakeEntry
		request           JournalRequest
		expectedLines     []string
		expectedExhausted bool
	}{
		{
			name:          "When tailLines is given it should return the most recent rendered lines",
			entries:       singleLineEntries(5),
			request:       JournalRequest{TailLines: 2},
			expectedLines: []string{"line-04", "line-05"},
		},
		{
			name:          "When lineOffset accompanies tailLines it should return the page before",
			entries:       singleLineEntries(5),
			request:       JournalRequest{TailLines: 2, LineOffset: 2},
			expectedLines: []string{"line-02", "line-03"},
		},
		{
			name:              "When the filtered result is shorter than the window it should return a short page marked exhausted",
			entries:           singleLineEntries(3),
			request:           JournalRequest{TailLines: 10},
			expectedLines:     []string{"line-01", "line-02", "line-03"},
			expectedExhausted: true,
		},
		{
			name:              "When the journal is empty it should return no lines rather than fail",
			entries:           nil,
			request:           JournalRequest{TailLines: 5},
			expectedLines:     nil,
			expectedExhausted: true,
		},
		{
			name:              "When the offset is past the whole result it should return an empty page",
			entries:           singleLineEntries(3),
			request:           JournalRequest{TailLines: 2, LineOffset: 10},
			expectedLines:     nil,
			expectedExhausted: true,
		},
		{
			name:    "When entries render to several lines each it should count rendered lines and not entries",
			entries: multiLineEntries(4, 3),
			request: JournalRequest{TailLines: 4},
			expectedLines: []string{
				"entry-03/line-3",
				"entry-04/line-1", "entry-04/line-2", "entry-04/line-3",
			},
		},
		{
			name:    "When the window boundary falls inside an entry's rendered block it should cut at the line boundary",
			entries: multiLineEntries(4, 3),
			request: JournalRequest{TailLines: 2, LineOffset: 3},
			expectedLines: []string{
				"entry-03/line-2", "entry-03/line-3",
			},
		},
		{
			name:    "When a blank line sits inside an entry's block it should be counted as a line",
			entries: []fakeEntry{{text: "head\n\ntail", match: true}},
			request: JournalRequest{TailLines: 2},
			expectedLines: []string{
				"", "tail",
			},
		},
		{
			name:          "When tailLines is absent it should return the whole filtered result from the head offset",
			entries:       singleLineEntries(4),
			request:       JournalRequest{LineOffset: 2},
			expectedLines: []string{"line-03", "line-04"},
			// No entry budget is involved, so the scan is complete by
			// construction.
			expectedExhausted: true,
		},
		{
			name:              "When neither tailLines nor lineOffset is given it should return the whole filtered result",
			entries:           singleLineEntries(3),
			request:           JournalRequest{},
			expectedLines:     []string{"line-01", "line-02", "line-03"},
			expectedExhausted: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			fake := &fakeJournal{entries: tc.entries}
			j := newJournalUnderTest(t, fake)

			snapshot, err := j.Snapshot(context.Background(), tc.request)
			require.NoError(err)
			require.Equal(tc.expectedLines, snapshot.Lines)
			require.Equal(tc.expectedExhausted, snapshot.Exhausted)
			if tc.request.TailLines > 0 {
				require.LessOrEqual(int64(len(snapshot.Lines)), tc.request.TailLines,
					"a successful page must never exceed tailLines")
			}
		})
	}
}

func TestJournalSnapshotCollectsConservatively(t *testing.T) {
	require := require.New(t)

	// 40 entries of 4 rendered lines each, with the entry budget applied after
	// the filters. An entry renders to at least one line, so a budget of
	// tailLines already covers the window and one round suffices -- no round
	// trip is spent on a widening step that cannot be needed.
	fake := &fakeJournal{entries: multiLineEntries(40, 4)}
	j := newJournalUnderTest(t, fake)

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 20})
	require.NoError(err)
	require.Len(snapshot.Lines, 20)
	require.Equal([]int64{20}, fake.budgets)
	require.Equal("entry-36/line-1", snapshot.Lines[0])
	require.Equal("entry-40/line-4", snapshot.Lines[19])
}

func TestJournalSnapshotWideningConverges(t *testing.T) {
	require := require.New(t)

	// 100 entries of which every fifth matches, with the entry budget applied
	// before the filters: a budget of tailLines comes back five times short,
	// so the scan has to widen. The shortfall projection must converge in a
	// few rounds and every round must strictly increase the budget.
	fake := &fakeJournal{entries: sparseEntries(100, 5, 1), budgetBeforeFilter: true}
	j := newJournalUnderTest(t, fake, WithMaxEntryBudget(1024))

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 12})
	require.NoError(err)
	require.Len(snapshot.Lines, 12)
	require.Equal("match-045/line-1", snapshot.Lines[0])
	require.Equal("match-100/line-1", snapshot.Lines[11])

	require.Greater(len(fake.budgets), 1, "a short first round should widen")
	require.LessOrEqual(len(fake.budgets), 4, "the shortfall projection should converge quickly")
	for i := 1; i < len(fake.budgets); i++ {
		require.Greater(fake.budgets[i], fake.budgets[i-1], "each round must widen")
	}
}

func TestJournalSnapshotWideningWithMultiLineEntries(t *testing.T) {
	require := require.New(t)

	// The same shape, but each matching entry renders to three lines, so the
	// entry budget and the rendered line count disagree in both directions at
	// once. The window still has to come out line-exact.
	fake := &fakeJournal{entries: sparseEntries(100, 10, 3), budgetBeforeFilter: true}
	j := newJournalUnderTest(t, fake, WithMaxEntryBudget(1024))

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 7, LineOffset: 2})
	require.NoError(err)
	require.Len(snapshot.Lines, 7)

	// Ten matching entries render to 30 lines. Dropping the trailing 2 and
	// taking the last 7 ends part-way through entry 100's block, and that
	// block is neither re-rendered nor padded out.
	require.Equal([]string{
		"match-080/line-1", "match-080/line-2", "match-080/line-3",
		"match-090/line-1", "match-090/line-2", "match-090/line-3",
		"match-100/line-1",
	}, snapshot.Lines)
}

func TestJournalSnapshotFailsRatherThanReturningAShortPage(t *testing.T) {
	require := require.New(t)

	// The journal holds far more than the window needs, but the entry budget
	// is capped below what would fill it. Returning the lines collected so far
	// would be a short page presented as success, so this must fail.
	fake := &fakeJournal{entries: multiLineEntries(500, 1)}
	j := newJournalUnderTest(t, fake, WithMaxEntryBudget(8))

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 100})
	require.ErrorIs(err, errors.ErrLogScanIncomplete)
	require.Nil(snapshot)
}

func TestJournalSnapshotExhaustedResultLargerThanTheEntryCap(t *testing.T) {
	require := require.New(t)

	// The window asked for is larger than the entry cap, and the journal holds
	// far less than either. The scan must still recognise an exhausted result
	// rather than fail: opening at the cap would leave the short round
	// undecidable, so the first round deliberately stays below it.
	fake := &fakeJournal{entries: singleLineEntries(3)}
	j := newJournalUnderTest(t, fake, WithMaxEntryBudget(8))

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 100})
	require.NoError(err)
	require.True(snapshot.Exhausted)
	require.Equal([]string{"line-01", "line-02", "line-03"}, snapshot.Lines)
	require.Equal([]int64{4, 8}, fake.budgets)
}

func TestJournalSnapshotExhaustionWhileTheJournalGrows(t *testing.T) {
	require := require.New(t)

	// The filtered result is exhausted at 3 entries, but a new entry lands
	// between rounds. Judging exhaustion by the line count alone would see
	// progress forever and eventually fail; judging it by the oldest line
	// settles it on the second round.
	fake := &fakeJournal{
		entries: singleLineEntries(3),
		onCall: func(f *fakeJournal, call int) {
			f.entries = append(f.entries, fakeEntry{text: fmt.Sprintf("appended-%d", call), match: true})
		},
	}
	j := newJournalUnderTest(t, fake, WithMaxEntryBudget(64))

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 50})
	require.NoError(err)
	require.True(snapshot.Exhausted)
	require.Equal("line-01", snapshot.Lines[0])
	require.Len(fake.budgets, 2)
}

func TestJournalSnapshotScanBudget(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(ctx context.Context, _, _ string, _ []string, _ int, _ ...string) (string, string, int) {
			<-ctx.Done()
			return "", ctx.Err().Error(), 124
		})

	j := NewJournal(log.NewPrefixLogger("test"), client.NewJournalctl(mockExec, v1beta1.Username("")),
		WithScanBudget(50*time.Millisecond))

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 10})
	require.ErrorIs(err, errors.ErrLogScanIncomplete)
	require.Nil(snapshot)
}

func TestJournalSnapshotCallerCancellationIsNotAScanTimeout(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(execCtx context.Context, _, _ string, _ []string, _ int, _ ...string) (string, string, int) {
			cancel()
			<-execCtx.Done()
			return "", "context canceled", 124
		})

	j := NewJournal(log.NewPrefixLogger("test"), client.NewJournalctl(mockExec, v1beta1.Username("")))

	_, err := j.Snapshot(ctx, JournalRequest{TailLines: 10})
	require.Error(err)
	require.NotErrorIs(err, errors.ErrLogScanIncomplete)
}

func TestJournalSnapshotFilterArguments(t *testing.T) {
	require := require.New(t)

	since := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	fake := &fakeJournal{entries: singleLineEntries(2)}
	j := newJournalUnderTest(t, fake)

	_, err := j.Snapshot(context.Background(), JournalRequest{
		Units:     []string{"flightctl-agent.service", "fctl-*.service"},
		Priority:  client.JournalPriorityWarning,
		Boot:      client.JournalBootCurrent,
		SinceTime: &since,
		UntilTime: &until,
		TailLines: 1,
	})
	require.NoError(err)
	require.Len(fake.args, 1)
	require.Equal([]string{
		"--no-pager", "--quiet", "-o", "short",
		"-u", "flightctl-agent.service",
		"-u", "fctl-*.service",
		"-p", "4",
		"-b", "0",
		"--since", "2026-11-17 09:00:00.000000 UTC",
		"--until", "2026-11-17 10:00:00.000000 UTC",
		"-n", "1",
	}, fake.args[0])
}

func TestJournalSnapshotHeadRelativeFormPassesNoEntryBudget(t *testing.T) {
	require := require.New(t)

	fake := &fakeJournal{entries: singleLineEntries(5)}
	j := newJournalUnderTest(t, fake)

	_, err := j.Snapshot(context.Background(), JournalRequest{LineOffset: 1})
	require.NoError(err)
	require.Equal([]int64{0}, fake.budgets)
	require.NotContains(fake.args[0], "-n")
}

func TestJournalSnapshotEmptyTimeInterval(t *testing.T) {
	require := require.New(t)

	// Both bounds fall inside one microsecond: rounding the lower bound up and
	// the upper bound down crosses them over, so no representable tick is in
	// range. That is an empty success, and the device is never asked.
	since := time.Date(2026, 11, 17, 9, 0, 0, 1_500, time.UTC)
	until := time.Date(2026, 11, 17, 9, 0, 0, 1_900, time.UTC)

	fake := &fakeJournal{entries: singleLineEntries(5)}
	j := newJournalUnderTest(t, fake)

	snapshot, err := j.Snapshot(context.Background(), JournalRequest{
		SinceTime: &since,
		UntilTime: &until,
		TailLines: 10,
	})
	require.NoError(err)
	require.Empty(snapshot.Lines)
	require.True(snapshot.Exhausted)
	require.Empty(fake.budgets, "no journalctl invocation should have been made")
}

func TestJournalSnapshotBootUnavailable(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		Return("", "Data from the specified boot (-1) is not available: No such boot ID in journal", 1)

	j := NewJournal(log.NewPrefixLogger("test"), client.NewJournalctl(mockExec, v1beta1.Username("")))

	_, err := j.Snapshot(context.Background(), JournalRequest{Boot: client.JournalBootPrevious, TailLines: 10})
	require.ErrorIs(err, errors.ErrLogBootUnavailable)
}

func TestJournalRequestValidation(t *testing.T) {
	now := time.Now()

	testCases := []struct {
		name          string
		request       JournalRequest
		expectedError bool
	}{
		{
			name:    "When no filter is set it should be accepted",
			request: JournalRequest{},
		},
		{
			name:    "When a wildcard unit pattern is used it should be accepted",
			request: JournalRequest{Units: []string{"fctl-alpha.service", "fctl-?eta.service", "fctl-*.service"}},
		},
		{
			name:          "When a unit pattern uses a character class it should be rejected",
			request:       JournalRequest{Units: []string{"fctl-[ab]*.service"}},
			expectedError: true,
		},
		{
			name:          "When a unit pattern carries a shell command it should be rejected",
			request:       JournalRequest{Units: []string{"x; touch /tmp/x"}},
			expectedError: true,
		},
		{
			name:          "When the priority is not one this API defines it should be rejected",
			request:       JournalRequest{Priority: "err"},
			expectedError: true,
		},
		{
			name:          "When the boot selector is not one this API defines it should be rejected",
			request:       JournalRequest{Boot: "all"},
			expectedError: true,
		},
		{
			name:          "When sinceSeconds and sinceTime are both set it should be rejected rather than resolved by precedence",
			request:       JournalRequest{SinceSeconds: 60, SinceTime: &now},
			expectedError: true,
		},
		{
			name:    "When only sinceSeconds is set it should be accepted",
			request: JournalRequest{SinceSeconds: 60},
		},
		{
			name:    "When only sinceTime is set it should be accepted",
			request: JournalRequest{SinceTime: &now},
		},
		{
			name:          "When tailLines is negative it should be rejected",
			request:       JournalRequest{TailLines: -1},
			expectedError: true,
		},
		{
			name:          "When lineOffset is negative it should be rejected",
			request:       JournalRequest{LineOffset: -1},
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			err := tc.request.Validate()
			if tc.expectedError {
				require.ErrorIs(err, errors.ErrLogInvalidParameters)
				return
			}
			require.NoError(err)
		})
	}
}

func TestJournalSnapshotRejectsInvalidRequestWithoutTouchingTheDevice(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// No call is expected: a rejected request starts no source work.
	mockExec := executer.NewMockExecuter(ctrl)
	j := NewJournal(log.NewPrefixLogger("test"), client.NewJournalctl(mockExec, v1beta1.Username("")))

	_, err := j.Snapshot(context.Background(), JournalRequest{Units: []string{"fctl-[ab]*.service"}})
	require.ErrorIs(err, errors.ErrLogInvalidParameters)
}

func TestJournalSnapshotPinsQueryBoundaryForTailRelative(t *testing.T) {
	// Regression: without an upper time bound, entries appended between
	// widening rounds can make the oldest rendered line appear unchanged,
	// causing reachedStart to report false exhaustion. The fix pins the
	// query at scan start when no explicit UntilTime is provided.
	require := require.New(t)

	fake := &fakeJournal{entries: singleLineEntries(5)}
	j := newJournalUnderTest(t, fake)

	_, err := j.Snapshot(context.Background(), JournalRequest{TailLines: 3})
	require.NoError(err)
	require.Len(fake.args, 1)

	// An --until argument must be present even though the request did not
	// specify UntilTime.
	hasUntil := false
	for _, arg := range fake.args[0] {
		if arg == "--until" {
			hasUntil = true
			break
		}
	}
	require.True(hasUntil, "--until must be added when no explicit UntilTime is set")
}

func TestReachedStartStableInputReportsExhaustion(t *testing.T) {
	// With the until-time fix that pins the query boundary, the input to
	// reachedStart is stable across widening rounds. This test verifies
	// that reachedStart correctly detects genuine exhaustion on stable
	// input, and correctly detects NON-exhaustion when the budget reaches
	// further back.

	testCases := []struct {
		name     string
		previous []string
		current  []string
		want     bool
	}{
		{
			name:     "When the budget finds the same entries it should report exhausted",
			previous: []string{"line-01", "line-02", "line-03"},
			current:  []string{"line-01", "line-02", "line-03"},
			want:     true,
		},
		{
			name:     "When the budget reaches further back it should report not exhausted",
			previous: []string{"line-05", "line-06"},
			current:  []string{"line-03", "line-04", "line-05", "line-06"},
			want:     false,
		},
		{
			name:     "When both are empty it should report exhausted",
			previous: nil,
			current:  nil,
			want:     true,
		},
		{
			name:     "When the previous is empty but current has entries it should report not exhausted",
			previous: nil,
			current:  []string{"line-01"},
			want:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.New(t).Equal(tc.want, reachedStart(tc.previous, tc.current))
		})
	}
}

func TestJournalSnapshotRejectsWindowOverflow(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	j := NewJournal(log.NewPrefixLogger("test"), client.NewJournalctl(mockExec, v1beta1.Username("")))

	// No journalctl invocation is expected: the window cannot be expressed, so
	// no entry budget can be chosen for it.
	_, err := j.Snapshot(context.Background(), JournalRequest{
		TailLines:  math.MaxInt64,
		LineOffset: 1,
	})
	require.ErrorIs(err, errors.ErrLogInvalidParameters)
}
