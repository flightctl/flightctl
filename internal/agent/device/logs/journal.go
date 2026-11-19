package logs

import (
	"context"
	stderrors "errors"
	"fmt"
	"math"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
)

// JournalRequest describes one journal snapshot. Zero values mean "filter
// absent": an unset filter widens the result, so every field is explicit.
type JournalRequest struct {
	// Units are unit names or patterns using the '*' and '?' wildcards. They
	// are OR-ed by journalctl.
	Units []string
	// Priority selects that severity and every more severe one.
	Priority client.JournalPriority
	// Boot selects the current or the previous boot.
	Boot client.JournalBoot
	// SinceSeconds bounds the read to the last N seconds. Mutually exclusive
	// with SinceTime.
	SinceSeconds int64
	// SinceTime is an inclusive lower bound.
	SinceTime *time.Time
	// UntilTime is an inclusive upper bound.
	UntilTime *time.Time
	// TailLines is the maximum number of rendered lines to return, counted
	// from the end of the filtered result. Zero means unset.
	TailLines int64
	// LineOffset is an offset in rendered lines: from the end of the result
	// when TailLines is set, from the head when it is not.
	LineOffset int64
}

// Validate reports whether the request is one the agent will act on. Every
// failure wraps errors.ErrLogInvalidParameters, and a failing request starts no
// source work.
func (r *JournalRequest) Validate() error {
	for _, unit := range r.Units {
		if err := client.ValidateJournalUnit(unit); err != nil {
			return err
		}
	}
	if r.Priority != "" {
		if _, ok := r.Priority.Level(); !ok {
			return fmt.Errorf("%w: unknown priority %q", errors.ErrLogInvalidParameters, r.Priority)
		}
	}
	if r.Boot != "" {
		if _, ok := r.Boot.Offset(); !ok {
			return fmt.Errorf("%w: unknown boot selector %q", errors.ErrLogInvalidParameters, r.Boot)
		}
	}
	if r.SinceSeconds < 0 {
		return fmt.Errorf("%w: sinceSeconds must be at least 1, got %d", errors.ErrLogInvalidParameters, r.SinceSeconds)
	}
	// Rejected rather than resolved by precedence: silently preferring one
	// would answer a question the caller did not ask.
	if r.SinceSeconds > 0 && r.SinceTime != nil {
		return fmt.Errorf("%w: sinceSeconds and sinceTime are mutually exclusive", errors.ErrLogInvalidParameters)
	}
	if r.TailLines < 0 {
		return fmt.Errorf("%w: tailLines must be at least 1, got %d", errors.ErrLogInvalidParameters, r.TailLines)
	}
	if r.LineOffset < 0 {
		return fmt.Errorf("%w: lineOffset must not be negative, got %d", errors.ErrLogInvalidParameters, r.LineOffset)
	}
	return nil
}

// emptyInterval reports whether the inclusive time bounds enclose no
// representable journal tick. Rounding the lower bound up and the upper bound
// down can cross them over when both fall inside the same microsecond; that is
// an empty result, not an error and not a reason to widen either bound back out
// to whole seconds.
func (r *JournalRequest) emptyInterval() bool {
	if r.SinceTime == nil || r.UntilTime == nil {
		return false
	}
	return client.JournalTickCeil(*r.SinceTime).After(client.JournalTickFloor(*r.UntilTime))
}

// filterOptions renders the request's filters as journalctl options. The entry
// budget is deliberately absent: it is chosen per query by the scan loop.
func (r *JournalRequest) filterOptions(maxBufferBytes int) []client.LogOptions {
	opts := []client.LogOptions{
		client.WithLogOutputFormat(client.JournalFormatShort),
		client.WithLogMaxOutputBytes(maxBufferBytes),
	}
	for _, unit := range r.Units {
		opts = append(opts, client.WithLogUnit(unit))
	}
	if r.Priority != "" {
		opts = append(opts, client.WithLogPriority(r.Priority))
	}
	if r.Boot != "" {
		opts = append(opts, client.WithLogBoot(r.Boot))
	}
	switch {
	case r.SinceSeconds > 0:
		opts = append(opts, client.WithLogSinceSeconds(r.SinceSeconds))
	case r.SinceTime != nil:
		opts = append(opts, client.WithLogSinceTimestamp(*r.SinceTime))
	}
	if r.UntilTime != nil {
		opts = append(opts, client.WithLogUntilTimestamp(*r.UntilTime))
	}
	return opts
}

// Journal retrieves snapshots of the systemd journal.
type Journal struct {
	log        *log.PrefixLogger
	journalctl *client.Journalctl

	scanBudget     time.Duration
	maxEntryBudget int64
	maxBufferBytes int
}

// JournalOption customises a Journal. The defaults are the design's budgets.
type JournalOption func(*Journal)

// WithScanBudget shortens the wall-clock budget for assembling one snapshot.
// A budget longer than DefaultScanBudget is ignored: the ceiling is fixed.
func WithScanBudget(budget time.Duration) JournalOption {
	return func(j *Journal) {
		if budget > 0 && budget < DefaultScanBudget {
			j.scanBudget = budget
		}
	}
}

// WithMaxEntryBudget caps the candidate entry budget across widening rounds.
func WithMaxEntryBudget(entries int64) JournalOption {
	return func(j *Journal) {
		if entries > 0 {
			j.maxEntryBudget = entries
		}
	}
}

// WithMaxBufferBytes caps the rendered candidate set buffered for one query.
func WithMaxBufferBytes(maxBytes int) JournalOption {
	return func(j *Journal) {
		if maxBytes > 0 {
			j.maxBufferBytes = maxBytes
		}
	}
}

func NewJournal(log *log.PrefixLogger, journalctl *client.Journalctl, opts ...JournalOption) *Journal {
	j := &Journal{
		log:            log,
		journalctl:     journalctl,
		scanBudget:     DefaultScanBudget,
		maxEntryBudget: DefaultMaxEntryBudget,
		maxBufferBytes: DefaultMaxBufferBytes,
	}
	for _, opt := range opts {
		opt(j)
	}
	return j
}

// Snapshot assembles the requested window of rendered journal lines.
//
// It either returns exactly the window asked for, a shorter window because the
// filtered result is exhausted, or an error. It never returns a short or
// shifted page as a success: a budget reached before the window is filled is
// errors.ErrLogScanIncomplete, and the caller reports that rather than the
// lines it happened to collect.
func (j *Journal) Snapshot(ctx context.Context, req JournalRequest) (*Snapshot, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.emptyInterval() {
		return &Snapshot{Exhausted: true}, nil
	}

	filters := req.filterOptions(j.maxBufferBytes)

	// Pin the query at the current time when no explicit upper bound is set.
	// Without this, entries appended between widening rounds can make the
	// oldest rendered line appear unchanged even though the budget has not
	// reached the actual start of the journal, causing reachedStart to
	// falsely report exhaustion and silently omit older entries.
	if req.UntilTime == nil {
		scanTime := time.Now()
		filters = append(filters, client.WithLogUntilTimestamp(scanTime))
	}

	// The scan budget covers the whole assembly -- every widening round, not
	// one invocation -- and ends at completion rather than at first output.
	scanCtx, cancel := context.WithTimeout(ctx, j.scanBudget)
	defer cancel()

	// Without tailLines the window is head-relative, so there is no suffix to
	// aim at: the whole filtered result is needed and no entry budget applies.
	if req.TailLines == 0 {
		lines, err := j.query(ctx, scanCtx, filters, 0)
		if err != nil {
			return nil, err
		}
		return &Snapshot{Lines: HeadWindow(lines, req.LineOffset), Exhausted: true}, nil
	}

	need, ok := addInt64(req.TailLines, req.LineOffset)
	if !ok {
		return nil, fmt.Errorf("%w: tailLines + lineOffset overflows int64", errors.ErrLogInvalidParameters)
	}

	// Collect conservatively: an entry renders to at least one line, so an
	// entry budget of need can never overshoot the window, and it is exactly
	// right whenever no entry renders to more than one line.
	budget := need
	if budget >= j.maxEntryBudget {
		// Start below the cap so that a later round can reach it and be
		// compared against this one. Opening at the cap leaves a short result
		// undecidable: with nothing to compare against there is no telling an
		// exhausted journal from one the cap cut off, and the request would
		// fail where an empty success was the right answer.
		budget = max(1, j.maxEntryBudget/2)
	}
	var previous []string
	havePrevious := false

	for round := 1; ; round++ {
		lines, err := j.query(ctx, scanCtx, filters, budget)
		if err != nil {
			return nil, err
		}

		if int64(len(lines)) >= need {
			return &Snapshot{Lines: TailWindow(lines, req.TailLines, req.LineOffset)}, nil
		}
		if havePrevious && reachedStart(previous, lines) {
			// End of data, not truncation: the page is simply shorter.
			return &Snapshot{Lines: TailWindow(lines, req.TailLines, req.LineOffset), Exhausted: true}, nil
		}
		if budget >= j.maxEntryBudget {
			return nil, fmt.Errorf("%w: entry budget %d reached after %d rounds with %d of %d rendered lines",
				errors.ErrLogScanIncomplete, budget, round, len(lines), need)
		}

		previous, havePrevious = lines, true
		budget = widen(budget, int64(len(lines)), need, j.maxEntryBudget)
		if j.log != nil {
			j.log.Debugf("Journal scan widening to %d entries after round %d yielded %d of %d rendered lines", budget, round, len(lines), need)
		}
	}
}

// query runs one filtered journalctl invocation and splits its output into
// rendered lines. entryBudget of 0 passes no -n at all.
func (j *Journal) query(ctx, scanCtx context.Context, filters []client.LogOptions, entryBudget int64) ([]string, error) {
	opts := filters
	if entryBudget > 0 {
		opts = append(append(make([]client.LogOptions, 0, len(filters)+1), filters...), client.WithLogMaxEntries(entryBudget))
	}

	out, err := j.journalctl.Output(scanCtx, opts...)
	if err != nil {
		// Distinguish the agent's own budget from the caller giving up: only
		// the former is a device-side timeout the caller reports as such.
		if ctx.Err() == nil && stderrors.Is(scanCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: scan budget of %s expired with an entry budget of %d: %w",
				errors.ErrLogScanIncomplete, j.scanBudget, entryBudget, err)
		}
		return nil, err
	}
	return SplitRenderedLines(out), nil
}

// reachedStart reports whether widening the entry budget stopped reaching
// further back.
//
// Each candidate set is a suffix of the filtered result, so a larger budget can
// only extend it at the old end. When the oldest rendered line stops moving the
// budget is no longer what is limiting the result, and the filtered result is
// exhausted. Comparing the oldest line rather than the line count is what keeps
// a journal that is still being appended to from looking like progress forever.
func reachedStart(previous, current []string) bool {
	if len(current) == 0 {
		return len(previous) == 0
	}
	if len(previous) == 0 {
		return false
	}
	return current[0] == previous[0] && len(current) >= len(previous)
}

// widen chooses the next entry budget. The shortfall ratio projects the
// observed lines-per-entry forward, which converges in one further round on a
// result of uniformly multi-line entries; doubling is the floor, so a round
// that learned nothing still makes progress.
func widen(budget, got, need, maxBudget int64) int64 {
	next := mulInt64(budget, 2)
	if got > 0 {
		if scaled := ceilDivInt64(mulInt64(budget, need), got); scaled > next {
			next = scaled
		}
	}
	if next > maxBudget {
		next = maxBudget
	}
	if next <= budget {
		next = budget + 1
	}
	return next
}

// addInt64 adds two non-negative values, reporting whether the sum overflows.
func addInt64(a, b int64) (int64, bool) {
	if a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

// mulInt64 multiplies two non-negative values, saturating at math.MaxInt64.
func mulInt64(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

func ceilDivInt64(a, b int64) int64 {
	if b <= 0 {
		return a
	}
	if a > math.MaxInt64-(b-1) {
		return math.MaxInt64
	}
	return (a + b - 1) / b
}
