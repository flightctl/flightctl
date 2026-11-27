# Device log retrieval

The device log API returns a snapshot of a device's logs. This document
describes the agent-side journal source: how a request's filters become a
`journalctl` invocation, and how a page of lines is selected from what comes
back.

It is reached over the [agent link](agent-link.md), as the `device.logs.get`
method the agent implements in the responder role. **No REST endpoint reaches
it yet**: nothing selects a link for an HTTP caller and nothing forwards a
result across replicas.

## Invocation

`journalctl` is run through the existing `executer.Executer` with an **argv
array** — never a shell string, never `bash -c`, never `sudo`. Every filter
value is therefore data: a unit pattern of `a; touch /tmp/x` is one argument,
matches no unit, and executes nothing.

Every invocation passes `--no-pager` and `--quiet`. `--quiet` matters for
correctness, not noise: journalctl writes `-- No entries --` to **stdout**, so
without it an empty result arrives as a line of log content.

The output format is journalctl's default `short`, byte-identical to what
operators read today.

## Filter mapping

| Request field | journalctl arguments |
|---|---|
| `units` | `-u <unit>`, repeated once per entry |
| `priority` | `-p <level>`, the syslog number — that level and every more severe one |
| `boot: current` / `previous` | `-b 0` / `-b -1` |
| `sinceSeconds` | `--since "<N> seconds ago"` |
| `sinceTime` | `--since "YYYY-MM-DD HH:MM:SS.ffffff UTC"` |
| `untilTime` | `--until "YYYY-MM-DD HH:MM:SS.ffffff UTC"` |

Named priorities are translated to their syslog numbers rather than passed
through. journalctl accepts names, but its own are the syslog short forms
(`emerg`, `crit`, `err`), which are not the names this API uses; the number is
the one spelling both agree on.

### Unit patterns

A unit must be 1–256 bytes from the charset `[0-9a-zA-Z:@._\-!*?]`. `*` and `?`
are the wildcards `journalctl -u` honours and the only ones supported. `[` and
`]` are deliberately **outside** the charset, so a character-class pattern is
rejected rather than passed through to be matched literally and silently return
nothing. Every shell metacharacter is outside the charset too — defence in
depth on top of the argv invocation, not the thing that makes it safe.

### Time bounds

Both bounds are **inclusive**, and both are normalized to the microsecond
resolution journal timestamps actually have: the lower bound is rounded **up**
to the next representable tick, the upper bound **down** to the tick at or
before it. Rounding outwards instead would admit entries outside the requested
interval; falling back to whole seconds would do so by up to a second. When the
two bounds fall inside one microsecond and cross over after rounding, the
interval contains no representable tick and the result is an empty success —
the device is not asked at all.

`sinceSeconds` and `sinceTime` are mutually exclusive. Both present is a
rejection, not a precedence rule: silently preferring one answers a question
the caller did not ask.

### Previous boot

`boot: previous` returns data only where journald persists across reboots.
Supported device images keep it volatile, so the ordinary outcome is
`errors.ErrLogBootUnavailable`, classified from journalctl's stderr before the
generic stderr classification — which would otherwise fold "No such boot ID in
journal" into a generic not-found error.

## The rendered-line window

Every count the API quotes — `tailLines`, `lineOffset`, the size of a page — is
denominated in **rendered logical content lines**: the output as the source
renders it, after all filters, in `short` format. Under `short` an entry with
embedded newlines renders as several lines, so `journalctl -n N`, which counts
**entries**, cannot implement the window. `-n` is a *candidate-query* knob only.

With `tailLines` the window is end-relative: drop the trailing `lineOffset`
lines, then take the last `tailLines` of what remains. Without `tailLines` it is
head-relative — the first `lineOffset` lines are skipped and the remainder
returned — and no entry budget is used at all, because the whole filtered result
is needed.

Boundaries are line boundaries, not entry boundaries. A page may begin or end
inside one entry's rendered block, and that block is not re-rendered or padded.

The end-relative scan is:

1. **Collect conservatively.** Query with an entry budget of
   `tailLines + lineOffset`. An entry renders to at least one line, so this can
   never overshoot the window, and it is exactly right whenever no entry is
   multi-line.
2. **Widen if short.** If the candidate set holds fewer rendered lines than the
   window needs and the filtered result is not exhausted, re-query with a larger
   budget. The next budget projects the observed lines-per-entry forward, with
   doubling as a floor so a round that learned nothing still makes progress.
3. **Window over rendered lines**, as above.

When no explicit upper time bound is set, the scan pins the query at the
scan-start time (`--until <scan_time>`). This creates a stable query boundary
so that entries appended during the scan cannot shift the tail window between
widening rounds.

### Knowing when to stop

Each candidate set is a *suffix* of the filtered result, so a larger budget can
only extend it at the old end. The filtered result is considered exhausted when
both the **oldest rendered line** and the **total rendered line count** are
unchanged between two rounds. The count check prevents false exhaustion when
distinct entries happen to share the same oldest rendered text: a wider budget
that finds additional entries always increases the rendered line count.

### Failing loudly

A page shorter than requested is legitimate **only** when the filtered result is
exhausted — that is end-of-data, not truncation. If the scan budget or the entry
budget is reached before the window can be filled, the request fails with
`errors.ErrLogScanIncomplete`. It never returns a short or shifted page as a
success, and there is no byte-based truncation on this path.

## Budgets

| Budget | Default | What it bounds |
|---|---|---|
| Scan budget | 60 s | Wall time to assemble one snapshot, across every widening round. A deployment may shorten it; nothing may lengthen it |
| Entry budget cap | 2²² entries | How far `-n` may be widened before the scan fails |
| Working buffer | 128 MiB | stdout buffered for one invocation. An overrun fails the call rather than returning the bytes that fit |
| Rendered-line cap | 2²⁰ lines | Rendered logical content lines per query. Newlines are counted before the string slice is allocated; exceeding the cap fails with `ErrLogBufferExceeded`. This prevents newline-dense content under the byte cap from amplifying into unbounded string-header metadata |

The scan budget ends at scan **completion**, not at first output. It is not a
follow lifetime and not an inactivity timer.

## Over the agent link

The method is `device.logs.get`: one `request` from the service, zero or more
`data` messages from the agent, exactly one terminal `result`.

### Parameters

`params` names a `source` of `"journal"` or `"file"` and carries exactly one
matching source object. `tailLines` and `lineOffset` sit beside it and apply to
either source, because both are denominated in rendered lines and neither is a
journal concept. `params.journal` carries the filters in the table above; an
instant has one accepted spelling, an RFC 3339 UTC instant with a literal `Z`
offset and microsecond precision.

Three structural rules are rejections rather than silent precedence, because
each would otherwise answer a question the caller did not ask: the source
object must match `source`, `sinceSeconds` and `sinceTime` are mutually
exclusive, and `lineOffset` may not accompany `follow`. Unknown members are
rejected rather than ignored, which is what keeps a byte-denominated limit
from returning under a new name and being quietly discarded.

**This build serves the journal source and finite snapshots only.** `follow`
and `source: "file"` decode — they are part of the schema — and are then
refused with `RetrievalFailed`. Not `MethodNotSupported`: that reason is a
verdict about the whole method, and the agent does implement it.

### Content messages

The page is assembled in full before the first byte is sent, so every failure
is a pre-content failure and arrives as a `result` with no `data` in front of
it. There is no partial page followed by an error.

Each line is written with its terminator, so a message ends on a newline
boundary and no reader buffers waiting for one. Chunk capacity is a compiled-in
constant inside the agent — 32 KiB of decoded text — and is **not** a result
limit: a larger snapshot is more messages and the same snapshot. It is sized
against the *escaped* form, because a JSON string expands under escaping by up
to six bytes per input byte for a control character written as `\u00XX`; six
times 32 KiB leaves the envelope comfortable room inside the 256 KiB composing
bound.

A single logical line longer than one chunk is the exception: it is split
across messages on a UTF-8 rune boundary, never mid-code-point, and the message
carrying its first part does not end with a newline, which is how a
continuation is marked. Message boundaries are invisible either way — the lines
a caller reconstitutes are the lines the source rendered.

An empty selection sends no `data` message at all. The `OK` alone says the
filtered result was empty, which a zero-length content message would say no
more clearly and a reader would have to tell apart from a flag-only one.

### Failure reasons

| Device-side error | `status.reason` |
|---|---|
| `ErrLogInvalidParameters`, and anything unmapped | `RetrievalFailed` |
| `ErrLogBootUnavailable` | `BootUnavailable` |
| `ErrLogBufferExceeded` | `ResponseTooLarge` |
| `ErrLogScanIncomplete` | `DeviceTimeout` |
| `ErrNotFound` | `SourceNotFound` |
| The request's context was cancelled | `Cancelled` |

Cancellation is checked before the error, and from the context rather than
from the error: it means the originator already fixed this request's outcome,
so whatever the source reported on its way out is a consequence and not the
cause.

Both ways the scan can run out of room — the wall-clock budget and the entry
budget — report `DeviceTimeout`. They are one reason because the remedy is the
same, a narrower filter or a smaller page; the `status.message` is what
separates them for an operator.

## Where the code lives

| Path | Role |
|---|---|
| `internal/agent/client/journalctl.go` | argv construction, unit-charset validation, timestamp normalization, stderr classification |
| `internal/agent/device/logs/journal.go` | request validation, the collect-and-widen scan |
| `internal/agent/device/logs/logs.go` | rendered-line splitting and the window, shared with the file source |
| `internal/agentlink/devicelogs/` | the `device.logs.get` params and payload schema, shared by both ends |
| `internal/agent/device/link/devicelogs.go` | the method handler: parameter mapping, chunking, reason mapping |
