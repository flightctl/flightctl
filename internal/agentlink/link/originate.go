package link

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
)

// OriginatedQueueCapacity bounds one originated request's inbound queue. The
// last slot is reserved for the terminal result, so an outcome is never lost
// to overflow.
const OriginatedQueueCapacity = 32

// Event is one message the responder sent for a request this endpoint
// originated.
type Event struct {
	Kind protocol.Kind
	// Payload is set for a data event.
	Payload *protocol.Value
	// Status is set for a result event and fixes the request's outcome.
	Status *protocol.Status
}

// Originated is a handle on a request this endpoint originated and still has
// outstanding.
//
// Its lifetime is the request's, not the link's. The link outlives any
// request, a request's end is never inferred from the connection closing, and
// the connection closing fails every request still running on it -- a clean
// Close with code 1000 is never a request's success.
type Originated struct {
	key requestKey
	e   *Endpoint
	ch  chan Event

	mu       sync.Mutex
	finished bool
}

// Originate sends a request on this link and returns a handle over which the
// responder's data messages and its single terminal result arrive.
//
// id must be unique within this endpoint's namespace and must never be
// reused, including after the link that carried it is replaced. That is what
// makes a late message from a superseded link unambiguously stale.
func (e *Endpoint) Originate(id, method string, params json.RawMessage, trace map[string]string) (*Originated, error) {
	key := requestKey{origin: e.role, id: id}

	data, err := protocol.EncodeRequest(e.role, id, method, params, trace)
	if err != nil {
		return nil, fmt.Errorf("composing request %s: %w", key, err)
	}

	o := &Originated{key: key, e: e, ch: make(chan Event, OriginatedQueueCapacity)}

	e.mu.Lock()
	if e.refusing {
		e.mu.Unlock()
		return nil, fmt.Errorf("originating %s: endpoint is shutting down", key)
	}
	if _, exists := e.originated[key]; exists {
		e.mu.Unlock()
		return nil, fmt.Errorf("originating %s: identifier is already outstanding", key)
	}
	e.originated[key] = o
	e.mu.Unlock()

	if err := e.enqueue(data); err != nil {
		e.mu.Lock()
		delete(e.originated, key)
		e.mu.Unlock()
		return nil, fmt.Errorf("sending request %s: %w", key, err)
	}

	return o, nil
}

// Events yields this request's data events followed by exactly one result
// event, after which the channel is closed. A closed channel with no result
// event never happens: a request whose link ended is failed explicitly.
func (o *Originated) Events() <-chan Event { return o.ch }

// Cancel stops this one retrieval without disturbing the link or any
// sibling. WebSocket supplies no per-request cancellation of its own, so the
// explicit cancel message is the only mechanism that does this.
//
// The caller does not wait for the confirming result: the outcome is fixed
// and the resources released here, and the confirmation is cleanup
// accounting only.
func (o *Originated) Cancel(reason, message string) error {
	o.fail(protocol.ReasonCancelled, message)
	return o.e.cancelOnLink(o.key, reason, message)
}

// offer hands one responder message to this request's queue. It is called
// from the reader and never waits.
func (o *Originated) offer(env *protocol.Envelope) {
	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		// Responder-kind messages naming a pair already completed are
		// dropped idempotently and counted.
		o.e.counters.DroppedUnknownPair.Add(1)
		return
	}
	o.mu.Unlock()

	if env.Kind == protocol.KindResult {
		// In-order handoff: the result is enqueued behind this request's
		// pending data, never dispatched around it, and the reserved
		// terminal slot means it always fits.
		o.deliver(Event{Kind: protocol.KindResult, Status: env.Status})
		o.finish()
		return
	}

	// Leave the terminal slot free for the outcome.
	if len(o.ch) >= cap(o.ch)-1 {
		// A full queue means this request's consumer is not keeping up. It
		// fails exactly one request, visibly: data is never silently
		// dropped and a request is never quietly shortened into a success.
		// The connection is not closed.
		o.e.log.Warnf("Agent link: queue overrun on %s, failing that request", o.key)
		o.fail(protocol.ReasonRetrievalFailed, "consumer did not keep up")
		// The cancel is enqueued to the writer rather than sent from the
		// reader.
		if err := o.e.cancelOnLink(o.key, protocol.ReasonRetrievalFailed, "queue overrun"); err != nil {
			o.e.log.Debugf("Agent link: could not cancel %s after overrun: %v", o.key, err)
		}
		return
	}

	o.deliver(Event{Kind: protocol.KindData, Payload: env.Payload})
}

func (o *Originated) deliver(ev Event) {
	select {
	case o.ch <- ev:
	default:
		o.e.log.Errorf("Agent link: dropped a %s event for %s with no slot available", ev.Kind, o.key)
	}
}

// fail fixes this request's outcome locally without waiting for the peer.
func (o *Originated) fail(reason, message string) {
	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		return
	}
	o.mu.Unlock()

	o.deliver(Event{Kind: protocol.KindResult, Status: &protocol.Status{Reason: reason, Message: message}})
	o.finish()
}

func (o *Originated) finish() {
	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		return
	}
	o.finished = true
	o.mu.Unlock()

	close(o.ch)

	o.e.mu.Lock()
	delete(o.e.originated, o.key)
	o.e.completed.add(o.key)
	o.e.mu.Unlock()
}

// cancelOnLink enqueues a cancel for one request. It never closes the
// connection and never disturbs a sibling.
func (e *Endpoint) cancelOnLink(key requestKey, reason, message string) error {
	data, err := protocol.EncodeCancel(key.origin, key.id, reason, message)
	if err != nil {
		return fmt.Errorf("composing cancel for %s: %w", key, err)
	}
	return e.enqueue(data)
}
