package link

import (
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/gorilla/websocket"
)

// errWriteQueueFull is returned when a message cannot be handed to the writer
// without waiting. The handoff either succeeds or reports the queue full; it
// never blocks its caller.
var errWriteQueueFull = errors.New("link: writer queue is full")

// errLinkClosed is returned once the link has ended.
var errLinkClosed = errors.New("link: closed")

// runWriter is the one dedicated writer for the life of the link. The library
// supports exactly one concurrent reader and one concurrent writer, which is
// why this structure is mandatory rather than stylistic. Control frames are
// issued through WriteControl without coordinating with it.
//
// Within a single logical request output keeps its order. Fair rotation
// between concurrent requests is an ordering discipline this queue does not
// yet need: the link carries no method, so no request streams. It arrives
// with the first method that does, together with that method's bounded
// per-request output buffer.
func (e *Endpoint) runWriter() {
	for {
		select {
		case <-e.linkCtx.Done():
			return
		case msg := <-e.writeCh:
			if msg.data == nil {
				// A bare flush barrier: everything queued ahead of it has
				// already been written.
				if msg.flushed != nil {
					close(msg.flushed)
				}
				continue
			}
			// A finite write deadline so a wedged peer cannot block the
			// writer forever.
			if err := e.ws.SetWriteDeadline(time.Now().Add(protocol.WriteTimeout)); err != nil {
				e.failLink(websocket.CloseInternalServerErr, "write fault")
				return
			}
			err := e.ws.WriteMessage(websocket.TextMessage, msg.data)
			if msg.flushed != nil {
				close(msg.flushed)
			}
			if err != nil {
				// A timed-out write poisons the connection -- the library is
				// explicit that after a write has timed out the state is
				// corrupt and all future writes fail. So this is link-scoped:
				// the connection is closed and every request on it fails
				// explicitly. It is never claimed that only the request whose
				// message was in flight is affected.
				e.log.Debugf("Agent link: write failed, failing the link: %v", err)
				e.failLink(websocket.CloseInternalServerErr, "write fault")
				return
			}
		}
	}
}

// enqueue offers one composed message to the writer without waiting.
func (e *Endpoint) enqueue(data []byte) error {
	select {
	case <-e.linkCtx.Done():
		return errLinkClosed
	default:
	}
	select {
	case e.writeCh <- outbound{data: data}:
		return nil
	default:
		return errWriteQueueFull
	}
}

// sendResult composes and enqueues a terminal result for a request that has
// no worker of its own -- a refused method, or a request arriving during
// shutdown.
//
// A terminal result that cannot be enqueued means the link cannot fulfil its
// one-result-each contract, so the link is failed rather than leaving the
// peer waiting on an open connection.
func (e *Endpoint) sendResult(key requestKey, reason, message string) {
	// The identifier is retired even though no work ran, so a re-delivery of
	// the same request is recognised as a stale duplicate rather than
	// answered twice.
	e.mu.Lock()
	e.completed.add(key)
	e.mu.Unlock()

	data, err := protocol.EncodeResult(key.origin, key.id, reason, message)
	if err != nil {
		e.log.Errorf("Agent link: cannot compose result for %s: %v", key, err)
		return
	}
	if err := e.enqueue(data); err != nil {
		e.log.Debugf("Agent link: cannot send result for %s: %v", key, err)
		if errors.Is(err, errWriteQueueFull) {
			e.failLink(websocket.CloseInternalServerErr, "cannot deliver terminal result")
		}
	}
}

// sendResultOnce fixes a worker-owned request's outcome, honouring the
// one-result-each rule.
//
// The result is encoded before the terminal flag is committed, so an
// encoding failure does not consume the one-shot and finishRequest can
// still send the fallback.
func (e *Endpoint) sendResultOnce(key requestKey, st *requestState, reason, message string) error {
	data, err := protocol.EncodeResult(key.origin, key.id, reason, message)
	if err != nil {
		return fmt.Errorf("composing result for %s: %w", key, err)
	}
	if !st.resultSent.CompareAndSwap(false, true) {
		// The outcome was already fixed. Later events for this identifier
		// are diagnostics and never overwrite it.
		return nil
	}
	if err := e.enqueue(data); err != nil {
		// Uncommit so finishRequest can send the fallback RetrievalFailed.
		st.resultSent.Store(false)
		return err
	}
	return nil
}

// runSupervisor is the one timer-driven goroutine per link that owns
// liveness. A reader blocked in ReadMessage observes what arrives; it cannot
// observe a Pong that never does, so the keepalive needs an actor of its own.
func (e *Endpoint) runSupervisor() {
	pingTicker := time.NewTicker(protocol.PingInterval)
	defer pingTicker.Stop()

	pongTimer := time.NewTimer(protocol.PongWait)
	if !pongTimer.Stop() {
		<-pongTimer.C
	}
	defer pongTimer.Stop()
	awaitingPong := false

	for {
		select {
		case <-e.linkCtx.Done():
			return

		case <-pingTicker.C:
			deadline := time.Now().Add(protocol.ControlWriteTimeout)
			if err := e.ws.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				// A failed or timed-out control write is a socket fault, not
				// a missing Pong, and fails the link at once.
				e.log.Debugf("Agent link: ping write failed, failing the link: %v", err)
				e.failLink(websocket.CloseInternalServerErr, "ping write failed")
				return
			}
			// The Pong-wait is armed from the successful return of that
			// WriteControl, never from a merely queued Ping.
			if !awaitingPong {
				pongTimer.Reset(protocol.PongWait)
				awaitingPong = true
			}

		case <-e.pongCh:
			if awaitingPong {
				if !pongTimer.Stop() {
					select {
					case <-pongTimer.C:
					default:
					}
				}
				awaitingPong = false
			}

		case <-pongTimer.C:
			// On expiry the supervisor closes the connection rather than
			// only recording a fault. That unblocks the reader, every
			// request on the link is failed explicitly, and the agent
			// redials. Cancelling request contexts without closing the
			// socket would leave the reader parked on a dead peer.
			e.log.Warnf("Agent link: no pong within %s, failing the link", protocol.PongWait)
			e.failLink(websocket.CloseInternalServerErr, "pong timeout")
			return
		}
	}
}

// failLink ends the link with a close code. The code is a diagnostic about
// the connection and is never rendered as a request's status.
//
// It sends a Close frame where one can still be sent, then closes the
// underlying connection, which unblocks the reader so the single cleanup path
// in Serve runs.
func (e *Endpoint) failLink(code int, text string) {
	e.closeOnce.Do(func() {
		e.closeErr = fmt.Errorf("link closed with code %d: %s", code, text)

		msg := websocket.FormatCloseMessage(code, protocol.CloseText(text))
		deadline := time.Now().Add(protocol.ControlWriteTimeout)
		if err := e.ws.WriteControl(websocket.CloseMessage, msg, deadline); err != nil {
			e.log.Debugf("Agent link: could not send close frame (%d): %v", code, err)
		}
		// Closing the connection is what actually ends the link: it unblocks
		// a reader parked in ReadMessage.
		_ = e.ws.Close()
	})
}
