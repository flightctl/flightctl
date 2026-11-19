package link

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/gorilla/websocket"
)

// runReader is the one dedicated reader for the life of the link. It reads,
// validates, attributes and dispatches, and nothing else.
//
// It must never wait on application work. On its own goroutine it performs no
// write to an application consumer, no data-message write to the socket
// (including a cancel), no key-value call, no start or stop of a source, and
// no decode of an unbounded body -- the message bound is applied before the
// decode, not after. Everything else is handed to the owning request's worker
// or to the writer through a bounded non-blocking offer.
//
// One honest exception: the library's default control handlers answer a Ping
// with a Pong and a Close with a Close from inside the read path. That write
// is bounded at 125 payload bytes, so it is a bounded pause rather than a
// dependency on an application consumer. The claim is precisely that the
// reader never waits on application work, not that the reader never writes.
func (e *Endpoint) runReader() error {
	for {
		msgType, data, err := e.ws.ReadMessage()
		if err != nil {
			// The reader must not spin after a failure: the library panics
			// after a thousand reads on a failed connection, so the first
			// read error ends the link and the loop exits.
			return e.classifyReadError(err)
		}

		if msgType != websocket.TextMessage {
			// A BINARY message cannot be attributed and is a link-level
			// protocol violation.
			e.failLink(websocket.CloseProtocolError, "binary frames are not used")
			return errors.New("link: received a binary message")
		}

		env, err := protocol.Decode(data)
		if err != nil {
			var lv *protocol.LinkViolation
			if errors.As(err, &lv) {
				e.failLink(lv.CloseCode, lv.Text)
				return fmt.Errorf("link: %w", err)
			}
			var rv *protocol.RequestViolation
			if errors.As(err, &rv) {
				e.counters.RequestViolations.Add(1)
				e.handleRequestViolation(rv)
				continue
			}
			// Decode only ever returns one of the two above; treat anything
			// else as unattributable rather than guessing.
			e.failLink(websocket.CloseProtocolError, "protocol error")
			return fmt.Errorf("link: undecodable message: %w", err)
		}

		if lv := e.dispatch(env); lv != nil {
			e.failLink(lv.CloseCode, lv.Text)
			return fmt.Errorf("link: %w", lv)
		}
	}
}

// classifyReadError turns the library's read error into the link's outcome.
// A clean Close with code 1000 is never a logical request's success, so no
// close code is translated into one here: the caller fails every request on
// the link regardless.
func (e *Endpoint) classifyReadError(err error) error {
	if errors.Is(err, websocket.ErrReadLimit) {
		// Link-scoped by construction: the library has already sent 1009.
		e.log.Warnf("Agent link: peer sent a message over the %d byte read limit", protocol.MaxMessageBytes)
		return fmt.Errorf("link: read limit exceeded: %w", err)
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return fmt.Errorf("link: peer closed the connection: %w", err)
	}
	return fmt.Errorf("link: read failed: %w", err)
}

// handleRequestViolation reports a fault in a message that could be
// attributed. Exactly one request fails; the link and its siblings continue.
func (e *Endpoint) handleRequestViolation(rv *protocol.RequestViolation) {
	e.log.Debugf("Agent link: request-scoped protocol violation for %s/%s: %v", rv.Origin, rv.ID, rv.Err)

	// Enforce sender roles on the error path, not only the happy path.
	// Without this a wrong-role message with a decode fault would bypass
	// dispatch's SenderMayUse check and could cancel active work.
	if !protocol.SenderMayUse(e.peerRole(), rv.Kind, rv.Origin) {
		e.counters.DroppedRoleViolation.Add(1)
		e.log.Debugf("Agent link: dropping violation for %s/%s from the wrong sender role", rv.Origin, rv.ID)
		return
	}

	key := requestKey{origin: rv.Origin, id: rv.ID}

	e.mu.Lock()
	st, live := e.active[key]
	orig, outstanding := e.originated[key]
	finished := e.completed.has(key)
	e.mu.Unlock()

	switch {
	case live:
		// A request we are responding to: cancel its work. Its worker sends
		// the terminal result.
		st.cancel()
	case outstanding:
		// A request we originated: fix its outcome locally. Nothing is sent
		// back, because a malformed responder message is not itself answered.
		orig.fail(rv.Reason, rv.Err.Error())
	case rv.Kind == protocol.KindRequest:
		// A malformed request is answered with exactly one result, so its
		// originator is not left waiting. This cannot loop: a result is
		// itself never replied to.
		if finished {
			e.counters.DroppedStaleDuplicate.Add(1)
			return
		}
		e.sendResult(key, rv.Reason, "request rejected: "+rv.Err.Error())
	default:
		// A malformed data, result or late cancel naming nothing we know:
		// dropped and counted, never acted on and never answered.
		e.counters.DroppedUnknownPair.Add(1)
	}
}

// dispatch resolves one decoded message. It returns a link violation only for
// a fault that cannot be attributed at all; everything else is handled in
// place, because attributable faults fail one request and unattributable
// faults fail the link, with nothing in between silently continuing.
func (e *Endpoint) dispatch(env *protocol.Envelope) *protocol.LinkViolation {
	// Sender role is enforced, not assumed. A request or cancel from the
	// endpoint that is not the named originator, or a data or result from
	// the endpoint that is, is dropped and counted -- nothing started,
	// nothing answered, and never re-resolved as though it came from the
	// other side.
	if !protocol.SenderMayUse(e.peerRole(), env.Kind, env.Origin) {
		e.counters.DroppedRoleViolation.Add(1)
		e.log.Debugf("Agent link: dropping %s message for %s/%s sent by the wrong role",
			env.Kind, env.Origin, env.ID)
		return nil
	}

	switch env.Kind {
	case protocol.KindRequest:
		return e.dispatchRequest(env)
	case protocol.KindCancel:
		e.dispatchCancel(env)
		return nil
	case protocol.KindData, protocol.KindResult:
		e.dispatchResponderMessage(env)
		return nil
	default:
		return nil
	}
}

// dispatchRequest resolves a peer-originated request. Only request may create
// state; data, result and cancel are moves on work that already exists.
func (e *Endpoint) dispatchRequest(env *protocol.Envelope) *protocol.LinkViolation {
	key := requestKey{origin: env.Origin, id: env.ID}

	e.mu.Lock()
	if _, live := e.active[key]; live {
		e.mu.Unlock()
		// We cannot decide which retrieval owns a live identifier, so no
		// identifier is recycled to resolve the collision: the link fails.
		return &protocol.LinkViolation{
			CloseCode: websocket.ClosePolicyViolation,
			Text:      "duplicate live request identifier",
			Err:       fmt.Errorf("request names the currently active identifier %s", key),
		}
	}
	if e.completed.has(key) {
		e.mu.Unlock()
		// The request it names is already finished and nothing is ambiguous.
		e.counters.DroppedStaleDuplicate.Add(1)
		e.log.Debugf("Agent link: dropping stale duplicate request %s", key)
		return nil
	}
	refusing := e.refusing
	e.mu.Unlock()

	if refusing {
		// A request arriving between "refuse new starts" and "connection
		// closed" is refused explicitly for its id -- not MethodNotSupported,
		// not silently dropped. The refusal is post-accept and is never
		// re-resolved.
		e.sendResult(key, protocol.ReasonRetrievalFailed, "endpoint is shutting down")
		return nil
	}

	// Method resolution happens after the envelope and before the
	// parameters, which is what makes MethodNotSupported reachable: under
	// the strict unknown-member rule the params of an unimplemented method
	// is a shape this endpoint cannot validate.
	handler, ok := e.handlers[env.Method]
	if !ok {
		e.counters.MethodNotSupported.Add(1)
		e.log.Debugf("Agent link: refusing unimplemented method %q for %s", env.Method, key)
		// Exactly one result, no work started, link and siblings untouched.
		// The diagnostic is bounded before it is built, so an oversized
		// method cannot force an oversized reply.
		e.sendResult(key, protocol.ReasonMethodNotSupported,
			fmt.Sprintf("method %q is not implemented", env.Method))
		return nil
	}

	e.startWorker(key, env, handler)
	return nil
}

// startWorker begins a new retrieval worker for an accepted request.
func (e *Endpoint) startWorker(key requestKey, env *protocol.Envelope, handler Handler) {
	ctx, cancel := context.WithCancel(e.linkCtx)
	st := &requestState{cancel: cancel, done: make(chan struct{})}

	e.mu.Lock()
	if e.refusing {
		// Lost a race with shutdown step 2 after the check above.
		e.mu.Unlock()
		cancel()
		e.sendResult(key, protocol.ReasonRetrievalFailed, "endpoint is shutting down")
		return
	}
	e.active[key] = st
	e.mu.Unlock()

	req := &Request{
		Origin: env.Origin,
		ID:     env.ID,
		Method: env.Method,
		Params: env.Params,
		Trace:  env.Trace,
	}

	go func() {
		defer close(st.done)
		defer cancel()
		defer e.finishRequest(key, st)
		handler(ctx, req, &responder{endpoint: e, key: key, state: st})
	}()
}

// finishRequest retires a request. One result each: if the handler returned
// without fixing an outcome, the endpoint fixes it rather than leaving the
// originator waiting.
func (e *Endpoint) finishRequest(key requestKey, st *requestState) {
	if !st.resultSent.Load() {
		e.sendResultOnce(key, st, protocol.ReasonRetrievalFailed, "handler returned without a result")
	}
	e.mu.Lock()
	delete(e.active, key)
	e.completed.add(key)
	e.mu.Unlock()
}

// dispatchCancel hands a cancel to the named request's worker. A cancel is
// per request and never touches the link: it must not close the connection
// and must not disturb any sibling.
func (e *Endpoint) dispatchCancel(env *protocol.Envelope) {
	key := requestKey{origin: env.Origin, id: env.ID}

	e.mu.Lock()
	st, ok := e.active[key]
	e.mu.Unlock()

	if !ok {
		// A cancel naming an unknown or finished identifier is dropped
		// idempotently.
		e.counters.DroppedUnknownPair.Add(1)
		return
	}
	e.log.Debugf("Agent link: cancelling %s", key)
	st.cancel()
}

// dispatchResponderMessage routes a data or result message to the request
// this endpoint originated. One naming an unknown or finished pair is dropped
// and counted: it never creates work.
func (e *Endpoint) dispatchResponderMessage(env *protocol.Envelope) {
	key := requestKey{origin: env.Origin, id: env.ID}

	e.mu.Lock()
	orig, ok := e.originated[key]
	e.mu.Unlock()

	if !ok {
		e.counters.DroppedUnknownPair.Add(1)
		e.log.Debugf("Agent link: dropping %s naming unknown pair %s", env.Kind, key)
		return
	}

	// The offer is bounded and non-blocking: the reader hands off and
	// returns, never waiting on the consumer behind this request.
	orig.offer(env)
}
