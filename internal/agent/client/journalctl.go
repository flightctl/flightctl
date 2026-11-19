package client

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	journalctlCommand = "/usr/bin/journalctl"

	// JournalFormatCat renders the message field alone, with no timestamp,
	// hostname or unit prefix. It is what the agent's own internal log
	// collection has always asked for.
	JournalFormatCat = "cat"
	// JournalFormatShort is journalctl's default rendering and the one the
	// device log API returns, byte-identical to what operators read today.
	JournalFormatShort = "short"

	// JournalMaxUnitLength is the longest unit name or pattern accepted. A
	// systemd unit name cannot exceed 256 bytes, so a longer value is a
	// malformed request rather than a pattern that merely matches nothing.
	JournalMaxUnitLength = 256

	// journalTimestampLayout is the systemd.time(7) absolute form, to the
	// microsecond. The zone is appended separately: "UTC" is not a layout
	// element, and spelling it out keeps the intent obvious.
	journalTimestampLayout = "2006-01-02 15:04:05.000000"
)

// JournalPriority is a syslog severity named as the device log API names it.
// Filtering on a priority selects that level and every more severe one.
type JournalPriority string

const (
	JournalPriorityEmergency JournalPriority = "emergency"
	JournalPriorityAlert     JournalPriority = "alert"
	JournalPriorityCritical  JournalPriority = "critical"
	JournalPriorityError     JournalPriority = "error"
	JournalPriorityWarning   JournalPriority = "warning"
	JournalPriorityNotice    JournalPriority = "notice"
	JournalPriorityInfo      JournalPriority = "info"
	JournalPriorityDebug     JournalPriority = "debug"
)

// journalPriorityLevels maps each named priority onto its syslog numeric
// severity. The numeric form is what reaches journalctl: it accepts both the
// number and its own short names ("emerg", "crit", "err"), but those short
// names are not the names this API uses, so translating to the number avoids
// maintaining a second naming scheme that only coincidentally lines up.
var journalPriorityLevels = map[JournalPriority]int{
	JournalPriorityEmergency: 0,
	JournalPriorityAlert:     1,
	JournalPriorityCritical:  2,
	JournalPriorityError:     3,
	JournalPriorityWarning:   4,
	JournalPriorityNotice:    5,
	JournalPriorityInfo:      6,
	JournalPriorityDebug:     7,
}

// Level returns the syslog numeric severity for p, and whether p is a priority
// this API defines.
func (p JournalPriority) Level() (int, bool) {
	level, ok := journalPriorityLevels[p]
	return level, ok
}

// JournalBoot selects which boot the journal is read from.
type JournalBoot string

const (
	JournalBootCurrent JournalBoot = "current"
	// JournalBootPrevious reads the boot before the current one. It returns
	// data only on a device whose journal persists across reboots; supported
	// device images keep journald volatile, so the ordinary outcome is
	// errors.ErrLogBootUnavailable.
	JournalBootPrevious JournalBoot = "previous"
)

// Offset returns the journalctl -b argument for b, and whether b is a selector
// this API defines.
func (b JournalBoot) Offset() (string, bool) {
	switch b {
	case JournalBootCurrent:
		return "0", true
	case JournalBootPrevious:
		return "-1", true
	default:
		return "", false
	}
}

// journalBootUnavailableMarkers are the lowercased stderr fragments systemd
// emits across supported versions when the requested boot holds no data. They
// are matched before the generic stderr classification, which would otherwise
// fold "No such boot ID in journal" into the generic not-found error.
var journalBootUnavailableMarkers = []string{
	"no such boot id",
	"data from the specified boot",
	"specified boot id or offset does not exist",
	"failed to look up boot",
}

// ValidateJournalUnit reports whether unit is a unit name or pattern this API
// accepts.
//
// The charset is [0-9a-zA-Z:@._\-!*?]. '*' and '?' are the wildcards
// journalctl -u honours and the only ones supported; '[' and ']' are
// deliberately absent, so a character-class pattern is rejected rather than
// passed through to be matched literally and silently return nothing. Every
// shell metacharacter is outside the charset too, which is defence in depth
// only: journalctl is invoked with an argv array and never through a shell.
func ValidateJournalUnit(unit string) error {
	if unit == "" {
		return fmt.Errorf("%w: unit pattern is empty", errors.ErrLogInvalidParameters)
	}
	if len(unit) > JournalMaxUnitLength {
		return fmt.Errorf("%w: unit pattern is %d bytes, the maximum is %d", errors.ErrLogInvalidParameters, len(unit), JournalMaxUnitLength)
	}
	for _, r := range unit {
		if !isJournalUnitRune(r) {
			return fmt.Errorf("%w: unit pattern %q contains unsupported character %q", errors.ErrLogInvalidParameters, unit, r)
		}
	}
	return nil
}

func isJournalUnitRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	}
	switch r {
	case ':', '@', '.', '_', '-', '!', '*', '?':
		return true
	}
	return false
}

// JournalTickCeil rounds t up to the next journal microsecond tick, the
// smallest representable instant at or after t. It is the correct rounding for
// an inclusive lower bound: no entry between t and the returned tick can exist,
// so nothing in range is excluded.
func JournalTickCeil(t time.Time) time.Time {
	rem := time.Duration(t.Nanosecond() % int(time.Microsecond))
	if rem == 0 {
		return t
	}
	return t.Add(time.Microsecond - rem)
}

// JournalTickFloor rounds t down to the journal microsecond tick at or before
// t. It is the correct rounding for an inclusive upper bound.
func JournalTickFloor(t time.Time) time.Time {
	return t.Add(-time.Duration(t.Nanosecond() % int(time.Microsecond)))
}

// FormatJournalTimestamp renders t in the absolute systemd.time(7) form
// journalctl accepts for --since and --until, to the microsecond and in UTC.
// Falling back to whole seconds would silently widen or narrow an inclusive
// bound, so the fractional part is always written.
func FormatJournalTimestamp(t time.Time) string {
	return t.UTC().Format(journalTimestampLayout) + " UTC"
}

func NewJournalctl(exec executer.Executer, user v1beta1.Username) *Journalctl {
	return &Journalctl{
		exec: exec,
		user: user,
	}
}

type Journalctl struct {
	exec executer.Executer
	user v1beta1.Username
}

type logOptions struct {
	args     []string
	format   string
	maxBytes int
	err      error
}

// fail records the first option error. An option that cannot be expressed must
// not quietly drop its filter: dropping one widens the result set, which is the
// one way a log query can be wrong without looking wrong.
func (o *logOptions) fail(err error) {
	if o.err == nil {
		o.err = err
	}
}

type LogOptions func(*logOptions)

func WithLogUnit(unit string) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "-u", unit)
	}
}

func WithLogTag(tag string) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "-t", tag)
	}
}

func WithLogSince(t time.Time) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "--since", t.Format("2006-01-02 15:04:05"))
	}
}

// WithLogOutputFormat selects the journalctl rendering. It defaults to
// JournalFormatShort.
func WithLogOutputFormat(format string) LogOptions {
	return func(o *logOptions) {
		o.format = format
	}
}

// WithLogPriority filters to priority and every more severe level.
func WithLogPriority(priority JournalPriority) LogOptions {
	return func(o *logOptions) {
		level, ok := priority.Level()
		if !ok {
			o.fail(fmt.Errorf("%w: unknown priority %q", errors.ErrLogInvalidParameters, priority))
			return
		}
		o.args = append(o.args, "-p", strconv.Itoa(level))
	}
}

// WithLogBoot restricts the read to one boot.
func WithLogBoot(boot JournalBoot) LogOptions {
	return func(o *logOptions) {
		offset, ok := boot.Offset()
		if !ok {
			o.fail(fmt.Errorf("%w: unknown boot selector %q", errors.ErrLogInvalidParameters, boot))
			return
		}
		o.args = append(o.args, "-b", offset)
	}
}

// WithLogSinceSeconds bounds the read to the last seconds seconds, relative to
// the device's clock at the moment journalctl runs.
func WithLogSinceSeconds(seconds int64) LogOptions {
	return func(o *logOptions) {
		if seconds < 1 {
			o.fail(fmt.Errorf("%w: sinceSeconds must be at least 1, got %d", errors.ErrLogInvalidParameters, seconds))
			return
		}
		o.args = append(o.args, "--since", fmt.Sprintf("%d seconds ago", seconds))
	}
}

// WithLogSinceTimestamp sets an inclusive lower bound at t, rounded up to the
// next representable journal tick.
func WithLogSinceTimestamp(t time.Time) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "--since", FormatJournalTimestamp(JournalTickCeil(t)))
	}
}

// WithLogUntilTimestamp sets an inclusive upper bound at t, rounded down to the
// representable journal tick at or before it.
func WithLogUntilTimestamp(t time.Time) LogOptions {
	return func(o *logOptions) {
		o.args = append(o.args, "--until", FormatJournalTimestamp(JournalTickFloor(t)))
	}
}

// WithLogMaxEntries bounds the candidate set journalctl returns to the last
// entries entries.
//
// This is a candidate-query knob and never a page size: -n counts journal
// entries, and under the short format one entry with embedded newlines renders
// as several lines. Callers that need a line-denominated window compute it over
// the rendered output.
func WithLogMaxEntries(entries int64) LogOptions {
	return func(o *logOptions) {
		if entries < 1 {
			o.fail(fmt.Errorf("%w: entry budget must be at least 1, got %d", errors.ErrLogInvalidParameters, entries))
			return
		}
		o.args = append(o.args, "-n", strconv.FormatInt(entries, 10))
	}
}

// WithLogMaxOutputBytes bounds the stdout the agent buffers for one invocation.
// An overrun fails the call with errors.ErrLogBufferExceeded rather than
// returning the bytes that fit, which would be a silently truncated page.
func WithLogMaxOutputBytes(maxBytes int) LogOptions {
	return func(o *logOptions) {
		o.maxBytes = maxBytes
	}
}

// Logs returns the journal as a slice of non-empty, whitespace-trimmed message
// bodies. It is the agent's own internal log collection path and is unchanged.
func (j *Journalctl) Logs(ctx context.Context, options ...LogOptions) ([]string, error) {
	// A caller-supplied format still wins: this only moves the historic default
	// out of the body so Output can default to short.
	stdout, err := j.run(ctx, append([]LogOptions{WithLogOutputFormat(JournalFormatCat)}, options...)...)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var result []string
	for _, line := range lines {
		if line != "" {
			result = append(result, strings.TrimSpace(line))
		}
	}

	return result, nil
}

// Output runs journalctl with the given options and returns stdout verbatim.
//
// Unlike Logs it neither trims nor drops anything: the device log API returns
// the journal byte-identically to what the source renders, and a blank line
// inside an entry is content.
func (j *Journalctl) Output(ctx context.Context, options ...LogOptions) (string, error) {
	return j.run(ctx, options...)
}

func (j *Journalctl) run(ctx context.Context, options ...LogOptions) (string, error) {
	opts := logOptions{format: JournalFormatShort}
	// TODO: it appears user systemd instances on RHEL distros generally write to the system journal
	// instead of user specific ones and in fact do not even support reading from user-based journals
	// for general users.
	/*if !j.user.IsRootUser() {
	  opts.args = append(opts.args, "--user", "-M", j.user.String()+"@")
	}*/
	for _, option := range options {
		option(&opts)
	}
	if opts.err != nil {
		return "", opts.err
	}

	// journalctl is always invoked with an argv array -- never a shell string,
	// never bash -c, never sudo -- so no filter value can be anything but data.
	//
	// --quiet suppresses journalctl's informational lines, of which
	// "-- No entries --" is the one that matters: it is written to stdout, so
	// without this flag an empty result arrives as a line of log content.
	args := append([]string{"--no-pager", "--quiet", "-o", opts.format}, opts.args...)

	var stdout, stderr string
	var exitCode int
	if opts.maxBytes > 0 {
		// The bounded executer splits its allowance evenly between stdout and
		// stderr, so ask for twice the bound plus one byte: that gives stdout
		// one byte beyond the limit, which is what makes an overrun detectable
		// instead of silently truncating.
		stdout, stderr, exitCode = j.exec.ExecuteWithBoundedOutputFromDir(ctx, "", journalctlCommand, args, 2*(opts.maxBytes+1))
	} else {
		stdout, stderr, exitCode = j.exec.ExecuteWithContext(ctx, journalctlCommand, args...)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("journalctl logs: %w", classifyJournalStderr(stderr, exitCode))
	}
	if opts.maxBytes > 0 && len(stdout) > opts.maxBytes {
		return "", fmt.Errorf("journalctl logs: %w: output exceeds %d bytes", errors.ErrLogBufferExceeded, opts.maxBytes)
	}

	return stdout, nil
}

// classifyJournalStderr maps a failed journalctl invocation onto an error.
// Absence of the requested boot is separated out first because it is an
// ordinary outcome of boot=previous on a device with a volatile journal, not a
// device fault, and the caller reports it distinctly.
func classifyJournalStderr(stderr string, exitCode int) error {
	lowered := strings.ToLower(stderr)
	for _, marker := range journalBootUnavailableMarkers {
		if strings.Contains(lowered, marker) {
			return fmt.Errorf("%w: code: %d: %s", errors.ErrLogBootUnavailable, exitCode, strings.TrimSpace(stderr))
		}
	}
	return errors.FromStderr(stderr, exitCode)
}
