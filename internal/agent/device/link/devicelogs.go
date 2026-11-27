package link

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/logs"
	"github.com/flightctl/flightctl/internal/agentlink/devicelogs"
	"github.com/flightctl/flightctl/internal/agentlink/link"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/flightctl/flightctl/pkg/log"
)

// ContentChunkBytes bounds how much decoded log text rides in one data
// message.
//
// It is an implementation-owned constant inside the agent: not negotiated, not
// configurable, and not a result limit. It never shortens, caps or terminates
// a result -- a snapshot larger than one chunk is sent as several data
// messages and is the same snapshot.
//
// It is sized against the *escaped* form, because a JSON string expands under
// escaping by up to six bytes per input byte for control characters written as
// \u00XX. Six times this constant is 192 KiB, which leaves the envelope's
// routing members and the payload's own keys comfortable room inside the
// 256 KiB composing bound, so a legitimate chunk can never be refused before
// the write and can never approach the peer's read limit.
const ContentChunkBytes = 32 << 10

// Handlers returns the closed, compiled-in allowlist of methods the agent
// implements in the responder role.
//
// Keying by method alone would be wrong, but the link supplies the other half:
// a request may only arrive from the endpoint its origin names, and the agent's
// peer is the service, so every request reaching a handler here is
// service-originated by construction. There is no path by which the agent is
// asked to take the originator role for one of these methods.
func Handlers(journal *logs.Journal, logger *log.PrefixLogger) map[string]link.Handler {
	return map[string]link.Handler{
		devicelogs.Method: deviceLogsHandler(journal, logger),
	}
}

// deviceLogsHandler runs one device.logs.get request to a terminal result.
//
// It owns its request and nothing else: it never touches the link, never
// observes a sibling, and returns when its context is cancelled.
func deviceLogsHandler(journal *logs.Journal, logger *log.PrefixLogger) link.Handler {
	return func(ctx context.Context, req *link.Request, out link.Responder) {
		params, err := devicelogs.DecodeParams(req.Params)
		if err != nil {
			// A parameter fault is request-scoped and starts no source work.
			logger.Debugf("Agent link: rejecting %s for %s: %v", req.Method, req.ID, err)
			respond(out, logger, protocol.ReasonRetrievalFailed, err.Error())
			return
		}

		// Two capabilities this build does not have. Both are refused as
		// RetrievalFailed rather than MethodNotSupported: that reason means
		// "this responder does not implement this method", and answering it
		// for a method the agent does implement would tell the service this
		// agent cannot serve logs at all, which is false and would be read as
		// a capability verdict.
		if params.Follow {
			respond(out, logger, protocol.ReasonRetrievalFailed, "follow is not implemented by this agent")
			return
		}
		if params.Source != devicelogs.SourceJournal {
			respond(out, logger, protocol.ReasonRetrievalFailed,
				fmt.Sprintf("source %q is not implemented by this agent", params.Source))
			return
		}

		snapshot, err := journal.Snapshot(ctx, journalRequest(params))
		if err != nil {
			reason := reasonFor(ctx, err)
			logger.Debugf("Agent link: %s for %s failed with %s: %v", req.Method, req.ID, reason, err)
			respond(out, logger, reason, err.Error())
			return
		}

		// The page is assembled before the first byte is sent, so a failure
		// above is always a pre-content failure and arrives as a result with
		// no data in front of it.
		if err := emitContent(out, snapshot.Lines); err != nil {
			// The link could not carry the content. Try to say so for this one
			// request; if that write fails too the link is ending and every
			// request on it is failed explicitly there.
			logger.Warnf("Agent link: could not send log content for %s: %v", req.ID, err)
			respond(out, logger, protocol.ReasonRetrievalFailed, "sending log content failed")
			return
		}

		respond(out, logger, protocol.ReasonOK, "")
	}
}

// journalRequest maps the decoded wire parameters onto a journal request.
//
// It translates and does not validate: the charset of a unit pattern, the
// spelling of a priority and the mutual exclusions are the source's rules, and
// checking them twice in two places is how the two copies drift apart.
func journalRequest(p *devicelogs.Params) logs.JournalRequest {
	req := logs.JournalRequest{
		Units:        p.Journal.Units,
		Priority:     client.JournalPriority(p.Journal.Priority),
		Boot:         client.JournalBoot(p.Journal.Boot),
		SinceSeconds: p.Journal.SinceSeconds,
		SinceTime:    p.Journal.SinceTime,
		UntilTime:    p.Journal.UntilTime,
		TailLines:    p.TailLines,
	}
	if p.LineOffset != nil {
		req.LineOffset = *p.LineOffset
	}
	return req
}

// reasonFor maps a retrieval failure onto the status reason the originator
// sees. Anything unrecognised is the generic failure: a reason is a contract
// with the REST layer above, so a new device-side error must be mapped
// deliberately rather than guessed at from its text.
func reasonFor(ctx context.Context, err error) string {
	// Checked first, and from the context rather than the error: cancellation
	// means the originator already fixed this request's outcome, so whatever
	// the source reported on its way out is a consequence and not the cause.
	if ctx.Err() != nil {
		return protocol.ReasonCancelled
	}

	switch {
	case stderrors.Is(err, errors.ErrLogInvalidParameters):
		return protocol.ReasonRetrievalFailed
	case stderrors.Is(err, errors.ErrLogBootUnavailable):
		return protocol.ReasonBootUnavailable
	case stderrors.Is(err, errors.ErrLogBufferExceeded):
		return protocol.ReasonResponseTooLarge
	case stderrors.Is(err, errors.ErrLogScanIncomplete):
		// Both ways the scan can run out of room -- the wall-clock budget and
		// the entry budget -- arrive here. They are one reason to the caller
		// because the remedy is the same, a narrower filter or a smaller page;
		// the status message is what separates them for an operator.
		return protocol.ReasonDeviceTimeout
	case stderrors.Is(err, errors.ErrNotFound):
		return protocol.ReasonSourceNotFound
	default:
		return protocol.ReasonRetrievalFailed
	}
}

// respond fixes the request's outcome. A failure here means the link is
// ending; the endpoint fails every request still on it, so there is nothing
// further this handler can usefully do.
func respond(out link.Responder, logger *log.PrefixLogger, reason, message string) {
	if err := out.Result(reason, message); err != nil {
		logger.Debugf("Agent link: could not send %s result: %v", reason, err)
	}
}

// emitContent sends the snapshot as one or more data messages.
//
// Each line is written with its terminator, so every message ends on a newline
// boundary and no reader has to buffer waiting for one. A single logical line
// longer than one chunk is the exception and is split across messages on a
// UTF-8 rune boundary, never mid-code-point; the message that carries its
// first part does not end with a newline, which is exactly how a continuation
// is marked. Message boundaries are invisible either way: the lines a caller
// reconstitutes are the lines the source rendered.
//
// An empty snapshot sends no data message at all. The result alone says the
// filtered selection was empty, which a zero-length content message would say
// no more clearly and a reader would have to distinguish from a flag-only one.
func emitContent(out link.Responder, lines []string) error {
	var buf strings.Builder

	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		payload, err := devicelogs.EncodePayload(devicelogs.Payload{Content: buf.String()})
		if err != nil {
			return fmt.Errorf("encoding log content: %w", err)
		}
		buf.Reset()
		return out.Data(payload)
	}

	for _, line := range lines {
		remaining := line + "\n"
		for len(remaining) > 0 {
			if buf.Len()+len(remaining) <= ContentChunkBytes {
				buf.WriteString(remaining)
				break
			}
			if buf.Len() > 0 {
				// Prefer the newline boundary already in the buffer over
				// splitting this line, then reconsider it against an empty one.
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			cut := runeBoundary(remaining, ContentChunkBytes)
			buf.WriteString(remaining[:cut])
			remaining = remaining[cut:]
			if err := flush(); err != nil {
				return err
			}
		}
	}

	return flush()
}

// runeBoundary returns the largest offset at or below max that starts a rune,
// so a chunk never ends mid-code-point.
func runeBoundary(s string, max int) int {
	if len(s) <= max {
		return len(s)
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		// No boundary in the whole window. That needs a rune longer than max,
		// which cannot happen for valid UTF-8 while max is this large, but
		// making no progress would loop forever so the cut is taken anyway.
		return max
	}
	return cut
}
