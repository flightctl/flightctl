// Package logs retrieves device log snapshots on behalf of the device log API.
//
// Every count the API quotes -- tailLines, lineOffset, the size of a returned
// page -- is denominated in **rendered logical content lines**: the output as
// the source renders it, after all filters, in journalctl's short format for
// the journal and as stored for a file. One journal entry with embedded
// newlines is several lines, so no journalctl flag implements the window
// directly; the window is computed here, over the rendered output.
//
// The package owns no transport. It returns a snapshot and a typed error, and
// the caller decides how to frame them.
package logs

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
)

const (
	// DefaultScanBudget is the wall time the agent may spend assembling one
	// finite initial selection. It is a ceiling on the whole scan, including
	// every widening round, and it ends at scan completion rather than at
	// first output. A deployment may shorten it; nothing may lengthen it.
	DefaultScanBudget = 60 * time.Second

	// DefaultMaxEntryBudget caps the candidate entry budget handed to
	// journalctl -n across widening rounds. Reaching it fails the request
	// loudly rather than returning a short page.
	DefaultMaxEntryBudget int64 = 1 << 22

	// DefaultMaxBufferBytes caps the rendered candidate set held in memory for
	// one query. It sits above the API's own response ceiling so a legitimate
	// maximum-size response is never rejected by the agent's working buffer.
	DefaultMaxBufferBytes = 128 << 20

	// DefaultMaxLineCount caps the number of rendered lines held as string
	// metadata for one query. Each line is a 16-byte string header; without a
	// cap, newline-dense content under DefaultMaxBufferBytes can amplify into
	// metadata that exceeds the intended memory budget.
	DefaultMaxLineCount int64 = 1 << 20
)

// Snapshot is one finite, fully assembled selection of log lines.
type Snapshot struct {
	// Lines are the selected rendered logical content lines, oldest first.
	// Each element excludes its terminator; a caller reconstituting the source
	// text joins them with "\n". An unterminated final line in the source is
	// one line here, indistinguishable from a terminated one.
	Lines []string

	// Exhausted reports that the scan reached the start of the filtered result
	// rather than stopping at a budget. A page shorter than the caller asked
	// for is legitimate only when this is true; otherwise the scan failed and
	// no Snapshot is returned at all.
	Exhausted bool
}

// SplitRenderedLines splits rendered source output into logical content lines.
//
// A trailing newline terminates the final line and does not begin another, so
// "a\nb\n" and "a\nb" are both two lines. Nothing is trimmed: leading and
// trailing whitespace and blank lines inside an entry are content.
func SplitRenderedLines(out string) []string {
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// SplitRenderedLinesBounded splits rendered output like SplitRenderedLines but
// rejects output whose line count exceeds maxLines. The count is computed
// before the slice is allocated, so newline-dense content under the byte cap
// cannot amplify into unbounded string-header metadata.
func SplitRenderedLinesBounded(out string, maxLines int64) ([]string, error) {
	if out == "" {
		return nil, nil
	}
	n := int64(strings.Count(out, "\n"))
	if len(out) > 0 && out[len(out)-1] != '\n' {
		n++ // unterminated last line
	}
	if maxLines > 0 && n > maxLines {
		return nil, fmt.Errorf("%w: rendered output has %d lines, exceeding the %d-line limit",
			errors.ErrLogBufferExceeded, n, maxLines)
	}
	return SplitRenderedLines(out), nil
}

// TailWindow returns the tailLines lines ending at len(lines)-lineOffset: drop
// the trailing lineOffset lines, then take the last tailLines of what remains.
//
// Boundaries are line boundaries, not entry boundaries. A page may begin or end
// inside one journal entry's rendered block, and that block is neither
// re-rendered nor padded.
func TailWindow(lines []string, tailLines, lineOffset int64) []string {
	end := int64(len(lines)) - lineOffset
	if end <= 0 {
		return nil
	}
	start := end - tailLines
	if start < 0 {
		start = 0
	}
	return lines[start:end]
}

// HeadWindow returns lines[lineOffset:], the head-relative form used when no
// tailLines is given: there is no window size to anchor at the end, so the
// first lineOffset lines are skipped and the remainder returned. An offset at
// or beyond the end is an empty result, not an error.
func HeadWindow(lines []string, lineOffset int64) []string {
	if lineOffset >= int64(len(lines)) {
		return nil
	}
	return lines[lineOffset:]
}
