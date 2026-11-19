package link

import (
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/gorilla/websocket"
)

// shutdown runs the ordered shutdown sequence.
//
// The order matters and is not an optimisation. Ceasing to accept work is not
// enough on its own -- an already-open link is still registered and can still
// be given a request this endpoint is about to abandon -- but closing first
// is impossible, because the link is the only way to deliver a terminal
// result to the requests still running on it.
//
//  1. Refuse new starts locally, explicitly, per request.
//  2. Finish or fail each running request while the connection is still
//     writable.
//  3. Then send the Close frame.
//
// Step 1 of the full sequence -- stop redialling -- belongs to the agent's
// link manager, not to one connection, and happens before Serve's context is
// cancelled.
func (e *Endpoint) shutdown() {
	// Step: refuse new starts. A request arriving from here on is failed
	// explicitly for its own id with a non-OK result, and no work begins. It
	// is never silently ignored and never left running orphaned.
	e.mu.Lock()
	e.refusing = true
	running := make([]*requestState, 0, len(e.active))
	for _, st := range e.active {
		running = append(running, st)
	}
	outstanding := make([]*Originated, 0, len(e.originated))
	for _, o := range e.originated {
		outstanding = append(outstanding, o)
	}
	e.mu.Unlock()

	// Step: finish or fail each running request while the connection is
	// still writable. A finite request that can complete within the budget
	// does so and sends its own OK; everything else is cancelled with an
	// explicit non-OK result for its own id. One result each.
	deadline := time.After(e.shutdownBudget)
	for _, st := range running {
		select {
		case <-st.done:
		case <-deadline:
			// Out of budget: cancel the rest and let their workers send
			// their own results.
			for _, remaining := range running {
				remaining.cancel()
			}
			e.drainRunning(running)
			e.finishShutdown(outstanding)
			return
		case <-e.readerExited:
			// The connection died underneath us; the link-end path in Serve
			// fails everything explicitly.
			return
		}
	}

	e.finishShutdown(outstanding)
}

// drainRunning gives cancelled workers a brief chance to write their terminal
// results before the Close frame goes out.
func (e *Endpoint) drainRunning(running []*requestState) {
	grace := time.After(protocol.ControlWriteTimeout)
	for _, st := range running {
		select {
		case <-st.done:
		case <-grace:
			return
		case <-e.readerExited:
			return
		}
	}
}

// finishShutdown fails anything this endpoint originated and then sends the
// Close frame, which is the last thing that happens on the link.
func (e *Endpoint) finishShutdown(outstanding []*Originated) {
	for _, o := range outstanding {
		o.fail(protocol.ReasonRetrievalFailed, "endpoint is shutting down")
	}

	// Let the writer drain what the previous steps queued, so the Close
	// frame really is last.
	e.waitForWriterDrain()

	// The close code is a diagnostic about the connection and conveys
	// nothing about the requests resolved above. 1001 is the natural choice
	// for an endpoint going away.
	e.failLink(websocket.CloseGoingAway, "shutting down")
}

// waitForWriterDrain waits, briefly and finitely, for every message queued so
// far to reach the socket.
//
// A queue-length check would not do: the writer can have dequeued a message
// and not yet written it, so an empty queue does not mean a drained one.
// Instead a barrier is queued behind everything else and waited on, which the
// single in-order writer only reaches once the messages ahead of it are out.
func (e *Endpoint) waitForWriterDrain() {
	flushed := make(chan struct{})
	select {
	case e.writeCh <- outbound{flushed: flushed}:
	case <-e.readerExited:
		return
	case <-e.linkCtx.Done():
		return
	case <-time.After(protocol.ControlWriteTimeout):
		return
	}

	select {
	case <-flushed:
	case <-e.readerExited:
	case <-e.linkCtx.Done():
	case <-time.After(protocol.ControlWriteTimeout):
	}
}

// failAllActive fails every request still running when the link ends.
//
// Each affected request is failed individually and explicitly; nothing is
// left to time out silently. This runs for every link end -- a Close from
// either side including 1000, a transport error, loss or reset -- because a
// polite 1000 and a 1006 differ in diagnostics and nothing else.
func (e *Endpoint) failAllActive() {
	e.mu.Lock()
	running := make([]*requestState, 0, len(e.active))
	for _, st := range e.active {
		running = append(running, st)
	}
	outstanding := make([]*Originated, 0, len(e.originated))
	for _, o := range e.originated {
		outstanding = append(outstanding, o)
	}
	e.mu.Unlock()

	// A retrieval still running has its source work stopped and its buffers
	// released through its own context being cancelled, whether or not it
	// ever produced output.
	for _, st := range running {
		st.cancel()
	}
	// Reconnection is not replay: a request in flight on a broken link fails
	// here and the replacement link starts nothing on its behalf.
	for _, o := range outstanding {
		o.fail(protocol.ReasonRetrievalFailed, "link ended before a result was received")
	}
}
