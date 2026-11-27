// Package devicelogs is the wire schema of the device.logs.get method: its
// params, its data payload, and the strict decoders both ends use.
//
// It is separate from the envelope because they version separately. Adding a
// method or changing what one of its params means is not an envelope change
// and involves no subprotocol bump; the envelope only has to carry a method
// name, a params object and a payload object, and knows nothing about what is
// in them.
//
// Like the envelope, the package is pure: no I/O, no goroutines, and shared
// verbatim by the agent, which decodes params and encodes payloads, and the
// service, which does the reverse. One schema, enforced once.
package devicelogs

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
)

// Method is the request method name.
//
// The allowlist that resolves it is keyed by method *and* responder role. This
// method is implemented by the agent as responder, for a service-originated
// request, and in no other combination: a request naming it with
// "origin":"agent" asks the service to read logs from the control-plane host,
// which no endpoint implements and which is answered with one
// MethodNotSupported.
const Method = "device.logs.get"

// Sources a request may name.
const (
	SourceJournal = "journal"
	SourceFile    = "file"
)

// MaxUnits bounds how many unit patterns one request may carry. The message
// bound already limits this; the explicit cap is so the refusal names the
// reason rather than arriving as an opaque oversized-message fault.
const MaxUnits = 128

// timestampLayout is the one accepted spelling of an instant on this wire: an
// RFC 3339 UTC instant with a literal Z offset and microsecond precision, the
// resolution journal timestamps actually have.
const timestampLayout = "2006-01-02T15:04:05.000000Z"

var (
	paramsMembers = map[string]bool{
		"source": true, "journal": true, "file": true,
		"follow": true, "tailLines": true, "lineOffset": true,
	}
	journalMembers = map[string]bool{
		"units": true, "priority": true, "boot": true,
		"sinceSeconds": true, "sinceTime": true, "untilTime": true,
	}
	fileMembers    = map[string]bool{"path": true}
	payloadMembers = map[string]bool{
		"content": true, "backlogComplete": true, "truncated": true, "rotated": true,
	}
)

// Params is one decoded device.logs.get request.
type Params struct {
	// Source is SourceJournal or SourceFile and selects which of the two
	// source objects below is present.
	Source string
	// Journal is set when Source is SourceJournal, and nil otherwise.
	Journal *JournalParams
	// File is set when Source is SourceFile, and nil otherwise.
	File *FileParams
	// Follow requests a live stream rather than a finite snapshot.
	Follow bool
	// TailLines is the maximum number of rendered logical content lines to
	// return, counted from the end of the filtered result. Zero means absent;
	// the wire minimum is 1, so no present value collides with that.
	TailLines int64
	// LineOffset is an offset in rendered logical content lines: from the end
	// of the result when TailLines is set, from the head when it is not.
	//
	// It is a pointer rather than a plain int64 because zero is a legitimate
	// present value and presence is what the follow rule turns on: an explicit
	// "lineOffset": 0 alongside "follow": true is rejected, since there is no
	// end of a live stream to count back from.
	LineOffset *int64
}

// JournalParams filters a journal read.
type JournalParams struct {
	// Units are unit names, or patterns using the '*' and '?' wildcards. The
	// charset is validated by the source, not here: this package decodes the
	// wire shape and the source decides what it can act on.
	Units []string
	// Priority selects that severity and every more severe one.
	Priority string
	// Boot selects the current or the previous boot.
	Boot string
	// SinceSeconds bounds the read to the last N seconds. Zero means absent.
	SinceSeconds int64
	// SinceTime is an inclusive lower bound.
	SinceTime *time.Time
	// UntilTime is an inclusive upper bound.
	UntilTime *time.Time
}

// FileParams names a file to read.
type FileParams struct {
	// Path is an absolute device path that must resolve within a permitted
	// root. The root is enforced by the source, not here.
	Path string
}

// Payload is one data message's body.
type Payload struct {
	// Content is log text. A message whose content does not end with a
	// newline is a continuation of the same logical line: message boundaries
	// are invisible to the reader and never change which lines a result has.
	Content string
	// BacklogComplete marks the backlog-to-live transition of a follow.
	BacklogComplete bool
	// Truncated reports that a line cap was reached, making this the final
	// content message.
	Truncated bool
	// Rotated reports the first content from a replacement file after
	// rotation. It is informational; the retrieval continues.
	Rotated bool
}

// DecodeParams validates and decodes a device.logs.get params object.
//
// Three structural rules are rejections rather than silent precedence, because
// each of them would otherwise answer a question the caller did not ask:
// exactly one of journal and file is present and it matches source; at most
// one of sinceSeconds and sinceTime is present; and lineOffset is rejected
// when follow is true.
//
// Every failure is request-scoped: the caller answers one result and starts no
// source work.
func DecodeParams(params *protocol.Value) (*Params, error) {
	if err := protocol.RejectUnknownMembers(params, paramsMembers, "params"); err != nil {
		return nil, err
	}

	source, present, err := protocol.StringMember(params, "source", "params")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("missing required member %q in params", "source")
	}
	if source != SourceJournal && source != SourceFile {
		return nil, fmt.Errorf("member %q in params is %q, not %q or %q",
			"source", trim(source), SourceJournal, SourceFile)
	}

	out := &Params{Source: source}

	if out.Follow, _, err = protocol.BoolMember(params, "follow", "params"); err != nil {
		return nil, err
	}
	if out.TailLines, _, err = protocol.Int64Member(params, "tailLines", "params", 1, math.MaxInt64); err != nil {
		return nil, err
	}
	offset, offsetPresent, err := protocol.Int64Member(params, "lineOffset", "params", 0, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if offsetPresent {
		if out.Follow {
			// A live stream has no end to count back from and no fixed head to
			// skip forward from, so an offset into it is not a request that can
			// be answered rather than one with an obvious reading.
			return nil, fmt.Errorf("member %q in params may not be combined with %q", "lineOffset", "follow")
		}
		out.LineOffset = &offset
	}

	journal, hasJournal, err := protocol.ObjectMember(params, "journal", "params")
	if err != nil {
		return nil, err
	}
	file, hasFile, err := protocol.ObjectMember(params, "file", "params")
	if err != nil {
		return nil, err
	}
	if hasJournal == hasFile {
		return nil, fmt.Errorf("exactly one of %q and %q must be present in params", "journal", "file")
	}
	if hasJournal != (source == SourceJournal) {
		return nil, fmt.Errorf("source %q in params does not match the source object present", source)
	}

	if hasJournal {
		if out.Journal, err = decodeJournalParams(journal); err != nil {
			return nil, err
		}
	} else {
		if out.File, err = decodeFileParams(file); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func decodeJournalParams(obj *protocol.Value) (*JournalParams, error) {
	const where = "params.journal"
	if err := protocol.RejectUnknownMembers(obj, journalMembers, where); err != nil {
		return nil, err
	}

	out := &JournalParams{}
	var err error
	if out.Units, _, err = protocol.StringArrayMember(obj, "units", where, MaxUnits); err != nil {
		return nil, err
	}
	if out.Priority, _, err = protocol.StringMember(obj, "priority", where); err != nil {
		return nil, err
	}
	if out.Boot, _, err = protocol.StringMember(obj, "boot", where); err != nil {
		return nil, err
	}
	if out.SinceSeconds, _, err = protocol.Int64Member(obj, "sinceSeconds", where, 1, math.MaxInt64); err != nil {
		return nil, err
	}
	if out.SinceTime, err = timestampMember(obj, "sinceTime", where); err != nil {
		return nil, err
	}
	if out.UntilTime, err = timestampMember(obj, "untilTime", where); err != nil {
		return nil, err
	}

	// Rejected rather than resolved by precedence.
	if out.SinceSeconds > 0 && out.SinceTime != nil {
		return nil, fmt.Errorf("members %q and %q in %s are mutually exclusive", "sinceSeconds", "sinceTime", where)
	}
	return out, nil
}

func decodeFileParams(obj *protocol.Value) (*FileParams, error) {
	const where = "params.file"
	if err := protocol.RejectUnknownMembers(obj, fileMembers, where); err != nil {
		return nil, err
	}
	path, present, err := protocol.StringMember(obj, "path", where)
	if err != nil {
		return nil, err
	}
	if !present || path == "" {
		return nil, fmt.Errorf("missing required member %q in %s", "path", where)
	}
	return &FileParams{Path: path}, nil
}

// timestampMember reads an optional instant.
//
// Exactly one spelling is accepted. An offset other than Z is rejected rather
// than converted, because a caller that wrote one is reasoning in a timezone
// the device does not share; and sub-microsecond digits are rejected rather
// than rounded, because the bound would then not be the bound that was asked
// for and the rounding direction would be invisible.
func timestampMember(obj *protocol.Value, name, where string) (*time.Time, error) {
	raw, present, err := protocol.StringMember(obj, name, where)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	parsed, err := time.Parse(timestampLayout, raw)
	if err != nil {
		return nil, fmt.Errorf("member %q in %s is not an RFC 3339 UTC instant with microsecond precision and a Z offset: %q",
			name, where, trim(raw))
	}
	utc := parsed.UTC()
	return &utc, nil
}

// EncodeParams builds the params object for a request. It is the originator's
// half of the same schema DecodeParams enforces, so a request this function
// builds is one that function accepts.
func EncodeParams(p *Params) (json.RawMessage, error) {
	if p == nil {
		return nil, fmt.Errorf("params are required")
	}

	wire := map[string]any{"source": p.Source}
	if p.Follow {
		wire["follow"] = true
	}
	if p.TailLines > 0 {
		wire["tailLines"] = p.TailLines
	}
	if p.LineOffset != nil {
		wire["lineOffset"] = *p.LineOffset
	}

	switch p.Source {
	case SourceJournal:
		if p.Journal == nil {
			return nil, fmt.Errorf("source %q requires journal parameters", SourceJournal)
		}
		journal := map[string]any{}
		if len(p.Journal.Units) > 0 {
			journal["units"] = p.Journal.Units
		}
		if p.Journal.Priority != "" {
			journal["priority"] = p.Journal.Priority
		}
		if p.Journal.Boot != "" {
			journal["boot"] = p.Journal.Boot
		}
		if p.Journal.SinceSeconds > 0 {
			journal["sinceSeconds"] = p.Journal.SinceSeconds
		}
		if p.Journal.SinceTime != nil {
			journal["sinceTime"] = FormatTimestamp(*p.Journal.SinceTime)
		}
		if p.Journal.UntilTime != nil {
			journal["untilTime"] = FormatTimestamp(*p.Journal.UntilTime)
		}
		wire["journal"] = journal
	case SourceFile:
		if p.File == nil {
			return nil, fmt.Errorf("source %q requires file parameters", SourceFile)
		}
		wire["file"] = map[string]any{"path": p.File.Path}
	default:
		return nil, fmt.Errorf("unknown source %q", trim(p.Source))
	}

	return json.Marshal(wire)
}

// FormatTimestamp renders an instant in the one spelling the wire accepts.
// Sub-microsecond digits are truncated here rather than rejected, because this
// is the originator stating a bound it has already chosen.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(timestampLayout)
}

// EncodePayload builds one data message body.
//
// Absent flags are omitted rather than written as false. The decoder treats
// absence and false identically for these three, so the choice is only about
// how many bytes a chunk of ordinary content costs.
func EncodePayload(p Payload) (json.RawMessage, error) {
	wire := map[string]any{"content": p.Content}
	if p.BacklogComplete {
		wire["backlogComplete"] = true
	}
	if p.Truncated {
		wire["truncated"] = true
	}
	if p.Rotated {
		wire["rotated"] = true
	}
	return json.Marshal(wire)
}

// DecodePayload validates and decodes one data message body.
func DecodePayload(payload *protocol.Value) (*Payload, error) {
	const where = "payload"
	if err := protocol.RejectUnknownMembers(payload, payloadMembers, where); err != nil {
		return nil, err
	}

	content, present, err := protocol.StringMember(payload, "content", where)
	if err != nil {
		return nil, err
	}
	if !present {
		// The empty string is a legitimate content value -- a flag-only
		// message carries one -- so absence cannot be read as empty.
		return nil, fmt.Errorf("missing required member %q in %s", "content", where)
	}

	out := &Payload{Content: content}
	if out.BacklogComplete, _, err = protocol.BoolMember(payload, "backlogComplete", where); err != nil {
		return nil, err
	}
	if out.Truncated, _, err = protocol.BoolMember(payload, "truncated", where); err != nil {
		return nil, err
	}
	if out.Rotated, _, err = protocol.BoolMember(payload, "rotated", where); err != nil {
		return nil, err
	}
	return out, nil
}

// trim bounds peer input placed in a diagnostic, so a refusal never echoes an
// unbounded string back onto the wire.
func trim(s string) string {
	const max = 64
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "..."
}
